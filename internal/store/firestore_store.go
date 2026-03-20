package store

import (
	"context"
	"fmt"
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
// All data is scoped under users/{uid}/ in Firestore.
type FirestoreStore struct {
	fs  FirestoreClient
	ctx context.Context
	uid string // Firebase user ID — all paths are scoped to this user
}

// NewFirestoreStore creates a Firestore-backed store scoped to the given user.
func NewFirestoreStore(ctx context.Context, fs FirestoreClient, uid string) *FirestoreStore {
	return &FirestoreStore{fs: fs, ctx: ctx, uid: uid}
}

func (s *FirestoreStore) Close() error { return nil }

// ── Path helpers ────────────────────────────────────────────────────────────

func (s *FirestoreStore) instancePath(id string) string {
	return fmt.Sprintf("users/%s/instances/%s", s.uid, id)
}

func (s *FirestoreStore) instancesCollection() string {
	return fmt.Sprintf("users/%s/instances", s.uid)
}

func (s *FirestoreStore) sessionPath(instanceID, sessionID string) string {
	return fmt.Sprintf("users/%s/instances/%s/sessions/%s", s.uid, instanceID, sessionID)
}

func (s *FirestoreStore) sessionsCollection(instanceID string) string {
	return fmt.Sprintf("users/%s/instances/%s/sessions", s.uid, instanceID)
}

func (s *FirestoreStore) messagePath(instanceID, sessionID, messageID string) string {
	return fmt.Sprintf("users/%s/instances/%s/sessions/%s/messages/%s", s.uid, instanceID, sessionID, messageID)
}

func (s *FirestoreStore) messagesCollection(instanceID, sessionID string) string {
	return fmt.Sprintf("users/%s/instances/%s/sessions/%s/messages", s.uid, instanceID, sessionID)
}

func (s *FirestoreStore) clientPath(clientID string) string {
	return fmt.Sprintf("users/%s/clients/%s", s.uid, clientID)
}

func (s *FirestoreStore) userConfigPath() string {
	return fmt.Sprintf("users/%s/config/user", s.uid)
}

func (s *FirestoreStore) clientConfigPath(clientID string) string {
	return fmt.Sprintf("users/%s/config/clients/%s", s.uid, clientID)
}

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
		"client_id":     inst.ClientID,
		"created_at":    now,
		"updated_at":    now,
	}
	return s.fs.SetDoc(s.ctx, s.instancePath(inst.ID), fields)
}

func (s *FirestoreStore) GetInstance(id string) (*Instance, error) {
	doc, err := s.fs.GetDoc(s.ctx, s.instancePath(id))
	if err != nil {
		return nil, fmt.Errorf("getting instance %s: %w", id, err)
	}
	if doc == nil {
		return nil, nil
	}
	return docToInstance(doc), nil
}

