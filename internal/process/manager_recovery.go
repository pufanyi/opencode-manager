package process

import (
	"fmt"
	"log/slog"
	"time"

	"github.com/pufanyi/opencode-manager/internal/provider"
)

// watchInstance blocks on the provider's Wait() and handles crash recovery.
// For Claude Code, Wait() returns nil immediately — no crash recovery needed.
func (m *Manager) watchInstance(inst *Instance, restartCount int) {
	err := inst.Provider.Wait()
	if err == nil {
		// Clean exit or no persistent process (Claude Code) — nothing to do.
		return
	}

	if m.ctx.Err() != nil {
		return
	}

	if inst.Status() == StatusStopped {
		return
	}

	slog.Error("instance crashed", "name", inst.Name, "error", err, "restarts", restartCount, "stderr", inst.Provider.Stderr())
	inst.SetStatus(StatusFailed)
	_ = m.store.UpdateInstanceStatus(inst.ID, string(StatusFailed))

	if restartCount >= m.maxRestarts {
		slog.Error("instance exceeded max restarts", "name", inst.Name)
		if m.onCrash != nil {
			m.onCrash(inst, fmt.Errorf("exceeded max restarts (%d): %w", m.maxRestarts, err))
		}
		if inst.ProviderType == provider.TypeOpenCode {
			m.portPool.Release(inst.Port)
		}
		return
	}

	delay := time.Duration(1<<uint(restartCount)) * time.Second
	slog.Info("restarting instance", "name", inst.Name, "delay", delay)

	select {
	case <-time.After(delay):
	case <-m.ctx.Done():
		return
	}

	// Re-allocate port for OpenCode instances
	if inst.ProviderType == provider.TypeOpenCode {
		m.portPool.Release(inst.Port)
		port, err2 := m.portPool.Allocate()
		if err2 != nil {
			slog.Error("failed to allocate port for restart", "name", inst.Name, "error", err2)
			if m.onCrash != nil {
				m.onCrash(inst, fmt.Errorf("port allocation failed: %w", err2))
			}
			return
		}
		inst.Port = port
		inst.Provider.SetPort(port)
		_ = m.store.UpdateInstancePort(inst.ID, port)
	}

	if err := inst.Provider.Start(m.ctx); err != nil {
		slog.Error("failed to restart instance", "name", inst.Name, "error", err)
		if m.onCrash != nil {
			m.onCrash(inst, fmt.Errorf("restart failed: %w", err))
		}
		if inst.ProviderType == provider.TypeOpenCode {
			m.portPool.Release(inst.Port)
		}
		return
	}

	inst.SetStatus(StatusRunning)
	_ = m.store.UpdateInstanceStatus(inst.ID, string(StatusRunning))

	// Wait for ready again
	go func() {
		if err := inst.Provider.WaitReady(m.ctx, 60*time.Second); err != nil && m.ctx.Err() == nil {
			slog.Error("restarted instance not ready", "name", inst.Name, "error", err)
		}
	}()

	go m.watchInstance(inst, restartCount+1)
}

func (m *Manager) StartHealthChecks() {
	if m.healthInterval <= 0 {
		return
	}
	go func() {
		ticker := time.NewTicker(m.healthInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				m.mu.RLock()
				for _, inst := range m.instances {
					if inst.Status() == StatusRunning {
						if err := inst.Provider.HealthCheck(m.ctx); err != nil {
							slog.Warn("health check failed", "name", inst.Name, "error", err)
						}
					}
				}
				m.mu.RUnlock()
			case <-m.ctx.Done():
				return
			}
		}
	}()
}

func (m *Manager) RestoreInstances() error {
	instances, err := m.store.GetRunningInstances()
	if err != nil {
		return err
	}

	for _, dbInst := range instances {
		// Only restore instances owned by this client.
		if dbInst.ClientID != "" && dbInst.ClientID != m.clientID {
			continue
		}

		provType := provider.Type(dbInst.ProviderType)
		var port int

		if provType == provider.TypeOpenCode {
			var err error
			port, err = m.portPool.Allocate()
			if err != nil {
				slog.Error("failed to allocate port for restored instance", "name", dbInst.Name, "error", err)
				continue
			}
			_ = m.store.UpdateInstancePort(dbInst.ID, port)
		}

		prov := m.createProvider(provType, dbInst.Directory, port, dbInst.Password, dbInst.ID)

		inst := &Instance{
			ID:           dbInst.ID,
			Name:         dbInst.Name,
			Directory:    dbInst.Directory,
			Port:         port,
			Password:     dbInst.Password,
			ClientID:     dbInst.ClientID,
			ProviderType: provType,
			Provider:     prov,
			status:       StatusStopped,
		}

		m.mu.Lock()
		m.instances[inst.ID] = inst
		m.mu.Unlock()

		if err := m.startInstance(inst); err != nil {
			slog.Error("failed to restore instance", "name", inst.Name, "error", err)
			_ = m.store.UpdateInstanceStatus(inst.ID, string(StatusFailed))
		}
	}

	return nil
}

func (m *Manager) LoadStopped() error {
	all, err := m.store.ListInstances()
	if err != nil {
		return err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	for _, dbInst := range all {
		if _, exists := m.instances[dbInst.ID]; exists {
			continue
		}
		// Only load instances owned by this client.
		if dbInst.ClientID != "" && dbInst.ClientID != m.clientID {
			continue
		}
		provType := provider.Type(dbInst.ProviderType)
		prov := m.createProvider(provType, dbInst.Directory, dbInst.Port, dbInst.Password, dbInst.ID)
		m.instances[dbInst.ID] = &Instance{
			ID:           dbInst.ID,
			Name:         dbInst.Name,
			Directory:    dbInst.Directory,
			Port:         dbInst.Port,
			Password:     dbInst.Password,
			ClientID:     dbInst.ClientID,
			ProviderType: provType,
			Provider:     prov,
			status:       InstanceStatus(dbInst.Status),
		}
	}
	return nil
}
