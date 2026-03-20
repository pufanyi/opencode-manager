package firebase

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

// TelegramState manages Telegram user state and message-session mappings in RTDB.
// All data lives under users/{uid}/telegram/.
type TelegramState struct {
	rtdb *RTDB
	uid  string
}

func NewTelegramState(rtdb *RTDB, uid string) *TelegramState {
	return &TelegramState{rtdb: rtdb, uid: uid}
}

// UserState holds the active instance/session for a Telegram user.
type UserState struct {
	UserID           int64
	ActiveInstanceID string
	ActiveSessionID  string
}

// GetUserState reads the Telegram user's active instance/session.
func (ts *TelegramState) GetUserState(ctx context.Context, telegramUserID int64) (*UserState, error) {
	path := TelegramUserStatePath(ts.uid, telegramUserID)
	var data map[string]interface{}
	if err := ts.rtdb.Get(ctx, path, &data); err != nil {
		return nil, fmt.Errorf("getting telegram user state: %w", err)
	}
	if data == nil {
		return &UserState{UserID: telegramUserID}, nil
	}
	return &UserState{
		UserID:           telegramUserID,
		ActiveInstanceID: stringVal(data, "active_instance_id"),
		ActiveSessionID:  stringVal(data, "active_session_id"),
	}, nil
}

// SetActiveInstance sets the active instance for a Telegram user, clearing the session.
func (ts *TelegramState) SetActiveInstance(ctx context.Context, telegramUserID int64, instanceID string) error {
	path := TelegramUserStatePath(ts.uid, telegramUserID)
	return ts.rtdb.Set(ctx, path, map[string]interface{}{
		"active_instance_id": instanceID,
		"active_session_id":  "",
		"updated_at":         time.Now().UnixMilli(),
	})
}

// SetActiveSession sets the active session for a Telegram user.
func (ts *TelegramState) SetActiveSession(ctx context.Context, telegramUserID int64, sessionID string) error {
	path := TelegramUserStatePath(ts.uid, telegramUserID)
	return ts.rtdb.Update(ctx, path, map[string]interface{}{
		"active_session_id": sessionID,
		"updated_at":        time.Now().UnixMilli(),
	})
}

// ClearUserState clears the user state if it points to the given instance.
func (ts *TelegramState) ClearUserState(ctx context.Context, telegramUserID int64, instanceID string) error {
	state, err := ts.GetUserState(ctx, telegramUserID)
	if err != nil {
		return err
	}
	if state.ActiveInstanceID != instanceID {
		return nil
	}
	path := TelegramUserStatePath(ts.uid, telegramUserID)
	return ts.rtdb.Update(ctx, path, map[string]interface{}{
		"active_instance_id": "",
		"active_session_id":  "",
		"updated_at":         time.Now().UnixMilli(),
	})
}

// SetMessageSession maps a Telegram message to a session.
func (ts *TelegramState) SetMessageSession(ctx context.Context, chatID int64, messageID int, sessionID string) error {
	path := TelegramMessageSessionPath(ts.uid, chatID, messageID)
	return ts.rtdb.Set(ctx, path, map[string]interface{}{
		"session_id": sessionID,
	})
}

// GetSessionByMessage looks up which session a Telegram message belongs to.
func (ts *TelegramState) GetSessionByMessage(ctx context.Context, chatID int64, messageID int) (string, error) {
	path := TelegramMessageSessionPath(ts.uid, chatID, messageID)
	var data map[string]interface{}
	if err := ts.rtdb.Get(ctx, path, &data); err != nil {
		return "", err
	}
	if data == nil {
		return "", nil
	}
	return stringVal(data, "session_id"), nil
}

func stringVal(m map[string]interface{}, key string) string {
	if v, ok := m[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
		return fmt.Sprint(v)
	}
	return ""
}

// Cleanup logs but doesn't fail on RTDB errors since these are ephemeral.
func init() {
	_ = slog.Info // keep import
}
