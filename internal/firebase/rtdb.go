package firebase

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// sseClient is an HTTP client optimised for long-lived SSE connections.
// It forces HTTP/1.1 (Firebase RTDB SSE works more reliably over HTTP/1.1)
// and keeps TCP keep-alive enabled so the OS detects dead peers.
var sseClient = &http.Client{
	Transport: &http.Transport{
		// Force HTTP/1.1 — disable HTTP/2 by providing a non-nil but empty TLSNextProto.
		TLSNextProto: make(map[string]func(authority string, c *tls.Conn) http.RoundTripper),
		DialContext: (&net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		TLSHandshakeTimeout: 10 * time.Second,
		// No idle timeout — SSE connections live for a long time.
		IdleConnTimeout: 0,
	},
	// No overall timeout — the SSE stream runs indefinitely.
	Timeout: 0,
}

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

// newRequest creates an authenticated request using the RTDB REST auth query param.
// Firebase user ID tokens are accepted by RTDB as ?auth=<token>.
func (r *RTDB) newRequest(ctx context.Context, method, path string, body io.Reader) (*http.Request, error) {
	token, err := r.auth.Token()
	if err != nil {
		return nil, err
	}

	reqURL, err := url.Parse(r.url(path))
	if err != nil {
		return nil, err
	}
	q := reqURL.Query()
	q.Set("auth", token)
	reqURL.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, method, reqURL.String(), body)
	if err != nil {
		return nil, err
	}
	return req, nil
}

// Set writes data at the given path (PUT — replaces entirely).
func (r *RTDB) Set(ctx context.Context, path string, data interface{}) error {
	body, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("marshaling data: %w", err)
	}

	req, err := r.newRequest(ctx, "PUT", path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("RTDB set: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return r.responseError("RTDB set", path, resp)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	return nil
}

// Update merges data at the given path (PATCH — partial update).
func (r *RTDB) Update(ctx context.Context, path string, data map[string]interface{}) error {
	body, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("marshaling data: %w", err)
	}

	req, err := r.newRequest(ctx, "PATCH", path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("RTDB update: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return r.responseError("RTDB update", path, resp)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	return nil
}

// Delete removes data at the given path.
func (r *RTDB) Delete(ctx context.Context, path string) error {
	req, err := r.newRequest(ctx, "DELETE", path, nil)
	if err != nil {
		return err
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("RTDB delete: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return r.responseError("RTDB delete", path, resp)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	return nil
}

// Get reads data at the given path.
func (r *RTDB) Get(ctx context.Context, path string, dest interface{}) error {
	req, err := r.newRequest(ctx, "GET", path, nil)
	if err != nil {
		return err
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("RTDB get: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return r.responseError("RTDB get", path, resp)
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
	req, err := r.newRequest(ctx, "GET", path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Cache-Control", "no-cache")

	connStart := time.Now()
	resp, err := sseClient.Do(req)
	if err != nil {
		return err
	}
	defer func() {
		resp.Body.Close()
		slog.Debug("firebase SSE connection closed", "path", path, "duration", time.Since(connStart).Round(time.Second))
	}()

	if resp.StatusCode != 200 {
		return r.responseError("SSE connect", path, resp)
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

func (r *RTDB) responseError(op, path string, resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
	debug := resp.Header.Get("X-Firebase-Auth-Debug")
	if debug != "" {
		return fmt.Errorf("%s %s: status %d: %s (auth-debug: %s)", op, path, resp.StatusCode, strings.TrimSpace(string(body)), debug)
	}
	if len(body) > 0 {
		return fmt.Errorf("%s %s: status %d: %s", op, path, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return fmt.Errorf("%s %s: status %d", op, path, resp.StatusCode)
}
