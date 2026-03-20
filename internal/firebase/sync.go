package firebase

import (
	"context"
	"log/slog"
	"time"
)

// InstanceInfo is the instance data synced to RTDB for the web frontend.
type InstanceInfo struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Directory    string `json:"directory"`
	Status       string `json:"status"`
	ProviderType string `json:"provider_type"`
}

// InstanceLister returns the current instance list.
type InstanceLister func() []InstanceInfo

// StartInstanceSync periodically syncs the instance list to RTDB.
func (c *Client) StartInstanceSync(ctx context.Context, lister InstanceLister, interval time.Duration) {
	if interval <= 0 {
		interval = 2 * time.Second
	}

	// Initial sync.
	c.syncInstances(ctx, lister)

	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				c.syncInstances(ctx, lister)
			case <-ctx.Done():
				return
			}
		}
	}()
}

func (c *Client) syncInstances(ctx context.Context, lister InstanceLister) {
	instances := lister()
	data := make(map[string]interface{}, len(instances))
	for _, inst := range instances {
		data[inst.ID] = map[string]interface{}{
			"id":            inst.ID,
			"name":          inst.Name,
			"directory":     inst.Directory,
			"status":        inst.Status,
			"provider_type": inst.ProviderType,
		}
	}
	if err := c.RTDB.Set(ctx, "instances", data); err != nil {
		slog.Warn("firebase: instance sync failed", "error", err)
	}
}
