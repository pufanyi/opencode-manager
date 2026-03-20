package store

import (
	"database/sql"
	"fmt"

	_ "modernc.org/sqlite"
)

// SQLiteStore is the local SQLite-backed implementation of Store.
type SQLiteStore struct {
	db *sql.DB
}

func NewSQLite(dbPath string) (*SQLiteStore, error) {
	db, err := sql.Open("sqlite", dbPath+"?_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		return nil, fmt.Errorf("opening database: %w", err)
	}

	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("pinging database: %w", err)
	}

	s := &SQLiteStore{db: db}
	if err := s.migrate(); err != nil {
		return nil, fmt.Errorf("running migrations: %w", err)
	}

	return s, nil
}

// New is an alias for NewSQLite for backward compatibility.
func New(dbPath string) (*SQLiteStore, error) {
	return NewSQLite(dbPath)
}

func (s *SQLiteStore) Close() error {
	return s.db.Close()
}

func (s *SQLiteStore) DB() *sql.DB {
	return s.db
}

// SaveMessage is a no-op for SQLite (history not persisted locally).
func (s *SQLiteStore) SaveMessage(sessionID string, msg *Message) error {
	return nil
}

// ListMessages returns nil for SQLite (history not persisted locally).
func (s *SQLiteStore) ListMessages(sessionID string) ([]*Message, error) {
	return nil, nil
}
