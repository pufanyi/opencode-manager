package store

import "time"

// ── Domain types ────────────────────────────────────────────────────────────

type Instance struct {
	ID           string
	Name         string
	Directory    string
	Port         int
	Password     string
	Status       string
	AutoStart    bool
	ProviderType string
	ClientID     string // ID of the Go client that owns this instance
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

type ClaudeSession struct {
	ID           string
	InstanceID   string
	Title        string
	CreatedAt    time.Time
	UpdatedAt    time.Time
	MessageCount int
	WorktreePath string
	Branch       string
}

// ClientInfo represents a registered Go client process.
type ClientInfo struct {
	ClientID  string
	Hostname  string
	StartedAt time.Time
}

// ── Store interface ─────────────────────────────────────────────────────────

// Store is the persistence interface for all application data.
// All paths are user-scoped (under users/{uid}/ in Firestore).
type Store interface {
	// ── Instances ──

	CreateInstance(inst *Instance) error
	GetInstance(id string) (*Instance, error)
	GetInstanceByName(name string) (*Instance, error)
	ListInstances() ([]*Instance, error)
	GetRunningInstances() ([]*Instance, error)
	GetInstancesByClient(clientID string) ([]*Instance, error)
	UpdateInstanceStatus(id, status string) error
	UpdateInstancePort(id string, port int) error
	DeleteInstance(id string) error

	// ── Sessions (nested under instances) ──

	CreateClaudeSession(instanceID, sessionID, title, worktreePath, branch string) error
	GetClaudeSession(instanceID, sessionID string) (*ClaudeSession, error)
	ListClaudeSessions(instanceID string) ([]ClaudeSession, error)
	UpdateClaudeSessionTitle(instanceID, sessionID, title string) error
	UpdateClaudeSessionActivity(instanceID, sessionID string) error
	DeleteClaudeSession(instanceID, sessionID string) error

	// ── Client Registration ──

	RegisterClient(info *ClientInfo) error

	// ── User Config (Firestore) ──

	GetUserConfig() (map[string]string, error)
	SetUserConfig(config map[string]string) error

	// ── Client Config (Firestore) ──

	GetClientConfig(clientID string) (map[string]string, error)
	SetClientConfig(clientID string, config map[string]string) error

	// ── Message History (nested under instances/sessions) ──

	SaveMessage(instanceID, sessionID string, msg *Message) error
	ListMessages(instanceID, sessionID string) ([]*Message, error)

	// ── Lifecycle ──

	Close() error
}

// Message represents a single message in a session's conversation history.
type Message struct {
	ID        string
	Role      string // "user" or "assistant"
	Content   string
	ToolCalls []ToolCall
	CreatedAt time.Time
}

// ToolCall represents a tool invocation with full details.
type ToolCall struct {
	Name   string `json:"name"`
	Status string `json:"status"` // "running", "completed", "error"
	Detail string `json:"detail"` // Short description (e.g. filename)
	Input  string `json:"input"`  // Tool input (JSON or text)
	Output string `json:"output"` // Tool output/result
}
