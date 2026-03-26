package store

import (
	"fmt"
	"strings"
	"time"
)

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
	_ = fmt.Sprint        // keep import

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
