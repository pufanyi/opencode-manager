package firebase

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// Presence sends periodic heartbeats to RTDB so the web frontend
// can show whether the Go server and its instances are online.
// Two-level presence:
//   - Client level: users/{uid}/clients/{clientID}/presence
//   - Instance level: users/{uid}/instances/{id}/runtime
type Presence struct {
	rtdb     *RTDB
	uid      string
	clientID string
	interval time.Duration

	mu          sync.Mutex
	instanceIDs map[string]bool // currently tracked instances
}

func NewPresence(rtdb *RTDB, uid, clientID string, interval time.Duration) *Presence {
	if interval <= 0 {
		interval = 30 * time.Second
	}
	return &Presence{
		rtdb:        rtdb,
		uid:         uid,
		clientID:    clientID,
		interval:    interval,
		instanceIDs: make(map[string]bool),
	}
}

// Start sends heartbeats until context is cancelled.
// Also marks client and instances offline on shutdown.
func (p *Presence) Start(ctx context.Context, instanceIDs []string) {
	p.mu.Lock()
	for _, id := range instanceIDs {
		p.instanceIDs[id] = true
	}
	p.mu.Unlock()

	// Initial heartbeat.
	p.heartbeat(ctx)

	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			p.heartbeat(ctx)
		case <-ctx.Done():
			p.markOffline()
			return
		}
	}
}

// AddInstance starts tracking an instance for presence heartbeats.
func (p *Presence) AddInstance(ctx context.Context, instanceID string) {
	p.mu.Lock()
	p.instanceIDs[instanceID] = true
	p.mu.Unlock()

	// Send immediate heartbeat for the new instance.
	now := time.Now().UnixMilli()
	if err := p.rtdb.Update(ctx, InstanceRuntimePath(p.uid, instanceID), map[string]interface{}{
		"online":    true,
		"client_id": p.clientID,
		"last_seen": now,
	}); err != nil {
		slog.Warn("firebase: instance presence failed", "instance", instanceID, "error", err)
	}
}

// RemoveInstance stops tracking an instance and marks it offline.
func (p *Presence) RemoveInstance(ctx context.Context, instanceID string) {
	p.mu.Lock()
	delete(p.instanceIDs, instanceID)
	p.mu.Unlock()

	now := time.Now().UnixMilli()
	if err := p.rtdb.Update(ctx, InstanceRuntimePath(p.uid, instanceID), map[string]interface{}{
		"online":    false,
		"client_id": p.clientID,
		"last_seen": now,
	}); err != nil {
		slog.Warn("firebase: failed to mark instance offline", "instance", instanceID, "error", err)
	}
}

func (p *Presence) heartbeat(ctx context.Context) {
	now := time.Now().UnixMilli()

	// Client-level heartbeat.
	if err := p.rtdb.Update(ctx, ClientPresencePath(p.uid, p.clientID), map[string]interface{}{
		"online":    true,
		"last_seen": now,
	}); err != nil {
		slog.Warn("firebase: client presence heartbeat failed", "error", err)
	}

	// Instance-level heartbeats.
	p.mu.Lock()
	ids := make([]string, 0, len(p.instanceIDs))
	for id := range p.instanceIDs {
		ids = append(ids, id)
	}
	p.mu.Unlock()

	for _, id := range ids {
		if err := p.rtdb.Update(ctx, InstanceRuntimePath(p.uid, id), map[string]interface{}{
			"online":    true,
			"client_id": p.clientID,
			"last_seen": now,
		}); err != nil {
			slog.Warn("firebase: instance presence heartbeat failed", "instance", id, "error", err)
		}
	}
}

func (p *Presence) markOffline() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	now := time.Now().UnixMilli()

	// Mark client offline.
	if err := p.rtdb.Update(ctx, ClientPresencePath(p.uid, p.clientID), map[string]interface{}{
		"online":    false,
		"last_seen": now,
	}); err != nil {
		slog.Warn("firebase: failed to mark client offline", "error", err)
	}

	// Mark instances offline.
	p.mu.Lock()
	ids := make([]string, 0, len(p.instanceIDs))
	for id := range p.instanceIDs {
		ids = append(ids, id)
	}
	p.mu.Unlock()

	for _, id := range ids {
		if err := p.rtdb.Update(ctx, InstanceRuntimePath(p.uid, id), map[string]interface{}{
			"online":    false,
			"client_id": p.clientID,
			"last_seen": now,
		}); err != nil {
			slog.Warn("firebase: failed to mark instance offline", "instance", id, "error", err)
		}
	}
}

// UpdateInstance can be called when an instance status changes.
func (p *Presence) UpdateInstance(ctx context.Context, instanceID string, online bool) {
	if err := p.rtdb.Update(ctx, InstanceRuntimePath(p.uid, instanceID), map[string]interface{}{
		"online":    online,
		"client_id": p.clientID,
		"last_seen": time.Now().UnixMilli(),
	}); err != nil {
		slog.Warn("firebase: failed to update presence", "instance", instanceID, "error", err)
	}
}
