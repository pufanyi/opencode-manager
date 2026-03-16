package store

import "fmt"

func (s *Store) migrate() error {
	migrations := []string{
		`CREATE TABLE IF NOT EXISTS instances (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL UNIQUE,
			directory TEXT NOT NULL,
			port INT NOT NULL,
			password TEXT NOT NULL,
			status TEXT NOT NULL DEFAULT 'stopped',
			auto_start BOOLEAN NOT NULL DEFAULT 0,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE TABLE IF NOT EXISTS user_state (
			user_id INTEGER PRIMARY KEY,
			active_instance_id TEXT REFERENCES instances(id),
			active_session_id TEXT,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,
	}

	for i, m := range migrations {
		if _, err := s.db.Exec(m); err != nil {
			return fmt.Errorf("migration %d: %w", i, err)
		}
	}

	return nil
}
