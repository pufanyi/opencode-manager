package provider

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/pufanyi/opencode-manager/internal/store"
)

// sessionCmd tracks one active claude process for a session.
type sessionCmd struct {
	cmd    *exec.Cmd
	cancel context.CancelFunc
}

// ClaudeCodeProvider manages Claude Code CLI invocations.
// Supports concurrent prompts across different sessions via git worktrees.
type ClaudeCodeProvider struct {
	binary string
	dir    string
	store  *store.Store
	instID string // instance ID for session tracking

	mu           sync.Mutex
	activeCmds   map[string]*sessionCmd // sessionID → active command
	usedSessions map[string]bool        // sessions that have been prompted at least once

	// mergeMu serializes merge+sync operations to avoid conflicts.
	mergeMu sync.Mutex
}

func NewClaudeCodeProvider(binary, dir string, st *store.Store, instanceID string) *ClaudeCodeProvider {
	return &ClaudeCodeProvider{
		binary:       binary,
		dir:          dir,
		store:        st,
		instID:       instanceID,
		activeCmds:   make(map[string]*sessionCmd),
		usedSessions: make(map[string]bool),
	}
}

func (p *ClaudeCodeProvider) Type() Type { return TypeClaudeCode }

func (p *ClaudeCodeProvider) Start(ctx context.Context) error {
	// Validate binary exists
	if _, err := exec.LookPath(p.binary); err != nil {
		return fmt.Errorf("claude binary not found: %w", err)
	}

	// Pre-load existing sessions so we use --resume for them
	existing, err := p.store.ListClaudeSessions(p.instID)
	if err == nil {
		p.mu.Lock()
		for _, s := range existing {
			if s.MessageCount > 0 {
				p.usedSessions[s.ID] = true
			}
		}
		p.mu.Unlock()
	}

	slog.Info("claude code provider ready", "dir", p.dir)
	return nil
}

func (p *ClaudeCodeProvider) Stop() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	for sid, sc := range p.activeCmds {
		sc.cancel()
		delete(p.activeCmds, sid)
	}
	return nil
}

func (p *ClaudeCodeProvider) WaitReady(ctx context.Context, timeout time.Duration) error {
	if _, err := exec.LookPath(p.binary); err != nil {
		return fmt.Errorf("claude binary not found: %w", err)
	}
	return nil
}

func (p *ClaudeCodeProvider) Wait() error { return nil }

func (p *ClaudeCodeProvider) Stderr() string { return "" }

func (p *ClaudeCodeProvider) SetPort(port int) {}

func (p *ClaudeCodeProvider) IsReady() bool { return true }

func (p *ClaudeCodeProvider) HealthCheck(ctx context.Context) error {
	cmd := exec.CommandContext(ctx, p.binary, "--version")
	return cmd.Run()
}

// isGitRepo checks whether the instance directory is a git repository.
func (p *ClaudeCodeProvider) isGitRepo() bool {
	cmd := exec.Command("git", "-C", p.dir, "rev-parse", "--is-inside-work-tree")
	return cmd.Run() == nil
}

// currentBranch returns the current branch name in the main repo.
func (p *ClaudeCodeProvider) currentBranch() string {
	out, err := exec.Command("git", "-C", p.dir, "rev-parse", "--abbrev-ref", "HEAD").Output()
	if err != nil {
		return "main"
	}
	return strings.TrimSpace(string(out))
}

// createWorktree creates a git worktree for a session and returns (worktreePath, branchName, error).
func (p *ClaudeCodeProvider) createWorktree(sessionID string) (string, string, error) {
	branch := fmt.Sprintf("session/%s", sessionID[:12])
	wtPath := filepath.Join(os.TempDir(), "opencode-manager", p.instID, "worktrees", sessionID[:12])

	if err := os.MkdirAll(filepath.Dir(wtPath), 0o755); err != nil {
		return "", "", fmt.Errorf("mkdir for worktree: %w", err)
	}

	// Create worktree with a new branch based on current HEAD
	cmd := exec.Command("git", "-C", p.dir, "worktree", "add", "-b", branch, wtPath, "HEAD")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", "", fmt.Errorf("git worktree add: %s: %w", stderr.String(), err)
	}

	slog.Info("created git worktree", "session", sessionID[:12], "path", wtPath, "branch", branch)
	return wtPath, branch, nil
}

