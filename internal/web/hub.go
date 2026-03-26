package web

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync"

	"github.com/pufanyi/opencode-manager/internal/provider"
)

// StreamHub manages WebSocket connections for real-time streaming.
type StreamHub struct {
	mu      sync.RWMutex
	clients map[*wsClient]bool
}

type wsClient struct {
	sessionFilter string // empty = all sessions
	send          chan []byte
}

func NewStreamHub() *StreamHub {
	return &StreamHub{
		clients: make(map[*wsClient]bool),
	}
}

func (h *StreamHub) Run(ctx context.Context) {
	<-ctx.Done()
	h.mu.Lock()
	for c := range h.clients {
		close(c.send)
	}
	h.clients = make(map[*wsClient]bool)
	h.mu.Unlock()
}

func (h *StreamHub) Broadcast(sessionID string, evt provider.StreamEvent) {
	data, _ := json.Marshal(map[string]any{
		"session_id": sessionID,
		"event":      evt,
	})

	h.mu.RLock()
	defer h.mu.RUnlock()

	for c := range h.clients {
		if c.sessionFilter == "" || c.sessionFilter == sessionID {
			select {
			case c.send <- data:
			default:
				slog.Warn("web: dropped event for slow SSE client",
					"session", sessionID, "type", evt.Type)
			}
		}
	}
}

// HandleWebSocket is a simple SSE-based streaming endpoint (no external WebSocket library needed).
// Clients connect to /api/ws?session=<id> for filtered events, or /api/ws for all.
func (h *StreamHub) HandleWebSocket(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, 500, "streaming not supported")
		return
	}

	sessionFilter := r.URL.Query().Get("session")

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	client := &wsClient{
		sessionFilter: sessionFilter,
		send:          make(chan []byte, 64),
	}

	h.mu.Lock()
	h.clients[client] = true
	h.mu.Unlock()

	defer func() {
		h.mu.Lock()
		delete(h.clients, client)
		h.mu.Unlock()
	}()

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case data, ok := <-client.send:
			if !ok {
				return
			}
			fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
		}
	}
}
