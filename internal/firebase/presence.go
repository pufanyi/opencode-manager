package firebase

import (
	"context"
	"log/slog"
	"time"
)

// Presence sends periodic heartbeats to RTDB so the web frontend
// can show whether the Go server is online.
type Presence struct {
	rtdb     *RTDB
	interval time.Duration
}

func NewPresence(rtdb *RTDB, interval time.Duration) *Presence {
	if interval <= 0 {
		interval = 30 * time.Second
	}
	return &Presence{
		rtdb:     rtdb,
		interval: interval,
	}
}

// Start sends heartbeats until context is cancelled.
// Also marks all instances as offline on shutdown.
func (p *Presence) Start(ctx context.Context, instanceIDs []string) {
	// Initial heartbeat.
	p.heartbeat(ctx, instanceIDs)

	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			p.heartbeat(ctx, instanceIDs)
		case <-ctx.Done():
			// Mark offline on shutdown.
			p.markOffline(instanceIDs)
			return
		}
	}
}

func (p *Presence) heartbeat(ctx context.Context, instanceIDs []string) {
	now := time.Now().UnixMilli()
	for _, id := range instanceIDs {
		if err := p.rtdb.Update(ctx, "presence/"+id, map[string]interface{}{
			"online":    true,
			"last_seen": now,
		}); err != nil {
			slog.Warn("firebase: presence heartbeat failed", "instance", id, "error", err)
		}
	}
}

func (p *Presence) markOffline(instanceIDs []string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	now := time.Now().UnixMilli()
	for _, id := range instanceIDs {
		p.rtdb.Update(ctx, "presence/"+id, map[string]interface{}{
			"online":    false,
			"last_seen": now,
		})
	}
}

// UpdateInstances can be called when the instance list changes.
func (p *Presence) UpdateInstance(ctx context.Context, instanceID string, online bool) {
	p.rtdb.Update(ctx, "presence/"+instanceID, map[string]interface{}{
		"online":    online,
		"last_seen": time.Now().UnixMilli(),
	})
}