// removeWorktree cleans up a git worktree.
func (p *ClaudeCodeProvider) removeWorktree(wtPath string) error {
	// First try git worktree remove
	cmd := exec.Command("git", "-C", p.dir, "worktree", "remove", "--force", wtPath)
	if err := cmd.Run(); err != nil {
		slog.Warn("git worktree remove failed, cleaning up manually", "path", wtPath, "error", err)
		os.RemoveAll(wtPath)
	}

	// Prune stale worktrees
	exec.Command("git", "-C", p.dir, "worktree", "prune").Run()
	return nil
}

// mergeAndSync merges a worktree branch back to the base branch and syncs other worktrees.
func (p *ClaudeCodeProvider) mergeAndSync(sessionID, wtPath, branch string) error {
	p.mergeMu.Lock()
	defer p.mergeMu.Unlock()

	baseBranch := p.currentBranch()

	// Check if the session branch has any new commits vs the base
	diffCmd := exec.Command("git", "-C", wtPath, "diff", "--quiet", baseBranch+".."+branch)
	if diffCmd.Run() == nil {
		slog.Info("no changes to merge", "session", sessionID[:12], "branch", branch)
		return nil
	}

	// Merge session branch into base branch in the main repo
	mergeCmd := exec.Command("git", "-C", p.dir, "merge", "--no-ff", "-m",
		fmt.Sprintf("Merge session/%s", sessionID[:12]), branch)
	var mergeStderr bytes.Buffer
	mergeCmd.Stderr = &mergeStderr
	if err := mergeCmd.Run(); err != nil {
		// Abort the failed merge
		exec.Command("git", "-C", p.dir, "merge", "--abort").Run()
		return fmt.Errorf("merge failed (branch %s): %s: %w", branch, mergeStderr.String(), err)
	}

	slog.Info("merged session branch", "session", sessionID[:12], "branch", branch, "into", baseBranch)

	// Sync all other active worktrees by rebasing them onto updated base
	p.syncWorktrees(sessionID, baseBranch)

	// Delete the merged branch
	exec.Command("git", "-C", p.dir, "branch", "-d", branch).Run()

	return nil
}

// syncWorktrees rebases all active worktrees (except the one that just merged) onto the base branch.
func (p *ClaudeCodeProvider) syncWorktrees(excludeSessionID, baseBranch string) {
	sessions, err := p.store.ListClaudeSessions(p.instID)
	if err != nil {
		slog.Warn("failed to list sessions for sync", "error", err)
		return
	}

	for _, s := range sessions {
		if s.ID == excludeSessionID || s.WorktreePath == "" {
			continue
		}
		// Check the worktree still exists
		if _, err := os.Stat(s.WorktreePath); os.IsNotExist(err) {
			continue
		}

		// Rebase the session branch onto the updated base
		rebaseCmd := exec.Command("git", "-C", s.WorktreePath, "rebase", baseBranch)
		var stderr bytes.Buffer
		rebaseCmd.Stderr = &stderr
		if err := rebaseCmd.Run(); err != nil {
			slog.Warn("rebase failed for worktree, aborting rebase",
				"session", s.ID[:12], "error", stderr.String())
			exec.Command("git", "-C", s.WorktreePath, "rebase", "--abort").Run()
		} else {
			slog.Info("synced worktree", "session", s.ID[:12])
		}
	}
}

func (p *ClaudeCodeProvider) CreateSession(ctx context.Context) (*Session, error) {
	id := uuid.New().String()

	var wtPath, branch string
	if p.isGitRepo() {
		var err error
		wtPath, branch, err = p.createWorktree(id)
		if err != nil {
			slog.Warn("failed to create worktree, session will use main dir", "error", err)
			// Fall back to running in the main directory (no concurrency isolation)
		}
	}

	if err := p.store.CreateClaudeSession(p.instID, id, "", wtPath, branch); err != nil {
		// Clean up worktree if session DB insert fails
		if wtPath != "" {
			p.removeWorktree(wtPath)
		}
		return nil, err
	}
	return &Session{ID: id, Title: ""}, nil
}

func (p *ClaudeCodeProvider) GetSession(ctx context.Context, sessionID string) (*Session, error) {
	s, err := p.store.GetClaudeSession(sessionID)
	if err != nil {
		return nil, err
	}
	if s == nil {
		return nil, fmt.Errorf("session not found")
	}
	return &Session{ID: s.ID, Title: s.Title}, nil
}

