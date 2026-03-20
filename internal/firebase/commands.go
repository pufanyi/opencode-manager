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
type CommandListener struct {
	rtdb    *RTDB
	handler CommandHandler
}

func NewCommandListener(rtdb *RTDB, handler CommandHandler) *CommandListener {
	return &CommandListener{
		rtdb:    rtdb,
		handler: handler,
	}
}

// Listen watches all commands across all instances.
// Blocks until context is cancelled.
func (cl *CommandListener) Listen(ctx context.Context) error {
	events := make(chan SSEEvent, 32)

	go func() {
		if err := cl.rtdb.Listen(ctx, "commands", events); err != nil && ctx.Err() == nil {
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
	// Path format from root listener: /{instanceId}/{commandId}
	// or /{instanceId}/{commandId}/{field}
	parts := splitPath(evt.Path)
	if len(parts) < 2 {
		return
	}
	instanceID := parts[0]
	commandID := parts[1]

	// Read the full command to check status.
	var cmd Command
	if err := cl.rtdb.Get(ctx, "commands/"+instanceID+"/"+commandID, &cmd); err != nil {
		slog.Warn("firebase: failed to read command", "error", err)
		return
	}

	if cmd.Status != "pending" {
		return // Already processed
	}

	// Acknowledge.
	if err := cl.rtdb.Update(ctx, "commands/"+instanceID+"/"+commandID, map[string]interface{}{
		"status":     "ack",
		"updated_at": time.Now().UnixMilli(),
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

	if err := cl.rtdb.Update(ctx, "commands/"+instanceID+"/"+commandID, update); err != nil {
		slog.Warn("firebase: failed to update command status", "error", err)
	}
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
