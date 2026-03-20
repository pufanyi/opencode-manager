package store

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// FirestoreClient is the interface for Firestore document operations.
// Implemented by firebase.Firestore — defined here to avoid import cycles.
type FirestoreClient interface {
	GetDoc(ctx context.Context, path string) (*FirestoreDoc, error)
	SetDoc(ctx context.Context, path string, fields map[string]interface{}) error
	UpdateDoc(ctx context.Context, path string, fields map[string]interface{}) error
	DeleteDoc(ctx context.Context, path string) error
	ListDocs(ctx context.Context, collectionPath string) ([]*FirestoreDoc, error)
}

// FirestoreDoc mirrors firebase.Document to avoid import cycles.
type FirestoreDoc struct {
	Name       string
	Fields     map[string]interface{}
	CreateTime string
	UpdateTime string
}

// FirestoreStore is the cloud-backed implementation of Store using Firestore REST API.
// RTDB remains for fast ephemeral communication; Firestore holds all persistent data.
type FirestoreStore struct {
	fs  FirestoreClient
	ctx context.Context
}

// NewFirestoreStore creates a Firestore-backed store.
func NewFirestoreStore(ctx context.Context, fs FirestoreClient) *FirestoreStore {
	return &FirestoreStore{fs: fs, ctx: ctx}
}

func (s *FirestoreStore) Close() error { return nil }

// ── Instances ───────────────────────────────────────────────────────────────

func (s *FirestoreStore) CreateInstance(inst *Instance) error {
	if inst.ProviderType == "" {
		inst.ProviderType = "claudecode"
	}
	now := time.Now()
	fields := map[string]interface{}{
		"id":            inst.ID,
		"name":          inst.Name,
		"directory":     inst.Directory,
		"port":          inst.Port,
		"password":      inst.Password,
		"status":        inst.Status,
		"auto_start":    inst.AutoStart,
		"provider_type": inst.ProviderType,
		"created_at":    now,
		"updated_at":    now,
	}
	return s.fs.SetDoc(s.ctx, "instances/"+inst.ID, fields)
}

func (s *FirestoreStore) GetInstance(id string) (*Instance, error) {
	doc, err := s.fs.GetDoc(s.ctx, "instances/"+id)
	if err != nil {
		return nil, fmt.Errorf("getting instance %s: %w", id, err)
	}
	if doc == nil {
		return nil, nil
	}
	return docToInstance(doc), nil
}

func (s *FirestoreStore) GetInstanceByName(name string) (*Instance, error) {
	// Firestore REST doesn't have simple field queries on the list endpoint.
	// Since instance count is small, list all and filter.
	instances, err := s.ListInstances()
	if err != nil {
		return nil, err
	}
	for _, inst := range instances {
		if inst.Name == name {
			return inst, nil
		}
	}
	return nil, nil
}

func (s *FirestoreStore) ListInstances() ([]*Instance, error) {
	docs, err := s.fs.ListDocs(s.ctx, "instances")
	if err != nil {
		return nil, fmt.Errorf("listing instances: %w", err)
	}
	var instances []*Instance
	for _, doc := range docs {
		instances = append(instances, docToInstance(doc))
	}
	return instances, nil
}

func (s *FirestoreStore) GetRunningInstances() ([]*Instance, error) {
	all, err := s.ListInstances()
	if err != nil {
		return nil, err
	}
	var result []*Instance
	for _, inst := range all {
		if inst.Status == "running" || inst.AutoStart {
			result = append(result, inst)
		}
	}
	return result, nil
}

func (s *FirestoreStore) UpdateInstanceStatus(id, status string) error {
	return s.fs.UpdateDoc(s.ctx, "instances/"+id, map[string]interface{}{
		"status":     status,
		"updated_at": time.Now(),
	})
}

func (s *FirestoreStore) UpdateInstancePort(id string, port int) error {
	return s.fs.UpdateDoc(s.ctx, "instances/"+id, map[string]interface{}{
		"port":       port,
		"updated_at": time.Now(),
	})
}

func (s *FirestoreStore) DeleteInstance(id string) error {
	// Delete associated sessions first.
	sessions, _ := s.ListClaudeSessions(id)
	for _, sess := range sessions {
		_ = s.DeleteClaudeSession(sess.ID)
	}
	return s.fs.DeleteDoc(s.ctx, "instances/"+id)
}