func (p *ClaudeCodeProvider) ListSessions(ctx context.Context) ([]Session, error) {
	sessions, err := p.store.ListClaudeSessions(p.instID)
	if err != nil {
		return nil, err
	}
	result := make([]Session, len(sessions))
	for i, s := range sessions {
		result[i] = Session{ID: s.ID, Title: s.Title}
	}
	return result, nil
}

func (p *ClaudeCodeProvider) Prompt(ctx context.Context, sessionID string, content string) (<-chan StreamEvent, error) {
	p.mu.Lock()
	// Kill any existing prompt for THIS session only (not other sessions)
	if old, ok := p.activeCmds[sessionID]; ok {
		old.cancel()
		delete(p.activeCmds, sessionID)
	}

	cmdCtx, cancel := context.WithCancel(ctx)
	isResume := p.usedSessions[sessionID]
	p.mu.Unlock()

	// Determine working directory: use worktree if available
	workDir := p.dir
	cs, err := p.store.GetClaudeSession(sessionID)
	if err == nil && cs != nil && cs.WorktreePath != "" {
		if _, statErr := os.Stat(cs.WorktreePath); statErr == nil {
			workDir = cs.WorktreePath
		}
	}

	args := []string{
		"-p", content,
		"--output-format", "stream-json",
		"--verbose",
		"--include-partial-messages",
		"--permission-mode", "bypassPermissions",
	}
	if isResume {
		args = append(args, "--resume", sessionID)
	} else {
		args = append(args, "--session-id", sessionID)
	}

	cmd := exec.CommandContext(cmdCtx, p.binary, args...)
	cmd.Dir = workDir

	var stderrBuf bytes.Buffer
	cmd.Stderr = &stderrBuf

	p.mu.Lock()
	p.activeCmds[sessionID] = &sessionCmd{cmd: cmd, cancel: cancel}
	p.mu.Unlock()

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("creating stdout pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("starting claude: %w", err)
	}

	p.mu.Lock()
	p.usedSessions[sessionID] = true
	p.mu.Unlock()

	slog.Info("claude prompt started", "session", sessionID, "pid", cmd.Process.Pid, "dir", workDir)

	ch := make(chan StreamEvent, 64)

	go func() {
		defer close(ch)
		defer cancel()

		var acc textAccumulator

		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 0, 1024*1024), 1024*1024)

		for scanner.Scan() {
			line := scanner.Bytes()
			if len(line) == 0 {
				continue
			}

			evt := parseClaudeEvent(line, &acc)
			if evt != nil {
				select {
				case ch <- *evt:
				case <-cmdCtx.Done():
					return
				}
			}
		}

		// Wait for process to exit
		waitErr := cmd.Wait()

		p.mu.Lock()
		delete(p.activeCmds, sessionID)
		p.mu.Unlock()

		if waitErr != nil && cmdCtx.Err() == nil {
			errMsg := waitErr.Error()
			if stderr := stderrBuf.String(); stderr != "" {
				errMsg = stderr
				slog.Error("claude process failed", "error", waitErr, "stderr", stderr)
			}
			select {
			case ch <- StreamEvent{Type: "error", Error: errMsg}:
			default:
			}
		}

		// Auto-merge if this is a worktree session and prompt succeeded
		if waitErr == nil && cs != nil && cs.WorktreePath != "" && cs.Branch != "" {
			if mergeErr := p.mergeAndSync(sessionID, cs.WorktreePath, cs.Branch); mergeErr != nil {
				slog.Error("auto-merge failed", "session", sessionID, "error", mergeErr)
				select {
				case ch <- StreamEvent{Type: "error", Error: fmt.Sprintf("Auto-merge failed: %s", mergeErr)}:
				default:
				}
			}
		}

		// Send done event
		select {
		case ch <- StreamEvent{Type: "done", Done: true}:
		default:
		}
	}()

	return ch, nil
}

func (p *ClaudeCodeProvider) Abort(ctx context.Context, sessionID string) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if sc, ok := p.activeCmds[sessionID]; ok {
		sc.cancel()
		if sc.cmd != nil && sc.cmd.Process != nil {
			_ = sc.cmd.Process.Kill()
		}
		delete(p.activeCmds, sessionID)
	}
	return nil
}

