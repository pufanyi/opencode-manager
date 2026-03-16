package store

import (
	"database/sql"
	"fmt"
)

func (s *Store) migrate() error {
	migrations := []string{
		`CREATE TABLE IF NOT EXISTS instances (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL UNIQUE,
			directory TEXT NOT NULL,
			port INT NOT NULL DEFAULT 0,
			password TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL DEFAULT 'stopped',
			auto_start BOOLEAN NOT NULL DEFAULT 0,
			provider_type TEXT NOT NULL DEFAULT 'opencode',
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE TABLE IF NOT EXISTS user_state (
			user_id INTEGER PRIMARY KEY,
			active_instance_id TEXT REFERENCES instances(id),
			active_session_id TEXT,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE TABLE IF NOT EXISTS claude_sessions (
			id TEXT PRIMARY KEY,
			instance_id TEXT NOT NULL,
			title TEXT DEFAULT '',
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,
	}

	for i, m := range migrations {
		if _, err := s.db.Exec(m); err != nil {
			return fmt.Errorf("migration %d: %w", i, err)
		}
	}

	// Safe ALTER TABLE for existing databases
	safeAddColumn(s.db, "instances", "provider_type", "TEXT NOT NULL DEFAULT 'opencode'")

	return nil
}

func safeAddColumn(db *sql.DB, table, column, colType string) {
	_, _ = db.Exec(fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", table, column, colType))
}
