package firebase

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

// RemoteConfig reads and writes application config from Firebase RTDB /config.
// This allows the Go server to boot with only Firebase credentials,
// pulling all other config (Telegram token, binary paths, etc.) from the cloud.
type RemoteConfig struct {
	rtdb *RTDB
}

func NewRemoteConfig(rtdb *RTDB) *RemoteConfig {
	return &RemoteConfig{rtdb: rtdb}
}

// Pull reads the config from Firebase. Returns nil map if no config exists.
func (rc *RemoteConfig) Pull(ctx context.Context) (map[string]string, error) {
	var raw map[string]interface{}
	if err := rc.rtdb.Get(ctx, "config", &raw); err != nil {
		return nil, fmt.Errorf("pulling config: %w", err)
	}

	if raw == nil {
		return nil, nil
	}

	settings := make(map[string]string, len(raw))
	for k, v := range raw {
		settings[k] = fmt.Sprint(v)
	}
	return settings, nil
}

// Push writes the config to Firebase.
func (rc *RemoteConfig) Push(ctx context.Context, settings map[string]string) error {
	data := make(map[string]interface{}, len(settings))
	for k, v := range settings {
		data[k] = v
	}
	return rc.rtdb.Set(ctx, "config", data)
}

// WaitForConfig blocks until /config is set in Firebase.
// Returns the settings when they appear.
func (rc *RemoteConfig) WaitForConfig(ctx context.Context) (map[string]string, error) {
	slog.Info("firebase: waiting for config to be set via web frontend...")

	events := make(chan SSEEvent, 8)
	go func() {
		if err := rc.rtdb.Listen(ctx, "config", events); err != nil && ctx.Err() == nil {
			slog.Error("firebase: config listener error", "error", err)
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case evt := <-events:
			if evt.Event == "put" && evt.Path == "/" {
				// Config was set at the root level — try to pull it.
				settings, err := rc.Pull(ctx)
				if err != nil {
					slog.Warn("firebase: failed to pull config after event", "error", err)
					continue
				}
				if settings != nil && len(settings) > 0 {
					slog.Info("firebase: config received", "keys", len(settings))
					return settings, nil
				}
			}
		case <-time.After(5 * time.Second):
			// Periodic poll as fallback.
			settings, err := rc.Pull(ctx)
			if err == nil && settings != nil && len(settings) > 0 {
				return settings, nil
			}
		}
	}
}
