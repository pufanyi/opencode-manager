package store

import "database/sql"

// SetMessageSession records which session a Telegram message belongs to.
func (s *Store) SetMessageSession(chatID int64, messageID int, sessionID string) error {
	_, err := s.db.Exec(
		`INSERT OR REPLACE INTO message_sessions (chat_id, message_id, session_id) VALUES (?, ?, ?)`,
		chatID, messageID, sessionID,
	)
	return err
}

// GetSessionByMessage looks up the session ID for a given Telegram message.
func (s *Store) GetSessionByMessage(chatID int64, messageID int) (string, error) {
	var sessionID string
	err := s.db.QueryRow(
		`SELECT session_id FROM message_sessions WHERE chat_id = ? AND message_id = ?`,
		chatID, messageID,
	).Scan(&sessionID)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return sessionID, err
}