func (s *FirestoreStore) GetInstanceByName(name string) (*Instance, error) {
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
	docs, err := s.fs.ListDocs(s.ctx, s.instancesCollection())
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

func (s *FirestoreStore) GetInstancesByClient(clientID string) ([]*Instance, error) {
	all, err := s.ListInstances()
	if err != nil {
		return nil, err
	}
	var result []*Instance
	for _, inst := range all {
		if inst.ClientID == clientID {
			result = append(result, inst)
		}
	}
	return result, nil
}

func (s *FirestoreStore) UpdateInstanceStatus(id, status string) error {
	return s.fs.UpdateDoc(s.ctx, s.instancePath(id), map[string]interface{}{
		"status":     status,
		"updated_at": time.Now(),
	})
}

func (s *FirestoreStore) UpdateInstancePort(id string, port int) error {
	return s.fs.UpdateDoc(s.ctx, s.instancePath(id), map[string]interface{}{
		"port":       port,
		"updated_at": time.Now(),
	})
}

func (s *FirestoreStore) DeleteInstance(id string) error {
	// Delete associated sessions first — we need to know the instance to enumerate sessions.
	sessions, _ := s.ListClaudeSessions(id)
	for _, sess := range sessions {
		_ = s.DeleteClaudeSession(id, sess.ID)
	}
	return s.fs.DeleteDoc(s.ctx, s.instancePath(id))
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
	return s.fs.SetDoc(s.ctx, s.sessionPath(instanceID, sessionID), fields)
}

func (s *FirestoreStore) GetClaudeSession(instanceID, sessionID string) (*ClaudeSession, error) {
	doc, err := s.fs.GetDoc(s.ctx, s.sessionPath(instanceID, sessionID))
	if err != nil {
		return nil, fmt.Errorf("getting session %s: %w", sessionID, err)
	}
	if doc == nil {
		return nil, nil
	}
	return docToSession(doc), nil
}

func (s *FirestoreStore) ListClaudeSessions(instanceID string) ([]ClaudeSession, error) {
	docs, err := s.fs.ListDocs(s.ctx, s.sessionsCollection(instanceID))
	if err != nil {
		return nil, fmt.Errorf("listing sessions: %w", err)
	}
	var sessions []ClaudeSession
	for _, doc := range docs {
		sessions = append(sessions, *docToSession(doc))
	}
	return sessions, nil
}

func (s *FirestoreStore) UpdateClaudeSessionTitle(instanceID, sessionID, title string) error {
	return s.fs.UpdateDoc(s.ctx, s.sessionPath(instanceID, sessionID), map[string]interface{}{
		"title":      title,
		"updated_at": time.Now(),
	})
}

func (s *FirestoreStore) UpdateClaudeSessionActivity(instanceID, sessionID string) error {
	doc, err := s.fs.GetDoc(s.ctx, s.sessionPath(instanceID, sessionID))
	if err != nil || doc == nil {
		return err
	}
	count := getInt(doc.Fields, "message_count")
	return s.fs.UpdateDoc(s.ctx, s.sessionPath(instanceID, sessionID), map[string]interface{}{
		"message_count": count + 1,
		"updated_at":    time.Now(),
	})
}

func (s *FirestoreStore) DeleteClaudeSession(instanceID, sessionID string) error {
	// Delete messages subcollection first.
	msgs, _ := s.fs.ListDocs(s.ctx, s.messagesCollection(instanceID, sessionID))
	for _, msg := range msgs {
		_ = s.fs.DeleteDoc(s.ctx, s.messagePath(instanceID, sessionID, DocIDFromName(msg.Name)))
	}
	return s.fs.DeleteDoc(s.ctx, s.sessionPath(instanceID, sessionID))
}

// ── Client Registration ─────────────────────────────────────────────────────

func (s *FirestoreStore) RegisterClient(info *ClientInfo) error {
	fields := map[string]interface{}{
		"client_id":  info.ClientID,
		"hostname":   info.Hostname,
		"started_at": info.StartedAt,
		"updated_at": time.Now(),
	}
	return s.fs.SetDoc(s.ctx, s.clientPath(info.ClientID), fields)
}

// ── User Config ─────────────────────────────────────────────────────────────

func (s *FirestoreStore) GetUserConfig() (map[string]string, error) {
	doc, err := s.fs.GetDoc(s.ctx, s.userConfigPath())
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

func (s *FirestoreStore) SetUserConfig(config map[string]string) error {
	fields := make(map[string]interface{}, len(config))
	for k, v := range config {
		fields[k] = v
	}
	return s.fs.SetDoc(s.ctx, s.userConfigPath(), fields)
}

// ── Client Config ───────────────────────────────────────────────────────────

func (s *FirestoreStore) GetClientConfig(clientID string) (map[string]string, error) {
	doc, err := s.fs.GetDoc(s.ctx, s.clientConfigPath(clientID))
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

func (s *FirestoreStore) SetClientConfig(clientID string, config map[string]string) error {
	fields := make(map[string]interface{}, len(config))
	for k, v := range config {
		fields[k] = v
	}
	return s.fs.SetDoc(s.ctx, s.clientConfigPath(clientID), fields)
}

// ── Message History ─────────────────────────────────────────────────────────

func (s *FirestoreStore) SaveMessage(instanceID, sessionID string, msg *Message) error {
	if msg.ID == "" {
		msg.ID = fmt.Sprintf("%d", time.Now().UnixNano())
	}
	if msg.CreatedAt.IsZero() {
		msg.CreatedAt = time.Now()
	}

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

	return s.fs.SetDoc(s.ctx, s.messagePath(instanceID, sessionID, msg.ID), fields)
}

func (s *FirestoreStore) ListMessages(instanceID, sessionID string) ([]*Message, error) {
	docs, err := s.fs.ListDocs(s.ctx, s.messagesCollection(instanceID, sessionID))
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
		ClientID:     getString(doc.Fields, "client_id"),
		CreatedAt:    parseTimestamp(getString(doc.Fields, "created_at")),
		UpdatedAt:    parseTimestamp(getString(doc.Fields, "updated_at")),
	}
}

func docToSession(doc *FirestoreDoc) *ClaudeSession {
	updatedAt := parseTimestamp(getString(doc.Fields, "updated_at"))
	if updatedAt.IsZero() {
		updatedAt = parseTimestamp(doc.UpdateTime)
	}

	title := getString(doc.Fields, "title")
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
