package firebase

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

// Config holds Firebase project configuration.
// Supports two auth modes:
//  1. Email/Password — for dedicated service accounts
//  2. RefreshToken — from browser-based login (Google, etc.)
type Config struct {
	APIKey       string
	DatabaseURL  string
	ProjectID    string // required for Firestore
	Email        string // optional (email/password mode)
	Password     string // optional (email/password mode)
	RefreshToken string // optional (browser login mode)
}

// Client is the main Firebase client for the Go server.
// It ties together Auth, RTDB, Firestore, Streamer, Presence, and CommandListener.
type Client struct {
	Auth      *Auth
	RTDB      *RTDB
	Firestore *Firestore
	Streamer  *Streamer
	Presence  *Presence
	Commands  *CommandListener

	config Config
}

// NewClient creates a Firebase client and signs in.
func NewClient(cfg Config) (*Client, error) {
	if cfg.APIKey == "" || cfg.DatabaseURL == "" {
		return nil, fmt.Errorf("firebase: APIKey and DatabaseURL are required")
	}

	auth := NewAuth(cfg.APIKey)

	// Sign in with refresh token (from browser login) or email/password.
	if cfg.RefreshToken != "" {
		if err := auth.SignInWithRefreshToken(cfg.RefreshToken); err != nil {
			return nil, fmt.Errorf("firebase: %w", err)
		}
	} else if cfg.Email != "" && cfg.Password != "" {
		if err := auth.SignIn(cfg.Email, cfg.Password); err != nil {
			return nil, fmt.Errorf("firebase: %w", err)
		}
	} else {
		return nil, fmt.Errorf("firebase: either RefreshToken or Email+Password required")
	}

	rtdb := NewRTDB(cfg.DatabaseURL, auth)

	var fs *Firestore
	if cfg.ProjectID != "" {
		fs = NewFirestore(cfg.ProjectID, auth)
	}

	return &Client{
		Auth:      auth,
		RTDB:      rtdb,
		Firestore: fs,
		Streamer:  NewStreamer(rtdb, 300*time.Millisecond),
		Presence:  NewPresence(rtdb, 30*time.Second),
		config:    cfg,
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
