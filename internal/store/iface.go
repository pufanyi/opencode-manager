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

type UserState struct {
	UserID           int64
	ActiveInstanceID string
	ActiveSessionID  string
}

// ── Store interface ─────────────────────────────────────────────────────────

// Store is the persistence interface for all application data.
type Store interface {
	// ── Instances ──

	CreateInstance(inst *Instance) error
	GetInstance(id string) (*Instance, error)
	GetInstanceByName(name string) (*Instance, error)
	ListInstances() ([]*Instance, error)
	GetRunningInstances() ([]*Instance, error)
	UpdateInstanceStatus(id, status string) error
	UpdateInstancePort(id string, port int) error
	DeleteInstance(id string) error

	// ── Sessions ──

	CreateClaudeSession(instanceID, sessionID, title, worktreePath, branch string) error
	GetClaudeSession(sessionID string) (*ClaudeSession, error)
	ListClaudeSessions(instanceID string) ([]ClaudeSession, error)
	UpdateClaudeSessionTitle(sessionID, title string) error
	UpdateClaudeSessionActivity(sessionID string) error
	DeleteClaudeSession(sessionID string) error

	// ── User State (Telegram) ──

	GetUserState(userID int64) (*UserState, error)
	SetActiveInstance(userID int64, instanceID string) error
	SetActiveSession(userID int64, sessionID string) error
	ClearUserState(userID int64, instanceID string) error

	// ── Message Sessions (Telegram message → session mapping) ──

	SetMessageSession(chatID int64, messageID int, sessionID string) error
	GetSessionByMessage(chatID int64, messageID int) (string, error)

	// ── Settings ──

	GetSetting(key string) (string, bool, error)
	SetSetting(key, value string) error
	DeleteSetting(key string) error
	GetAllSettings() (map[string]string, error)
	HasSettings() (bool, error)
	SetSettings(settings map[string]string) error

	// ── Message History ──

	SaveMessage(sessionID string, msg *Message) error
	ListMessages(sessionID string) ([]*Message, error)

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