// ── Sessions ────────────────────────────────────────────────────────────────

func (s *FirestoreStore) CreateClaudeSession(instanceID, sessionID, title, worktreePath, branch string) error {
	now := time.Now()
	fields := map[string]interface{}{
		"id":            sessionID,
		"instance_id":   instanceID,
		"title":         title,
		"worktree_path": worktreePath,
		"branch":        branch,
		"message_count": 0,
		"created_at":    now,
		"updated_at":    now,
	}
	return s.fs.SetDoc(s.ctx, "sessions/"+sessionID, fields)
}

func (s *FirestoreStore) GetClaudeSession(sessionID string) (*ClaudeSession, error) {
	doc, err := s.fs.GetDoc(s.ctx, "sessions/"+sessionID)
	if err != nil {
		return nil, fmt.Errorf("getting session %s: %w", sessionID, err)
	}
	if doc == nil {
		return nil, nil
	}
	return docToSession(doc), nil
}

func (s *FirestoreStore) ListClaudeSessions(instanceID string) ([]ClaudeSession, error) {
	docs, err := s.fs.ListDocs(s.ctx, "sessions")
	if err != nil {
		return nil, fmt.Errorf("listing sessions: %w", err)
	}
	var sessions []ClaudeSession
	for _, doc := range docs {
		cs := docToSession(doc)
		if cs.InstanceID == instanceID {
			sessions = append(sessions, *cs)
		}
	}
	return sessions, nil
}

func (s *FirestoreStore) UpdateClaudeSessionTitle(sessionID, title string) error {
	return s.fs.UpdateDoc(s.ctx, "sessions/"+sessionID, map[string]interface{}{
		"title":      title,
		"updated_at": time.Now(),
	})
}

func (s *FirestoreStore) UpdateClaudeSessionActivity(sessionID string) error {
	// Read current count, increment, write back.
	// Acceptable for our low-concurrency use case.
	doc, err := s.fs.GetDoc(s.ctx, "sessions/"+sessionID)
	if err != nil || doc == nil {
		return err
	}
	count := getInt(doc.Fields, "message_count")
	return s.fs.UpdateDoc(s.ctx, "sessions/"+sessionID, map[string]interface{}{
		"message_count": count + 1,
		"updated_at":    time.Now(),
	})
}

func (s *FirestoreStore) DeleteClaudeSession(sessionID string) error {
	// Delete messages subcollection first.
	msgs, _ := s.fs.ListDocs(s.ctx, "sessions/"+sessionID+"/messages")
	for _, msg := range msgs {
		_ = s.fs.DeleteDoc(s.ctx, "sessions/"+sessionID+"/messages/"+DocIDFromName(msg.Name))
	}
	return s.fs.DeleteDoc(s.ctx, "sessions/"+sessionID)
}

// ── User State ──────────────────────────────────────────────────────────────

func (s *FirestoreStore) GetUserState(userID int64) (*UserState, error) {
	key := strconv.FormatInt(userID, 10)
	doc, err := s.fs.GetDoc(s.ctx, "user_state/"+key)
	if err != nil {
		return nil, fmt.Errorf("getting user state: %w", err)
	}
	if doc == nil {
		return &UserState{UserID: userID}, nil
	}
	return &UserState{
		UserID:           userID,
		ActiveInstanceID: getString(doc.Fields, "active_instance_id"),
		ActiveSessionID:  getString(doc.Fields, "active_session_id"),
	}, nil
}

func (s *FirestoreStore) SetActiveInstance(userID int64, instanceID string) error {
	key := strconv.FormatInt(userID, 10)
	return s.fs.SetDoc(s.ctx, "user_state/"+key, map[string]interface{}{
		"user_id":            userID,
		"active_instance_id": instanceID,
		"active_session_id":  "",
		"updated_at":         time.Now(),
	})
}

func (s *FirestoreStore) SetActiveSession(userID int64, sessionID string) error {
	key := strconv.FormatInt(userID, 10)
	return s.fs.UpdateDoc(s.ctx, "user_state/"+key, map[string]interface{}{
		"active_session_id": sessionID,
		"updated_at":        time.Now(),
	})
}

