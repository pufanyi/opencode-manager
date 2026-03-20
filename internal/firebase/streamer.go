package firebase

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/pufanyi/opencode-manager/internal/provider"
)

// Streamer buffers provider StreamEvents and flushes them to RTDB periodically.
type Streamer struct {
	rtdb          *RTDB
	flushInterval time.Duration
}

func NewStreamer(rtdb *RTDB, flushInterval time.Duration) *Streamer {
	if flushInterval <= 0 {
		flushInterval = 300 * time.Millisecond
	}
	return &Streamer{
		rtdb:          rtdb,
		flushInterval: flushInterval,
	}
}

// streamState holds the buffered state for one active session stream.
type streamState struct {
	mu        sync.Mutex
	content   string
	toolCalls []map[string]interface{}
	dirty     bool
}

// WrapEvents intercepts a provider event channel, streams events to RTDB,
// and returns a new channel that passes events through unchanged.
// The caller reads from the returned channel exactly as before.
func (s *Streamer) WrapEvents(ctx context.Context, sessionID string, ch <-chan provider.StreamEvent) <-chan provider.StreamEvent {
	out := make(chan provider.StreamEvent, 16)

	go func() {
		defer close(out)
		s.streamSession(ctx, sessionID, ch, out)
	}()

	return out
}

func (s *Streamer) streamSession(ctx context.Context, sessionID string, in <-chan provider.StreamEvent, out chan<- provider.StreamEvent) {
	path := "streams/" + sessionID
	state := &streamState{}

	// Initialize the stream node.
	if err := s.rtdb.Set(ctx, path, map[string]interface{}{
		"content":    "",
		"status":     "streaming",
		"tool_calls": []interface{}{},
		"updated_at": time.Now().UnixMilli(),
	}); err != nil {
		slog.Warn("firebase: failed to init stream", "session", sessionID, "error", err)
	}

	// Periodic flush goroutine.
	ticker := time.NewTicker(s.flushInterval)
	defer ticker.Stop()

	flushDone := make(chan struct{})
	go func() {
		defer close(flushDone)
		for {
			select {
			case <-ticker.C:
				s.flush(ctx, path, state)
			case <-ctx.Done():
				return
			case <-flushDone:
				return
			}
		}
	}()

	for evt := range in {
		// Buffer the event for Firebase.
		state.mu.Lock()
		switch evt.Type {
		case "text":
			state.content = evt.Text // Text is already accumulated by provider
			state.dirty = true
		case "tool_use":
			state.toolCalls = updateToolCalls(state.toolCalls, evt)
			state.dirty = true
		case "done":
			// Final flush handled below.
		case "error":
			// Final flush handled below.
		}
		state.mu.Unlock()

		// Pass through to original consumer.
		select {
		case out <- evt:
		case <-ctx.Done():
			return
		}

		// Handle terminal events.
		if evt.Type == "done" || evt.Type == "error" {
			s.flushFinal(ctx, path, state, evt)
			return
		}
	}

	// Channel closed without done/error — mark complete anyway.
	s.flushFinal(ctx, path, state, provider.StreamEvent{Type: "done"})
}

func (s *Streamer) flush(ctx context.Context, path string, state *streamState) {
	state.mu.Lock()
	if !state.dirty {
		state.mu.Unlock()
		return
	}
	data := map[string]interface{}{
		"content":    state.content,
		"tool_calls": state.toolCalls,
		"updated_at": time.Now().UnixMilli(),
	}
	state.dirty = false
	state.mu.Unlock()

	if err := s.rtdb.Update(ctx, path, data); err != nil {
		slog.Warn("firebase: flush failed", "path", path, "error", err)
	}
}

func (s *Streamer) flushFinal(ctx context.Context, path string, state *streamState, evt provider.StreamEvent) {
	state.mu.Lock()
	data := map[string]interface{}{
		"content":    state.content,
		"tool_calls": state.toolCalls,
		"updated_at": time.Now().UnixMilli(),
	}
	state.mu.Unlock()

	if evt.Type == "error" {
		data["status"] = "error"
		data["error"] = evt.Error
	} else {
		data["status"] = "complete"
	}

	if err := s.rtdb.Update(ctx, path, data); err != nil {
		slog.Warn("firebase: final flush failed", "path", path, "error", err)
	}
}

func updateToolCalls(calls []map[string]interface{}, evt provider.StreamEvent) []map[string]interface{} {
	// Find existing tool call by name and update, or append new one.
	for i, tc := range calls {
		if tc["name"] == evt.ToolName && tc["status"] != "completed" && tc["status"] != "error" {
			calls[i]["status"] = evt.ToolState
			calls[i]["detail"] = evt.ToolDetail
			return calls
		}
	}
	return append(calls, map[string]interface{}{
		"name":   evt.ToolName,
		"status": evt.ToolState,
		"detail": evt.ToolDetail,
	})
}

// CleanupStream removes a stream node after it's no longer needed.
func (s *Streamer) CleanupStream(ctx context.Context, sessionID string) {
	if err := s.rtdb.Delete(ctx, "streams/"+sessionID); err != nil {
		slog.Warn("firebase: cleanup stream failed", "session", sessionID, "error", err)
	}
}
