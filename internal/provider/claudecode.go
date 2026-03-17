package provider

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/pufanyi/opencode-manager/internal/store"
)

// ClaudeCodeProvider manages Claude Code CLI invocations.
// Each prompt spawns a new `claude -p` process with stream-json output.
type ClaudeCodeProvider struct {
	binary string
	dir    string
	store  *store.Store
	instID string // instance ID for session tracking

	mu        sync.Mutex
	activeCmd *exec.Cmd
	activeCancel context.CancelFunc
	activeSession string
}

func NewClaudeCodeProvider(binary, dir string, st *store.Store, instanceID string) *ClaudeCodeProvider {
	return &ClaudeCodeProvider{
		binary: binary,
		dir:    dir,
		store:  st,
		instID: instanceID,
	}
}

func (p *ClaudeCodeProvider) Type() Type { return TypeClaudeCode }

func (p *ClaudeCodeProvider) Start(ctx context.Context) error {
	// Validate binary exists
	if _, err := exec.LookPath(p.binary); err != nil {
		return fmt.Errorf("claude binary not found: %w", err)
	}
	slog.Info("claude code provider ready", "dir", p.dir)
	return nil
}

func (p *ClaudeCodeProvider) Stop() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.activeCancel != nil {
		p.activeCancel()
		p.activeCancel = nil
	}
	return nil
}

func (p *ClaudeCodeProvider) WaitReady(ctx context.Context, timeout time.Duration) error {
	// Claude Code has no persistent server — just validate binary exists.
	if _, err := exec.LookPath(p.binary); err != nil {
		return fmt.Errorf("claude binary not found: %w", err)
	}
	return nil
}

func (p *ClaudeCodeProvider) Wait() error {
	// No persistent process to wait on.
	return nil
}

func (p *ClaudeCodeProvider) Stderr() string {
	// No persistent process stderr.
	return ""
}

func (p *ClaudeCodeProvider) SetPort(port int) {
	// No-op: Claude Code doesn't use a port.
}

func (p *ClaudeCodeProvider) IsReady() bool {
	return true // Always ready if binary exists
}

func (p *ClaudeCodeProvider) HealthCheck(ctx context.Context) error {
	cmd := exec.CommandContext(ctx, p.binary, "--version")
	return cmd.Run()
}

func (p *ClaudeCodeProvider) CreateSession(ctx context.Context) (*Session, error) {
	id := uuid.New().String()
	if err := p.store.CreateClaudeSession(p.instID, id, ""); err != nil {
		return nil, err
	}
	return &Session{ID: id}, nil
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
	// Kill any existing prompt
	if p.activeCancel != nil {
		p.activeCancel()
	}

	cmdCtx, cancel := context.WithCancel(ctx)
	p.activeCancel = cancel
	p.activeSession = sessionID
	p.mu.Unlock()

	cmd := exec.CommandContext(cmdCtx, p.binary,
		"-p", content,
		"--output-format", "stream-json",
		"--verbose",
		"--include-partial-messages",
		"--session-id", sessionID,
		"--permission-mode", "bypassPermissions",
	)
	cmd.Dir = p.dir

	var stderrBuf bytes.Buffer
	cmd.Stderr = &stderrBuf

	p.mu.Lock()
	p.activeCmd = cmd
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

	slog.Info("claude prompt started", "session", sessionID, "pid", cmd.Process.Pid)

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
		err := cmd.Wait()

		p.mu.Lock()
		p.activeCmd = nil
		p.activeCancel = nil
		p.mu.Unlock()

		if err != nil && cmdCtx.Err() == nil {
			errMsg := err.Error()
			if stderr := stderrBuf.String(); stderr != "" {
				errMsg = stderr
				slog.Error("claude process failed", "error", err, "stderr", stderr)
			}
			select {
			case ch <- StreamEvent{Type: "error", Error: errMsg}:
			default:
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

	if p.activeCancel != nil && p.activeSession == sessionID {
		p.activeCancel()
		p.activeCancel = nil
		if p.activeCmd != nil && p.activeCmd.Process != nil {
			_ = p.activeCmd.Process.Kill()
		}
	}
	return nil
}

// Claude Code stream-json event types (with --include-partial-messages).
// One JSON object per line:
//
// {"type":"system","subtype":"init",...}
// {"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"..."}}}
// {"type":"assistant","message":{"content":[{"type":"text","text":"full text"}]},...}
// {"type":"tool_use","tool":{"name":"..."},...}
// {"type":"tool_result","tool":{"name":"..."},...}
// {"type":"result","subtype":"success","result":"full text",...}

type claudeEvent struct {
	Type    string `json:"type"`
	Subtype string `json:"subtype,omitempty"`

	// For "stream_event"
	Event *claudeStreamEvent `json:"event,omitempty"`

	// For "assistant" events (full message snapshot)
	Message *claudeMessage `json:"message,omitempty"`

	// For "result" events
	Result  string `json:"result,omitempty"`
	IsError bool   `json:"is_error,omitempty"`
	Error   string `json:"error,omitempty"`

	// For "tool_use" / "tool_result" events
	Tool *claudeTool `json:"tool,omitempty"`
}

type claudeStreamEvent struct {
	Type         string            `json:"type"`
	Delta        *claudeDelta      `json:"delta,omitempty"`
	ContentBlock *claudeBlock      `json:"content_block,omitempty"`
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
	Name string `json:"name,omitempty"`
}

// textAccumulator tracks the full text for streaming display.
// Claude sends deltas; we accumulate and emit the full text each time
// so the streaming bridge can display progressively.
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
		// Full message snapshot — use as final text, reset accumulator
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
			return &StreamEvent{Type: "tool_use", ToolName: name, ToolState: "running"}
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