func (s *FirestoreStore) ClearUserState(userID int64, instanceID string) error {
	state, err := s.GetUserState(userID)
	if err != nil {
		return err
	}
	if state.ActiveInstanceID != instanceID {
		return nil // Not pointing to this instance, nothing to clear.
	}
	key := strconv.FormatInt(userID, 10)
	return s.fs.UpdateDoc(s.ctx, "user_state/"+key, map[string]interface{}{
		"active_instance_id": "",
		"active_session_id":  "",
		"updated_at":         time.Now(),
	})
}

// ── Message Sessions ────────────────────────────────────────────────────────

func (s *FirestoreStore) SetMessageSession(chatID int64, messageID int, sessionID string) error {
	key := fmt.Sprintf("%d_%d", chatID, messageID)
	return s.fs.SetDoc(s.ctx, "message_sessions/"+key, map[string]interface{}{
		"chat_id":    chatID,
		"message_id": messageID,
		"session_id": sessionID,
	})
}

func (s *FirestoreStore) GetSessionByMessage(chatID int64, messageID int) (string, error) {
	key := fmt.Sprintf("%d_%d", chatID, messageID)
	doc, err := s.fs.GetDoc(s.ctx, "message_sessions/"+key)
	if err != nil {
		return "", err
	}
	if doc == nil {
		return "", nil
	}
	return getString(doc.Fields, "session_id"), nil
}

// ── Settings ────────────────────────────────────────────────────────────────

// Settings are stored in RTDB /config in cloud mode. These methods provide
// a Firestore fallback for completeness but are typically not used.

func (s *FirestoreStore) GetSetting(key string) (string, bool, error) {
	doc, err := s.fs.GetDoc(s.ctx, "settings/config")
	if err != nil {
		return "", false, err
	}
	if doc == nil {
		return "", false, nil
	}
	val, ok := doc.Fields[key]
	if !ok {
		return "", false, nil
	}
	return fmt.Sprint(val), true, nil
}

func (s *FirestoreStore) SetSetting(key, value string) error {
	return s.fs.UpdateDoc(s.ctx, "settings/config", map[string]interface{}{
		key: value,
	})
}

func (s *FirestoreStore) DeleteSetting(key string) error {
	// Firestore doesn't support field deletion via simple update.
	// Set to empty string as equivalent.
	return s.fs.UpdateDoc(s.ctx, "settings/config", map[string]interface{}{
		key: "",
	})
}

func (s *FirestoreStore) GetAllSettings() (map[string]string, error) {
	doc, err := s.fs.GetDoc(s.ctx, "settings/config")
	if err != nil {
		return nil, err
	}
	if doc == nil {
		return nil, nil
	}
	result := make(map[string]string, len(doc.Fields))
	for k, v := range doc.Fields {
		result[k] = fmt.Sprint(v)
	}
	return result, nil
}

func (s *FirestoreStore) HasSettings() (bool, error) {
	doc, err := s.fs.GetDoc(s.ctx, "settings/config")
	if err != nil {
		return false, err
	}
	return doc != nil && len(doc.Fields) > 0, nil
}

func (s *FirestoreStore) SetSettings(settings map[string]string) error {
	fields := make(map[string]interface{}, len(settings))
	for k, v := range settings {
		fields[k] = v
	}
	return s.fs.SetDoc(s.ctx, "settings/config", fields)
}

// ── Message History ─────────────────────────────────────────────────────────

func (s *FirestoreStore) SaveMessage(sessionID string, msg *Message) error {
	if msg.ID == "" {
		msg.ID = fmt.Sprintf("%d", time.Now().UnixNano())
	}
	if msg.CreatedAt.IsZero() {
		msg.CreatedAt = time.Now()
	}

	// Encode tool calls as array of maps.
	toolCalls := make([]interface{}, len(msg.ToolCalls))
	for i, tc := range msg.ToolCalls {
		toolCalls[i] = map[string]interface{}{
			"name":   tc.Name,
			"status": tc.Status,
			"detail": tc.Detail,
			"input":  tc.Input,
			"output": tc.Output,
		}
	}

	fields := map[string]interface{}{
		"id":         msg.ID,
		"role":       msg.Role,
		"content":    msg.Content,
		"tool_calls": toolCalls,
		"created_at": msg.CreatedAt,
	}

	return s.fs.SetDoc(s.ctx, "sessions/"+sessionID+"/messages/"+msg.ID, fields)
}

