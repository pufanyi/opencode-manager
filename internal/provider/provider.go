package provider

import "context"

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

// StreamEvent is a normalized streaming event sent to the bot layer.
type StreamEvent struct {
	// Type: "text", "tool_use", "done", "error"
	Type      string
	Text      string // Accumulated text so far (for "text" events)
	ToolName  string // For tool_use
	ToolState string // "running", "completed", "error"
	Done      bool   // True when response is fully complete
	Error     string // Non-empty on error events
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

	// --- Sessions ---
	CreateSession(ctx context.Context) (*Session, error)
	GetSession(ctx context.Context, sessionID string) (*Session, error)
	ListSessions(ctx context.Context) ([]Session, error)

	// --- Prompting ---
	// Prompt sends a prompt and returns a channel of StreamEvents.
	// The channel is closed when the response is complete.
	Prompt(ctx context.Context, sessionID string, content string) (<-chan StreamEvent, error)

	// Abort cancels a running prompt for the given session.
	Abort(ctx context.Context, sessionID string) error
}
