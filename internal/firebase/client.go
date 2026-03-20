package firebase

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

// Config holds Firebase project configuration.
// APIKey and DatabaseURL are public values (same as web frontend config).
// Email/Password are the Go service account credentials in Firebase Auth.
type Config struct {
	APIKey      string
	DatabaseURL string
	Email       string
	Password    string
}

// Client is the main Firebase client for the Go server.
// It ties together Auth, RTDB, Streamer, Presence, and CommandListener.
type Client struct {
	Auth     *Auth
	RTDB     *RTDB
	Streamer *Streamer
	Presence *Presence
	Commands *CommandListener

	config Config
}

// NewClient creates a Firebase client and signs in.
func NewClient(cfg Config) (*Client, error) {
	if cfg.APIKey == "" || cfg.DatabaseURL == "" {
		return nil, fmt.Errorf("firebase: APIKey and DatabaseURL are required")
	}
	if cfg.Email == "" || cfg.Password == "" {
		return nil, fmt.Errorf("firebase: Email and Password are required for Go client auth")
	}

	auth := NewAuth(cfg.APIKey)
	if err := auth.SignIn(cfg.Email, cfg.Password); err != nil {
		return nil, fmt.Errorf("firebase: %w", err)
	}

	rtdb := NewRTDB(cfg.DatabaseURL, auth)

	return &Client{
		Auth:     auth,
		RTDB:     rtdb,
		Streamer: NewStreamer(rtdb, 300*time.Millisecond),
		Presence: NewPresence(rtdb, 30*time.Second),
		config:   cfg,
	}, nil
}

// SetCommandHandler registers the handler for web frontend commands.
func (c *Client) SetCommandHandler(handler CommandHandler) {
	c.Commands = NewCommandListener(c.RTDB, handler)
}

// StartBackground starts presence heartbeats and command listener.
// Non-blocking — spawns goroutines.
func (c *Client) StartBackground(ctx context.Context, instanceIDs []string) {
	// Presence heartbeats.
	go c.Presence.Start(ctx, instanceIDs)

	// Command listener.
	if c.Commands != nil {
		go func() {
			if err := c.Commands.Listen(ctx); err != nil && ctx.Err() == nil {
				slog.Error("firebase: command listener stopped", "error", err)
			}
		}()
	}

	slog.Info("firebase: background services started",
		"instances", len(instanceIDs),
		"commands", c.Commands != nil)
}
