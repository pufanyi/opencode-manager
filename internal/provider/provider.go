package provider

import (
	"context"
	"time"
)

// Type identifies the backend.
type Type string

const (
	TypeOpenCode   Type = "opencode"
	TypeClaudeCode Type = "claudecode"
)

// Session is the provider-agnostic session representation.
type Session struct {
	ID    string
	Title string
}

// CreateSessionOpts controls how a new session is created.
type CreateSessionOpts struct {
	UseWorktree bool // Create an isolated git worktree for this session
}

// StreamEvent is a normalized streaming event sent to the bot layer.
type StreamEvent struct {
	// Type: "text", "tool_use", "done", "error", "merge_failed"
	Type        string
	Text        string // Accumulated text so far (for "text" events)
	ToolName    string // For tool_use
	ToolState   string // "running", "completed", "error"
	ToolDetail  string // Short description (e.g., Agent description)
	Done        bool   // True when response is fully complete
	Error       string // Non-empty on error events
	MergeBranch string // Branch name (for merge_failed events)
}

// Provider is the abstraction that bot handlers talk to.
type Provider interface {
	// Type returns the provider backend type.
	Type() Type

	// --- Lifecycle ---
	Start(ctx context.Context) error
	Stop() error
	IsReady() bool
	HealthCheck(ctx context.Context) error

	// WaitReady blocks until the provider is ready to accept requests.
	// OpenCode polls HTTP; Claude Code validates binary and returns immediately.
	WaitReady(ctx context.Context, timeout time.Duration) error

	// Wait blocks until the underlying process exits.
	// OpenCode blocks on cmd.Wait(); Claude Code returns nil (no persistent process).
	Wait() error

	// Stderr returns the last error output for crash diagnostics.
	Stderr() string

	// SetPort sets the port for providers that use one (OpenCode).
	// No-op for providers without a persistent server (Claude Code).
	SetPort(port int)

	// --- Sessions ---
	CreateSession(ctx context.Context, opts *CreateSessionOpts) (*Session, error)
	GetSession(ctx context.Context, sessionID string) (*Session, error)
	ListSessions(ctx context.Context) ([]Session, error)

	// SupportsWorktree returns true if this provider can create isolated git worktrees.
	SupportsWorktree() bool

	// --- Prompting ---
	// Prompt sends a prompt and returns a channel of StreamEvents.
	// The channel is closed when the response is complete.
	Prompt(ctx context.Context, sessionID string, content string) (<-chan StreamEvent, error)

	// Abort cancels a running prompt for the given session.
	Abort(ctx context.Context, sessionID string) error
}
