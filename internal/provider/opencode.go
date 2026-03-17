package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os/exec"
	"sync"
	"time"

	"github.com/pufanyi/opencode-manager/internal/opencode"
)

// OpenCodeProvider manages an opencode serve process and communicates via HTTP+SSE.
type OpenCodeProvider struct {
	binary   string
	dir      string
	port     int
	password string

	mu     sync.Mutex
	cmd    *exec.Cmd
	cancel context.CancelFunc
	ready  bool
	stderr bytes.Buffer

	client     *opencode.Client
	subscriber *opencode.SSESubscriber
	sseCtx     context.Context
	sseCancel  context.CancelFunc

	// Active prompt channels keyed by sessionID
	streams   map[string]chan StreamEvent
	streamsMu sync.Mutex
}

func NewOpenCodeProvider(binary, dir string, port int, password string) *OpenCodeProvider {
	return &OpenCodeProvider{
		binary:   binary,
		dir:      dir,
		port:     port,
		password: password,
		streams:  make(map[string]chan StreamEvent),
	}
}

func (p *OpenCodeProvider) Type() Type { return TypeOpenCode }

func (p *OpenCodeProvider) SetPort(port int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.port = port
}

func (p *OpenCodeProvider) Port() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.port
}

func (p *OpenCodeProvider) Start(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	cmdCtx, cancel := context.WithCancel(ctx)
	p.cancel = cancel
	p.stderr.Reset()

	cmd := exec.CommandContext(cmdCtx, p.binary, "serve",
		"--port", fmt.Sprintf("%d", p.port),
		"--hostname", "127.0.0.1",
	)
	cmd.Dir = p.dir
	cmd.Env = append(cmd.Environ(),
		fmt.Sprintf("OPENCODE_SERVER_PASSWORD=%s", p.password),
	)
	cmd.Stderr = &p.stderr

	if err := cmd.Start(); err != nil {
		cancel()
		return fmt.Errorf("starting opencode serve: %w", err)
	}

	p.cmd = cmd
	p.client = opencode.NewClient(fmt.Sprintf("http://127.0.0.1:%d", p.port), p.password)
	p.ready = false

	slog.Info("opencode process started", "dir", p.dir, "port", p.port, "pid", cmd.Process.Pid)
	return nil
}

func (p *OpenCodeProvider) WaitReady(ctx context.Context, timeout time.Duration) error {
	p.mu.Lock()
	client := p.client
	p.mu.Unlock()

	if client == nil {
		return fmt.Errorf("provider not started")
	}

	sub := opencode.NewSSESubscriber(client)
	if err := sub.WaitReady(ctx, timeout); err != nil {
		return err
	}

	p.mu.Lock()
	p.ready = true
	p.mu.Unlock()

	// Start SSE listener
	p.startSSE()
	return nil
}

func (p *OpenCodeProvider) startSSE() {
	p.mu.Lock()
	client := p.client
	p.mu.Unlock()

	subscriber := opencode.NewSSESubscriber(client)

	sseCtx, sseCancel := context.WithCancel(context.Background())
	p.mu.Lock()
	p.subscriber = subscriber
	p.sseCtx = sseCtx
	p.sseCancel = sseCancel
	p.mu.Unlock()

	subscriber.On("*", func(eventType string, data json.RawMessage) {
		if eventType != opencode.EventMessageUpdated && eventType != opencode.EventMessageCreated {
			return
		}

		var msg opencode.Message
		if err := json.Unmarshal(data, &msg); err != nil {
			return
		}

		if msg.Role != "assistant" {
			return
		}

		p.streamsMu.Lock()
		ch, ok := p.streams[msg.SessionID]
		p.streamsMu.Unlock()

		if !ok {
			return
		}

		// Build stream events from message parts
		for _, part := range msg.Parts {
			switch part.Type {
			case "text":
				select {
				case ch <- StreamEvent{Type: "text", Text: part.Text}:
				default:
				}
			case "tool-invocation":
				if part.ToolName != "" {
					select {
					case ch <- StreamEvent{Type: "tool_use", ToolName: part.ToolName, ToolState: part.State}:
					default:
					}
				}
			}
		}

		// Check if done
		if msg.Time != nil && msg.Time.Finished > 0 {
			select {
			case ch <- StreamEvent{Type: "done", Done: true}:
			default:
			}
		}
	})

	go func() {
		if err := subscriber.Subscribe(sseCtx); err != nil && sseCtx.Err() == nil {
			slog.Error("SSE subscriber failed", "dir", p.dir, "error", err)
		}
	}()
}