func (s *FirestoreStore) ListMessages(sessionID string) ([]*Message, error) {
	docs, err := s.fs.ListDocs(s.ctx, "sessions/"+sessionID+"/messages")
	if err != nil {
		return nil, fmt.Errorf("listing messages for session %s: %w", sessionID, err)
	}

	var messages []*Message
	for _, doc := range docs {
		msg := &Message{
			ID:      getString(doc.Fields, "id"),
			Role:    getString(doc.Fields, "role"),
			Content: getString(doc.Fields, "content"),
		}

		if ts := getString(doc.Fields, "created_at"); ts != "" {
			msg.CreatedAt = parseTimestamp(ts)
		}

		// Decode tool calls.
		if raw, ok := doc.Fields["tool_calls"]; ok {
			if arr, ok := raw.([]interface{}); ok {
				for _, item := range arr {
					if m, ok := item.(map[string]interface{}); ok {
						msg.ToolCalls = append(msg.ToolCalls, ToolCall{
							Name:   fmt.Sprint(m["name"]),
							Status: fmt.Sprint(m["status"]),
							Detail: fmt.Sprint(m["detail"]),
							Input:  fmt.Sprint(m["input"]),
							Output: fmt.Sprint(m["output"]),
						})
					}
				}
			}
		}

		messages = append(messages, msg)
	}
	return messages, nil
}

// ── Helpers ─────────────────────────────────────────────────────────────────

func getString(fields map[string]interface{}, key string) string {
	if v, ok := fields[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

func getInt(fields map[string]interface{}, key string) int {
	if v, ok := fields[key]; ok {
		switch n := v.(type) {
		case int64:
			return int(n)
		case float64:
			return int(n)
		case int:
			return n
		}
	}
	return 0
}

func getBool(fields map[string]interface{}, key string) bool {
	if v, ok := fields[key]; ok {
		if b, ok := v.(bool); ok {
			return b
		}
	}
	return false
}

func parseTimestamp(s string) time.Time {
	for _, layout := range []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02T15:04:05Z",
	} {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Time{}
}

func docToInstance(doc *FirestoreDoc) *Instance {
	return &Instance{
		ID:           getString(doc.Fields, "id"),
		Name:         getString(doc.Fields, "name"),
		Directory:    getString(doc.Fields, "directory"),
		Port:         getInt(doc.Fields, "port"),
		Password:     getString(doc.Fields, "password"),
		Status:       getString(doc.Fields, "status"),
		AutoStart:    getBool(doc.Fields, "auto_start"),
		ProviderType: getString(doc.Fields, "provider_type"),
		CreatedAt:    parseTimestamp(getString(doc.Fields, "created_at")),
		UpdatedAt:    parseTimestamp(getString(doc.Fields, "updated_at")),
	}
}

func docToSession(doc *FirestoreDoc) *ClaudeSession {
	// Try both the Firestore updateTime and the stored field.
	updatedAt := parseTimestamp(getString(doc.Fields, "updated_at"))
	if updatedAt.IsZero() {
		updatedAt = parseTimestamp(doc.UpdateTime)
	}

	title := getString(doc.Fields, "title")
	// Handle "0" as empty (Firestore may coerce empty string → "0" on int fields).
	if title == "0" || title == "<nil>" {
		title = ""
	}

	worktree := getString(doc.Fields, "worktree_path")
	if worktree == "0" || worktree == "<nil>" {
		worktree = ""
	}

	branch := getString(doc.Fields, "branch")
	if branch == "0" || branch == "<nil>" {
		branch = ""
	}

	_ = strings.TrimSpace // keep import

	return &ClaudeSession{
		ID:           getString(doc.Fields, "id"),
		InstanceID:   getString(doc.Fields, "instance_id"),
		Title:        title,
		CreatedAt:    parseTimestamp(getString(doc.Fields, "created_at")),
		UpdatedAt:    updatedAt,
		MessageCount: getInt(doc.Fields, "message_count"),
		WorktreePath: worktree,
		Branch:       branch,
	}
}
