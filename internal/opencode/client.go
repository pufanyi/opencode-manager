package opencode

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

type Client struct {
	baseURL  string
	password string
	http     *http.Client
}

func NewClient(baseURL, password string) *Client {
	return &Client{
		baseURL:  baseURL,
		password: password,
		http: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

func (c *Client) BaseURL() string {
	return c.baseURL
}

func (c *Client) doRequest(method, path string, body any) (*http.Response, error) {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("marshaling request: %w", err)
		}
		reader = bytes.NewReader(data)
	}

	req, err := http.NewRequest(method, c.baseURL+path, reader)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}

	req.SetBasicAuth("opencode", c.password)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("executing request: %w", err)
	}

	if resp.StatusCode >= 400 {
		defer resp.Body.Close()
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("API error %d: %s", resp.StatusCode, string(respBody))
	}

	return resp, nil
}

func (c *Client) decodeResponse(resp *http.Response, target any) error {
	defer resp.Body.Close()
	return json.NewDecoder(resp.Body).Decode(target)
}

// Status checks if the OpenCode server is running.
func (c *Client) Status() (*StatusResponse, error) {
	resp, err := c.doRequest("GET", "/", nil)
	if err != nil {
		return nil, err
	}
	var result StatusResponse
	return &result, c.decodeResponse(resp, &result)
}

// ListSessions returns all sessions.
func (c *Client) ListSessions() ([]Session, error) {
	resp, err := c.doRequest("GET", "/session", nil)
	if err != nil {
		return nil, err
	}
	var result []Session
	return result, c.decodeResponse(resp, &result)
}

// CreateSession creates a new session.
func (c *Client) CreateSession() (*Session, error) {
	resp, err := c.doRequest("POST", "/session", nil)
	if err != nil {
		return nil, err
	}
	var result Session
	return &result, c.decodeResponse(resp, &result)
}

// GetSession retrieves a session by ID.
func (c *Client) GetSession(sessionID string) (*Session, error) {
	resp, err := c.doRequest("GET", "/session/"+sessionID, nil)
	if err != nil {
		return nil, err
	}
	var result Session
	return &result, c.decodeResponse(resp, &result)
}

// ListMessages returns messages for a session.
func (c *Client) ListMessages(sessionID string) ([]Message, error) {
	resp, err := c.doRequest("GET", "/session/"+sessionID+"/message", nil)
	if err != nil {
		return nil, err
	}
	var result []Message
	return result, c.decodeResponse(resp, &result)
}

// PromptAsync sends a prompt asynchronously (fire-and-forget).
func (c *Client) PromptAsync(sessionID, content string) error {
	body := PromptRequest{
		SessionID: sessionID,
		Content:   content,
	}
	resp, err := c.doRequest("POST", "/session/"+sessionID+"/prompt", body)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

// Abort cancels the current running prompt.
func (c *Client) Abort(sessionID string) error {
	resp, err := c.doRequest("POST", "/session/"+sessionID+"/abort", nil)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}
