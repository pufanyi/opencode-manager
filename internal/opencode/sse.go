package opencode

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// SSESubscriber manages an SSE connection to an OpenCode instance.
type SSESubscriber struct {
	client   *Client
	handlers map[string][]EventHandler
}

// EventHandler processes an SSE event.
type EventHandler func(eventType string, data json.RawMessage)

func NewSSESubscriber(client *Client) *SSESubscriber {
	return &SSESubscriber{
		client:   client,
		handlers: make(map[string][]EventHandler),
	}
}

// On registers a handler for a specific event type. Use "*" for all events.
func (s *SSESubscriber) On(eventType string, handler EventHandler) {
	s.handlers[eventType] = append(s.handlers[eventType], handler)
}

// Subscribe starts listening for SSE events. Blocks until context is cancelled.
// Automatically reconnects on connection drops.
func (s *SSESubscriber) Subscribe(ctx context.Context) error {
	for {
		err := s.connect(ctx)
		if ctx.Err() != nil {
			return ctx.Err()
		}

		slog.Warn("SSE connection dropped, reconnecting", "error", err)

		select {
		case <-time.After(2 * time.Second):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (s *SSESubscriber) connect(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, "GET", s.client.baseURL+"/event", nil)
	if err != nil {
		return fmt.Errorf("creating SSE request: %w", err)
	}

	req.SetBasicAuth("", s.client.password)
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Cache-Control", "no-cache")

	httpClient := &http.Client{
		Timeout: 0, // No timeout for SSE
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("connecting to SSE: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("SSE connection failed: status %d", resp.StatusCode)
	}

	scanner := bufio.NewScanner(resp.Body)
	// Increase buffer for potentially large SSE messages
	scanner.Buffer(make([]byte, 0, 1024*1024), 1024*1024)

	var event SSEEvent
	heartbeatTimeout := time.NewTimer(15 * time.Second)
	defer heartbeatTimeout.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-heartbeatTimeout.C:
			return fmt.Errorf("SSE heartbeat timeout")
		default:
		}

		if !scanner.Scan() {
			if err := scanner.Err(); err != nil {
				return fmt.Errorf("SSE read error: %w", err)
			}
			return fmt.Errorf("SSE stream ended")
		}

		heartbeatTimeout.Reset(15 * time.Second)
		line := scanner.Text()

		if line == "" {
			// Empty line = end of event
			if event.Event != "" && event.Data != "" {
				s.dispatch(event)
			}
			event = SSEEvent{}
			continue
		}

		if strings.HasPrefix(line, ":") {
			// Comment/heartbeat
			continue
		}

		if strings.HasPrefix(line, "event:") {
			event.Event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		} else if strings.HasPrefix(line, "data:") {
			data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if event.Data != "" {
				event.Data += "\n" + data
			} else {
				event.Data = data
			}
		} else if strings.HasPrefix(line, "id:") {
			event.ID = strings.TrimSpace(strings.TrimPrefix(line, "id:"))
		}
	}
}

func (s *SSESubscriber) dispatch(event SSEEvent) {
	raw := json.RawMessage(event.Data)

	// Dispatch to specific handlers
	if handlers, ok := s.handlers[event.Event]; ok {
		for _, h := range handlers {
			h(event.Event, raw)
		}
	}

	// Dispatch to wildcard handlers
	if handlers, ok := s.handlers["*"]; ok {
		for _, h := range handlers {
			h(event.Event, raw)
		}
	}
}