func (p *OpenCodeProvider) Stop() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.sseCancel != nil {
		p.sseCancel()
	}

	if p.cancel != nil {
		p.cancel()
	}

	if p.cmd != nil && p.cmd.Process != nil {
		_ = p.cmd.Process.Kill()
		_ = p.cmd.Wait()
	}

	p.ready = false
	p.cmd = nil
	slog.Info("opencode process stopped", "dir", p.dir)
	return nil
}

func (p *OpenCodeProvider) Wait() error {
	p.mu.Lock()
	cmd := p.cmd
	p.mu.Unlock()
	if cmd == nil {
		return nil
	}
	return cmd.Wait()
}

func (p *OpenCodeProvider) Stderr() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.stderr.String()
}

func (p *OpenCodeProvider) IsReady() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.ready
}

func (p *OpenCodeProvider) HealthCheck(ctx context.Context) error {
	p.mu.Lock()
	client := p.client
	p.mu.Unlock()
	if client == nil {
		return fmt.Errorf("not started")
	}
	return client.Status()
}

func (p *OpenCodeProvider) SupportsWorktree() bool { return false }

func (p *OpenCodeProvider) CreateSession(ctx context.Context, opts *CreateSessionOpts) (*Session, error) {
	p.mu.Lock()
	client := p.client
	p.mu.Unlock()

	s, err := client.CreateSession()
	if err != nil {
		return nil, err
	}
	return &Session{ID: s.ID, Title: s.Title}, nil
}

func (p *OpenCodeProvider) GetSession(ctx context.Context, sessionID string) (*Session, error) {
	p.mu.Lock()
	client := p.client
	p.mu.Unlock()

	s, err := client.GetSession(sessionID)
	if err != nil {
		return nil, err
	}
	return &Session{ID: s.ID, Title: s.Title}, nil
}

func (p *OpenCodeProvider) ListSessions(ctx context.Context) ([]Session, error) {
	p.mu.Lock()
	client := p.client
	p.mu.Unlock()

	sessions, err := client.ListSessions()
	if err != nil {
		return nil, err
	}

	result := make([]Session, len(sessions))
	for i, s := range sessions {
		result[i] = Session{ID: s.ID, Title: s.Title}
	}
	return result, nil
}

func (p *OpenCodeProvider) Prompt(ctx context.Context, sessionID string, content string) (<-chan StreamEvent, error) {
	p.mu.Lock()
	client := p.client
	p.mu.Unlock()

	ch := make(chan StreamEvent, 64)

	p.streamsMu.Lock()
	// Close old stream for this session if any
	if old, ok := p.streams[sessionID]; ok {
		close(old)
	}
	p.streams[sessionID] = ch
	p.streamsMu.Unlock()

	if err := client.PromptAsync(sessionID, content); err != nil {
		p.streamsMu.Lock()
		delete(p.streams, sessionID)
		p.streamsMu.Unlock()
		close(ch)
		return nil, err
	}

	// Clean up stream when context is cancelled
	go func() {
		<-ctx.Done()
		p.streamsMu.Lock()
		if current, ok := p.streams[sessionID]; ok && current == ch {
			delete(p.streams, sessionID)
		}
		p.streamsMu.Unlock()
	}()

	return ch, nil
}

func (p *OpenCodeProvider) Abort(ctx context.Context, sessionID string) error {
	p.mu.Lock()
	client := p.client
	p.mu.Unlock()

	// Remove stream
	p.streamsMu.Lock()
	if ch, ok := p.streams[sessionID]; ok {
		close(ch)
		delete(p.streams, sessionID)
	}
	p.streamsMu.Unlock()

	return client.Abort(sessionID)
}
