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
// Automatically reconnects on connection drops with exponential backoff.
func (s *SSESubscriber) Subscribe(ctx context.Context) error {
	var failures int
	for {
		connected, err := s.connect(ctx)
		if ctx.Err() != nil {
			return ctx.Err()
		}

		if connected {
			failures = 0 // Reset backoff after a successful connection
		}
		failures++
		delay := backoff(failures)
		slog.Warn("SSE connection dropped, reconnecting", "error", err, "retry_in", delay)

		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// WaitReady polls the server until it responds or the context is cancelled.
func (s *SSESubscriber) WaitReady(ctx context.Context, timeout time.Duration) error {
	deadline := time.After(timeout)
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline:
			return fmt.Errorf("instance not ready after %s", timeout)
		case <-ticker.C:
			_, err := s.client.Status()
			if err == nil {
				return nil
			}
		}
	}
}

func backoff(failures int) time.Duration {
	d := time.Duration(1<<uint(min(failures, 6))) * time.Second // 2s, 4s, 8s ... cap 64s
	return d
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// connect returns (true, err) if it connected successfully but later dropped,
// or (false, err) if it failed to connect at all.
func (s *SSESubscriber) connect(ctx context.Context) (connected bool, err error) {
	req, err := http.NewRequestWithContext(ctx, "GET", s.client.baseURL+"/event", nil)
	if err != nil {
		return false, fmt.Errorf("creating SSE request: %w", err)
	}

	req.SetBasicAuth("", s.client.password)
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Cache-Control", "no-cache")

	httpClient := &http.Client{
		Timeout: 0, // No timeout for SSE
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return false, fmt.Errorf("connecting to SSE: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("SSE connection failed: status %d", resp.StatusCode)
	}

	connected = true
	scanner := bufio.NewScanner(resp.Body)
	// Increase buffer for potentially large SSE messages
	scanner.Buffer(make([]byte, 0, 1024*1024), 1024*1024)

	var event SSEEvent
	heartbeatTimeout := time.NewTimer(15 * time.Second)
	defer heartbeatTimeout.Stop()

	for {
		select {
		case <-ctx.Done():
			return connected, ctx.Err()
		case <-heartbeatTimeout.C:
			return connected, fmt.Errorf("SSE heartbeat timeout")
		default:
		}

		if !scanner.Scan() {
			if err := scanner.Err(); err != nil {
				return connected, fmt.Errorf("SSE read error: %w", err)
			}
			return connected, fmt.Errorf("SSE stream ended")
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
