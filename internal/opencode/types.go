package opencode

import "time"

// Session represents an OpenCode session.
type Session struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// SessionListResponse is the response from GET /session.
type SessionListResponse struct {
	Sessions []Session `json:"sessions"`
}

// SessionCreateRequest creates a new session.
type SessionCreateRequest struct {
	Title string `json:"title,omitempty"`
}

// Message represents an OpenCode message.
type Message struct {
	ID        string         `json:"id"`
	SessionID string         `json:"sessionId"`
	Role      string         `json:"role"`
	Parts     []MessagePart  `json:"parts"`
	CreatedAt time.Time      `json:"createdAt"`
	System    bool           `json:"system"`
	Time      *MessageTiming `json:"time,omitempty"`
}

type MessageTiming struct {
	Created  int64 `json:"created"`
	Finished int64 `json:"finished"`
}

// MessagePart is a part of a message (text, tool call, etc.).
type MessagePart struct {
	Type      string `json:"type"`
	Text      string `json:"text,omitempty"`
	ToolName  string `json:"toolName,omitempty"`
	State     string `json:"state,omitempty"`
	ToolInput any    `json:"toolInput,omitempty"`
	Output    string `json:"output,omitempty"`
}

// MessageListResponse is the response from GET /session/:id/messages.
type MessageListResponse struct {
	Messages []Message `json:"messages"`
}

// PromptRequest sends a prompt to OpenCode.
type PromptRequest struct {
	SessionID string `json:"sessionId"`
	Content   string `json:"content"`
}

// PromptResponse is the response from POST /session/:id/prompt.
type PromptResponse struct {
	ID string `json:"id"`
}

// SSEEvent represents a server-sent event from OpenCode.
type SSEEvent struct {
	Event string
	Data  string
	ID    string
}

// Event types from OpenCode SSE stream.
const (
	EventMessageUpdated  = "message.updated"
	EventMessageCreated  = "message.created"
	EventMessageDeleted  = "message.deleted"
	EventSessionUpdated  = "session.updated"
	EventSessionDeleted  = "session.deleted"
)

// EventPayload wraps SSE event data.
type EventPayload struct {
	Type    string  `json:"type"`
	Session *Session `json:"session,omitempty"`
	Message *Message `json:"message,omitempty"`
}

// StatusResponse from GET /.
type StatusResponse struct {
	Version string `json:"version"`
}
