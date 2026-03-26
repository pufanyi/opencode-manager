package firebase

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"
)

// Command represents a command sent from the web frontend via RTDB.
type Command struct {
	Action  string          `json:"action"`  // "start", "stop", "delete", "prompt", "create_session"
	Payload json.RawMessage `json:"payload"` // Action-specific data
	Status  string          `json:"status"`  // "pending", "ack", "done", "error"
	UserID  string          `json:"user_id"` // Firebase Auth UID
}

// PromptPayload is the payload for "prompt" commands.
type PromptPayload struct {
	SessionID string `json:"session_id"`
	Content   string `json:"content"`
}

// CommandHandler is called when a new command is received.
// It should execute the command and return an optional result and error.
type CommandHandler func(ctx context.Context, instanceID, commandID string, cmd Command) (interface{}, error)

// CommandListener watches RTDB for new commands and dispatches them.
// Listens on users/{uid}/commands via SSE.
type CommandListener struct {
	rtdb     *RTDB
	uid      string
	clientID string
	handler  CommandHandler
}

func NewCommandListener(rtdb *RTDB, uid, clientID string, handler CommandHandler) *CommandListener {
	return &CommandListener{
		rtdb:     rtdb,
		uid:      uid,
		clientID: clientID,
		handler:  handler,
	}
}

// Listen watches all commands for this user.
// Blocks until context is cancelled.
func (cl *CommandListener) Listen(ctx context.Context) error {
	events := make(chan SSEEvent, 32)
	basePath := CommandsBasePath(cl.uid)

	go func() {
		if err := cl.rtdb.Listen(ctx, basePath, events); err != nil && ctx.Err() == nil {
			slog.Error("firebase: command listener stopped", "error", err)
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case evt := <-events:
			if evt.Event != "put" && evt.Event != "patch" {
				continue
			}
			cl.handleEvent(ctx, evt)
		}
	}
}

func (cl *CommandListener) handleEvent(ctx context.Context, evt SSEEvent) {
	parts := splitPath(evt.Path)

	switch len(parts) {
	case 0:
		// Path "/" — initial snapshot: { instanceId: { cmdId: {...}, ... }, ... }
		var instances map[string]map[string]json.RawMessage
		if err := json.Unmarshal(evt.Data, &instances); err != nil {
			return
		}
		for instID, cmds := range instances {
			for cmdID, raw := range cmds {
				cl.processCommand(ctx, instID, cmdID, raw)
			}
		}
	case 1:
		// Path "/{instanceId}" — new or updated instance node: { cmdId: {...}, ... }
		var cmds map[string]json.RawMessage
		if err := json.Unmarshal(evt.Data, &cmds); err != nil {
			return
		}
		for cmdID, raw := range cmds {
			cl.processCommand(ctx, parts[0], cmdID, raw)
		}
	case 2:
		// Path "/{instanceId}/{commandId}" — single command
		cl.processCommand(ctx, parts[0], parts[1], evt.Data)
	default:
		// Deeper path (field-level update, e.g. our own status writes) — skip.
	}
}

// processCommand parses a command from inline SSE data and executes it if pending.
func (cl *CommandListener) processCommand(ctx context.Context, instanceID, commandID string, data json.RawMessage) {
	var cmd Command
	if err := json.Unmarshal(data, &cmd); err != nil {
		return
	}

	if cmd.Status != "pending" {
		// Clean up stale commands discovered during reconnection.
		if cmd.Status == "done" || cmd.Status == "error" {
			cmdPath := CommandPath(cl.uid, instanceID, commandID)
			go func() {
				if err := cl.rtdb.Delete(context.Background(), cmdPath); err != nil {
					slog.Warn("firebase: failed to cleanup stale command", "path", cmdPath, "error", err)
				}
			}()
		}
		return
	}

	cmdPath := CommandPath(cl.uid, instanceID, commandID)

	// Acknowledge with client_id.
	if err := cl.rtdb.Update(ctx, cmdPath, map[string]interface{}{
		"status":             "ack",
		"acked_by_client_id": cl.clientID,
		"updated_at":         time.Now().UnixMilli(),
	}); err != nil {
		slog.Warn("firebase: failed to ack command", "error", err)
	}

	slog.Info("firebase: executing command",
		"instance", instanceID, "action", cmd.Action, "command", commandID)

	// Execute.
	result, err := cl.handler(ctx, instanceID, commandID, cmd)

	// Update status.
	update := map[string]interface{}{
		"updated_at": time.Now().UnixMilli(),
	}
	if err != nil {
		update["status"] = "error"
		update["error"] = err.Error()
	} else {
		update["status"] = "done"
		if result != nil {
			resultJSON, _ := json.Marshal(result)
			update["result"] = json.RawMessage(resultJSON)
		}
	}

	if err := cl.rtdb.Update(ctx, cmdPath, update); err != nil {
		slog.Warn("firebase: failed to update command status", "error", err)
	}

	// Delete the command after a short delay so the frontend can read the result.
	go func() {
		time.Sleep(10 * time.Second)
		if err := cl.rtdb.Delete(context.Background(), cmdPath); err != nil {
			slog.Warn("firebase: failed to cleanup command", "path", cmdPath, "error", err)
		}
	}()
}

func splitPath(path string) []string {
	var parts []string
	for _, p := range splitOnSlash(path) {
		if p != "" {
			parts = append(parts, p)
		}
	}
	return parts
}

func splitOnSlash(s string) []string {
	result := []string{}
	current := ""
	for _, c := range s {
		if c == '/' {
			result = append(result, current)
			current = ""
		} else {
			current += string(c)
		}
	}
	result = append(result, current)
	return result
}
