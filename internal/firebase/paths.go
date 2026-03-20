package firebase

import "fmt"

// ── RTDB path builders (user-scoped) ────────────────────────────────────────

// ClientPresencePath returns the RTDB path for a client's presence heartbeat.
// Path: users/{uid}/clients/{clientID}/presence
func ClientPresencePath(uid, clientID string) string {
	return fmt.Sprintf("users/%s/clients/%s/presence", uid, clientID)
}

// InstanceRuntimePath returns the RTDB path for an instance's runtime status.
// Path: users/{uid}/instances/{instanceID}/runtime
func InstanceRuntimePath(uid, instanceID string) string {
	return fmt.Sprintf("users/%s/instances/%s/runtime", uid, instanceID)
}

// CommandsBasePath returns the RTDB path for listening to all commands.
// Path: users/{uid}/commands
func CommandsBasePath(uid string) string {
	return fmt.Sprintf("users/%s/commands", uid)
}

// CommandPath returns the RTDB path for a specific command.
// Path: users/{uid}/commands/{instanceID}/{cmdID}
func CommandPath(uid, instanceID, cmdID string) string {
	return fmt.Sprintf("users/%s/commands/%s/%s", uid, instanceID, cmdID)
}

// StreamPath returns the RTDB path for a session's stream data.
// Path: users/{uid}/streams/{sessionID}
func StreamPath(uid, sessionID string) string {
	return fmt.Sprintf("users/%s/streams/%s", uid, sessionID)
}

// TelegramUserStatePath returns the RTDB path for Telegram user state.
// Path: users/{uid}/telegram/user_state/{telegramUserID}
func TelegramUserStatePath(uid string, telegramUserID int64) string {
	return fmt.Sprintf("users/%s/telegram/user_state/%d", uid, telegramUserID)
}

// TelegramMessageSessionPath returns the RTDB path for a Telegram message-to-session mapping.
// Path: users/{uid}/telegram/message_sessions/{chatID}_{messageID}
func TelegramMessageSessionPath(uid string, chatID int64, messageID int) string {
	return fmt.Sprintf("users/%s/telegram/message_sessions/%d_%d", uid, chatID, messageID)
}

// ── Firestore path builders (user-scoped) ───────────────────────────────────

// FSInstanceDocPath returns the Firestore path for an instance document.
// Path: users/{uid}/instances/{instanceID}
func FSInstanceDocPath(uid, instanceID string) string {
	return fmt.Sprintf("users/%s/instances/%s", uid, instanceID)
}

// FSInstancesCollectionPath returns the Firestore path for the instances collection.
// Path: users/{uid}/instances
func FSInstancesCollectionPath(uid string) string {
	return fmt.Sprintf("users/%s/instances", uid)
}

// FSSessionDocPath returns the Firestore path for a session document.
// Path: users/{uid}/instances/{instanceID}/sessions/{sessionID}
func FSSessionDocPath(uid, instanceID, sessionID string) string {
	return fmt.Sprintf("users/%s/instances/%s/sessions/%s", uid, instanceID, sessionID)
}

// FSSessionsCollectionPath returns the Firestore path for the sessions collection under an instance.
// Path: users/{uid}/instances/{instanceID}/sessions
func FSSessionsCollectionPath(uid, instanceID string) string {
	return fmt.Sprintf("users/%s/instances/%s/sessions", uid, instanceID)
}

// FSMessageDocPath returns the Firestore path for a message document.
// Path: users/{uid}/instances/{instanceID}/sessions/{sessionID}/messages/{messageID}
func FSMessageDocPath(uid, instanceID, sessionID, messageID string) string {
	return fmt.Sprintf("users/%s/instances/%s/sessions/%s/messages/%s", uid, instanceID, sessionID, messageID)
}

// FSMessagesCollectionPath returns the Firestore path for the messages collection under a session.
// Path: users/{uid}/instances/{instanceID}/sessions/{sessionID}/messages
func FSMessagesCollectionPath(uid, instanceID, sessionID string) string {
	return fmt.Sprintf("users/%s/instances/%s/sessions/%s/messages", uid, instanceID, sessionID)
}

// FSClientDocPath returns the Firestore path for a client registration document.
// Path: users/{uid}/clients/{clientID}
func FSClientDocPath(uid, clientID string) string {
	return fmt.Sprintf("users/%s/clients/%s", uid, clientID)
}

// FSUserConfigPath returns the Firestore path for the user's config document.
// Path: users/{uid}/config/user
func FSUserConfigPath(uid string) string {
	return fmt.Sprintf("users/%s/config/user", uid)
}

// FSClientConfigPath returns the Firestore path for a client's config document.
// Path: users/{uid}/config/clients/{clientID}
func FSClientConfigPath(uid, clientID string) string {
	return fmt.Sprintf("users/%s/config/clients/%s", uid, clientID)
}
