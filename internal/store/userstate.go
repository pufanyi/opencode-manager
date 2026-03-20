package store

import (
	"database/sql"
	"fmt"
)

type UserState struct {
	UserID           int64
	ActiveInstanceID string
	ActiveSessionID  string
}

func (s *SQLiteStore) GetUserState(userID int64) (*UserState, error) {
	state := &UserState{}
	err := s.db.QueryRow(
		`SELECT user_id, COALESCE(active_instance_id, ''), COALESCE(active_session_id, '')
		 FROM user_state WHERE user_id = ?`, userID,
	).Scan(&state.UserID, &state.ActiveInstanceID, &state.ActiveSessionID)

	if err == sql.ErrNoRows {
		return &UserState{UserID: userID}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("getting user state: %w", err)
	}
	return state, nil
}

func (s *SQLiteStore) SetActiveInstance(userID int64, instanceID string) error {
	_, err := s.db.Exec(
		`INSERT INTO user_state (user_id, active_instance_id, active_session_id, updated_at)
		 VALUES (?, ?, '', CURRENT_TIMESTAMP)
		 ON CONFLICT(user_id) DO UPDATE SET
		   active_instance_id = excluded.active_instance_id,
		   active_session_id = '',
		   updated_at = CURRENT_TIMESTAMP`,
		userID, instanceID,
	)
	return err
}

func (s *SQLiteStore) SetActiveSession(userID int64, sessionID string) error {
	_, err := s.db.Exec(
		`UPDATE user_state SET active_session_id = ?, updated_at = CURRENT_TIMESTAMP
		 WHERE user_id = ?`,
		sessionID, userID,
	)
	return err
}

func (s *SQLiteStore) ClearUserState(userID int64, instanceID string) error {
	_, err := s.db.Exec(
		`UPDATE user_state SET active_instance_id = '', active_session_id = '', updated_at = CURRENT_TIMESTAMP
		 WHERE user_id = ? AND active_instance_id = ?`,
		userID, instanceID,
	)
	return err
}