// DeleteSession removes the worktree and branch for a session.
func (p *ClaudeCodeProvider) DeleteSession(ctx context.Context, sessionID string) error {
	cs, err := p.store.GetClaudeSession(sessionID)
	if err != nil {
		return err
	}
	if cs == nil {
		return nil
	}

	// Abort any running prompt
	_ = p.Abort(ctx, sessionID)

	// Remove worktree
	if cs.WorktreePath != "" {
		p.removeWorktree(cs.WorktreePath)
	}

	// Delete the branch
	if cs.Branch != "" {
		exec.Command("git", "-C", p.dir, "branch", "-D", cs.Branch).Run()
	}

	return p.store.DeleteClaudeSession(sessionID)
}

// Claude Code stream-json event types (with --include-partial-messages).
type claudeEvent struct {
	Type    string `json:"type"`
	Subtype string `json:"subtype,omitempty"`

	Event     *claudeStreamEvent     `json:"event,omitempty"`
	Message   *claudeMessage         `json:"message,omitempty"`
	Result    string                 `json:"result,omitempty"`
	IsError   bool                   `json:"is_error,omitempty"`
	Error     string                 `json:"error,omitempty"`
	Tool      *claudeTool            `json:"tool,omitempty"`
	ToolInput map[string]interface{} `json:"tool_input,omitempty"`
}

type claudeStreamEvent struct {
	Type         string       `json:"type"`
	Delta        *claudeDelta `json:"delta,omitempty"`
	ContentBlock *claudeBlock `json:"content_block,omitempty"`
}

type claudeDelta struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

type claudeMessage struct {
	Content []claudeBlock `json:"content"`
}

type claudeBlock struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
	Name string `json:"name,omitempty"`
}

type claudeTool struct {
	Name  string                 `json:"name,omitempty"`
	Input map[string]interface{} `json:"input,omitempty"`
}

type textAccumulator struct {
	buf strings.Builder
}

func (a *textAccumulator) append(delta string) string {
	a.buf.WriteString(delta)
	return a.buf.String()
}

func (a *textAccumulator) reset() {
	a.buf.Reset()
}

// extractToolDetail extracts a human-readable detail string from tool input.
// For Agent tools, this is the "description" field.
func extractToolDetail(name string, tool *claudeTool, topInput map[string]interface{}) string {
	if name != "Agent" {
		return ""
	}
	// Try tool.Input (nested inside tool object)
	if tool != nil && tool.Input != nil {
		if desc, ok := tool.Input["description"].(string); ok && desc != "" {
			return desc
		}
	}
	// Try top-level tool_input
	if topInput != nil {
		if desc, ok := topInput["description"].(string); ok && desc != "" {
			return desc
		}
	}
	return ""
}

func parseClaudeEvent(line []byte, acc *textAccumulator) *StreamEvent {
	var evt claudeEvent
	if err := json.Unmarshal(line, &evt); err != nil {
		return nil
	}

	switch evt.Type {
	case "stream_event":
		if evt.Event == nil {
			return nil
		}
		switch evt.Event.Type {
		case "content_block_delta":
			if evt.Event.Delta != nil && evt.Event.Delta.Type == "text_delta" && evt.Event.Delta.Text != "" {
				fullText := acc.append(evt.Event.Delta.Text)
				return &StreamEvent{Type: "text", Text: fullText}
			}
		case "content_block_start":
			if evt.Event.ContentBlock != nil && evt.Event.ContentBlock.Type == "tool_use" {
				return &StreamEvent{Type: "tool_use", ToolName: evt.Event.ContentBlock.Name, ToolState: "running"}
			}
		}

	case "assistant":
		if evt.Message != nil {
			var text string
			for _, block := range evt.Message.Content {
				if block.Type == "text" {
					text += block.Text
				}
			}
			if text != "" {
				acc.reset()
				acc.append(text)
				return &StreamEvent{Type: "text", Text: text}
			}
		}

	case "tool_use":
		name := ""
		if evt.Tool != nil {
			name = evt.Tool.Name
		}
		if name != "" {
			detail := extractToolDetail(name, evt.Tool, evt.ToolInput)
			return &StreamEvent{Type: "tool_use", ToolName: name, ToolState: "running", ToolDetail: detail}
		}

	case "tool_result":
		name := ""
		if evt.Tool != nil {
			name = evt.Tool.Name
		}
		if name != "" {
			return &StreamEvent{Type: "tool_use", ToolName: name, ToolState: "completed"}
		}

	case "result":
		if evt.Subtype == "error" || evt.IsError {
			errMsg := evt.Error
			if errMsg == "" {
				errMsg = "prompt failed"
			}
			return &StreamEvent{Type: "error", Error: errMsg}
		}
		return &StreamEvent{Type: "done", Done: true}
	}

	return nil
}
