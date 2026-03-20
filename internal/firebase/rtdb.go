package firebase

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// RTDB is a client for Firebase Realtime Database REST API.
type RTDB struct {
	baseURL string
	auth    *Auth
}

func NewRTDB(databaseURL string, auth *Auth) *RTDB {
	return &RTDB{
		baseURL: strings.TrimSuffix(databaseURL, "/"),
		auth:    auth,
	}
}

func (r *RTDB) url(path string) string {
	return fmt.Sprintf("%s/%s.json", r.baseURL, strings.TrimPrefix(path, "/"))
}

func (r *RTDB) authURL(path string) (string, error) {
	token, err := r.auth.Token()
	if err != nil {
		return "", err
	}
	return r.url(path) + "?auth=" + token, nil
}

// Set writes data at the given path (PUT — replaces entirely).
func (r *RTDB) Set(ctx context.Context, path string, data interface{}) error {
	u, err := r.authURL(path)
	if err != nil {
		return err
	}

	body, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("marshaling data: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "PUT", u, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("RTDB set: %w", err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)

	if resp.StatusCode != 200 {
		return fmt.Errorf("RTDB set %s: status %d", path, resp.StatusCode)
	}
	return nil
}

// Update merges data at the given path (PATCH — partial update).
func (r *RTDB) Update(ctx context.Context, path string, data map[string]interface{}) error {
	u, err := r.authURL(path)
	if err != nil {
		return err
	}

	body, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("marshaling data: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "PATCH", u, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("RTDB update: %w", err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)

	if resp.StatusCode != 200 {
		return fmt.Errorf("RTDB update %s: status %d", path, resp.StatusCode)
	}
	return nil
}

// Delete removes data at the given path.
func (r *RTDB) Delete(ctx context.Context, path string) error {
	u, err := r.authURL(path)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, "DELETE", u, nil)
	if err != nil {
		return err
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("RTDB delete: %w", err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)

	if resp.StatusCode != 200 {
		return fmt.Errorf("RTDB delete %s: status %d", path, resp.StatusCode)
	}
	return nil
}

// Get reads data at the given path.
func (r *RTDB) Get(ctx context.Context, path string, dest interface{}) error {
	u, err := r.authURL(path)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return err
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("RTDB get: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return fmt.Errorf("RTDB get %s: status %d", path, resp.StatusCode)
	}

	return json.NewDecoder(resp.Body).Decode(dest)
}

// SSEEvent represents an event from the RTDB SSE stream.
type SSEEvent struct {
	Event string          // "put", "patch", "keep-alive", "cancel", "auth_revoked"
	Path  string          // Path relative to the listened location
	Data  json.RawMessage // The data at that path
}

// Listen opens an SSE connection and sends events to the channel.
// Blocks until context is cancelled. Reconnects automatically on disconnect.
func (r *RTDB) Listen(ctx context.Context, path string, events chan<- SSEEvent) error {
	for {
		err := r.listenOnce(ctx, path, events)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		slog.Warn("firebase RTDB listen disconnected, reconnecting",
			"path", path, "error", err)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

func (r *RTDB) listenOnce(ctx context.Context, path string, events chan<- SSEEvent) error {
	u, err := r.authURL(path)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "text/event-stream")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return fmt.Errorf("SSE connect failed: status %d", resp.StatusCode)
	}

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 1024*1024), 1024*1024)

	var eventType string
	for scanner.Scan() {
		line := scanner.Text()

		if strings.HasPrefix(line, "event: ") {
			eventType = strings.TrimPrefix(line, "event: ")
			continue
		}

		if strings.HasPrefix(line, "data: ") {
			dataStr := strings.TrimPrefix(line, "data: ")

			if eventType == "keep-alive" {
				eventType = ""
				continue
			}

			if eventType == "cancel" || eventType == "auth_revoked" {
				return fmt.Errorf("stream %s", eventType)
			}

			var payload struct {
				Path string          `json:"path"`
				Data json.RawMessage `json:"data"`
			}
			if err := json.Unmarshal([]byte(dataStr), &payload); err != nil {
				slog.Warn("firebase RTDB SSE parse error", "error", err)
				eventType = ""
				continue
			}

			select {
			case events <- SSEEvent{
				Event: eventType,
				Path:  payload.Path,
				Data:  payload.Data,
			}:
			case <-ctx.Done():
				return ctx.Err()
			}
			eventType = ""
		}
	}

	if err := scanner.Err(); err != nil {
		return err
	}
	return io.EOF
}
