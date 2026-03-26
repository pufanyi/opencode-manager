package provider

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
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

// DefaultMaxWorktrees is the maximum number of worktrees maintained per instance.
// When exceeded, the oldest non-active worktree sessions are evicted (FIFO).
const DefaultMaxWorktrees = 5

// ClaudeCodeProvider manages Claude Code CLI invocations.
// Supports concurrent prompts across different sessions via git worktrees.
type ClaudeCodeProvider struct {
	binary       string
	dir          string
	store        store.Store
	instID       string // instance ID for session tracking
	maxWorktrees int    // max worktrees per instance (FIFO eviction)

	mu           sync.Mutex
	activeCmds   map[string]*sessionCmd // sessionID → active command
	usedSessions map[string]bool        // sessions that have been prompted at least once

	// mergeMu serializes merge+sync operations to avoid conflicts.
	mergeMu sync.Mutex

	// Main-dir exclusive lock: at most one session may use the main
	// directory at a time.
	mainDirMu     sync.Mutex
	mainDirHolder string         // session ID currently holding the lock
	mainDirNotify []chan struct{} // closed when main dir becomes free
}

func NewClaudeCodeProvider(binary, dir string, st store.Store, instanceID string) *ClaudeCodeProvider {
	return &ClaudeCodeProvider{
		binary:       binary,
		dir:          dir,
		store:        st,
		instID:       instanceID,
		maxWorktrees: DefaultMaxWorktrees,
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

func (p *ClaudeCodeProvider) CreateSession(ctx context.Context, opts *CreateSessionOpts) (*Session, error) {
	id := uuid.New().String()

	var wtPath, branch string
	if opts != nil && opts.UseWorktree && p.isGitRepo() {
		// Evict oldest worktrees if at limit (FIFO)
		p.enforceWorktreeLimit()

		var err error
		wtPath, branch, err = p.createWorktree(id)
		if err != nil {
			return nil, fmt.Errorf("worktree creation failed: %w", err)
		}
	}

	if err := p.store.CreateClaudeSession(p.instID, id, "", wtPath, branch); err != nil {
		// Clean up worktree if session DB insert fails
		if wtPath != "" {
			_ = p.removeWorktree(wtPath)
		}
		return nil, err
	}
	return &Session{ID: id, Title: ""}, nil
}

func (p *ClaudeCodeProvider) GetSession(ctx context.Context, sessionID string) (*Session, error) {
	s, err := p.store.GetClaudeSession(p.instID, sessionID)
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
	cs, err := p.store.GetClaudeSession(p.instID, sessionID)
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

		var parser claudeParser

		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 0, 1024*1024), 1024*1024)

		for scanner.Scan() {
			line := scanner.Bytes()
			if len(line) == 0 {
				continue
			}

			evt := parser.parseEvent(line)
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
				case ch <- StreamEvent{Type: "merge_failed", Error: mergeErr.Error(), MergeBranch: cs.Branch}:
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
	cs, err := p.store.GetClaudeSession(p.instID, sessionID)
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
		_ = p.removeWorktree(cs.WorktreePath)
	}

	// Delete the branch
	if cs.Branch != "" {
		_ = exec.Command("git", "-C", p.dir, "branch", "-D", cs.Branch).Run()
	}

	return p.store.DeleteClaudeSession(p.instID, sessionID)
}
