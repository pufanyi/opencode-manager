package process

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/pufanyi/opencode-manager/internal/provider"
	"github.com/pufanyi/opencode-manager/internal/store"
)

type CrashCallback func(inst *Instance, err error)

type Manager struct {
	mu             sync.RWMutex
	instances      map[string]*Instance // keyed by ID
	opencodeBinary string
	claudeBinary   string
	portPool       *PortPool
	store          *store.Store
	healthInterval time.Duration
	maxRestarts    int
	onCrash        CrashCallback

	ctx    context.Context
	cancel context.CancelFunc
}

func NewManager(ctx context.Context, opencodeBinary, claudeBinary string, portPool *PortPool, st *store.Store, healthInterval time.Duration, maxRestarts int) *Manager {
	mCtx, cancel := context.WithCancel(ctx)
	return &Manager{
		instances:      make(map[string]*Instance),
		opencodeBinary: opencodeBinary,
		claudeBinary:   claudeBinary,
		portPool:       portPool,
		store:          st,
		healthInterval: healthInterval,
		maxRestarts:    maxRestarts,
		ctx:            mCtx,
		cancel:         cancel,
	}
}

func (m *Manager) SetCrashCallback(cb CrashCallback) {
	m.onCrash = cb
}

func (m *Manager) CreateAndStart(name, directory string, autoStart bool, providerType provider.Type) (*Instance, error) {
	if providerType == "" {
		providerType = provider.TypeOpenCode
	}

	id := uuid.New().String()
	var port int
	var password string

	if providerType == provider.TypeOpenCode {
		var err error
		port, err = m.portPool.Allocate()
		if err != nil {
			return nil, err
		}
		password, err = generatePassword()
		if err != nil {
			m.portPool.Release(port)
			return nil, err
		}
	}

	prov := m.createProvider(providerType, directory, port, password, id)

	inst := &Instance{
		ID:           id,
		Name:         name,
		Directory:    directory,
		Port:         port,
		Password:     password,
		ProviderType: providerType,
		Provider:     prov,
		status:       StatusStopped,
	}

	if err := m.store.CreateInstance(&store.Instance{
		ID:           id,
		Name:         name,
		Directory:    directory,
		Port:         port,
		Password:     password,
		Status:       string(StatusStopped),
		AutoStart:    autoStart,
		ProviderType: string(providerType),
	}); err != nil {
		if providerType == provider.TypeOpenCode {
			m.portPool.Release(port)
		}
		return nil, fmt.Errorf("saving instance: %w", err)
	}

	m.mu.Lock()
	m.instances[id] = inst
	m.mu.Unlock()

	if err := m.startInstance(inst); err != nil {
		return nil, err
	}

	return inst, nil
}

func (m *Manager) createProvider(provType provider.Type, dir string, port int, password, instanceID string) provider.Provider {
	switch provType {
	case provider.TypeClaudeCode:
		return provider.NewClaudeCodeProvider(m.claudeBinary, dir, m.store, instanceID)
	default:
		return provider.NewOpenCodeProvider(m.opencodeBinary, dir, port, password)
	}
}

func (m *Manager) StartInstance(id string) error {
	m.mu.RLock()
	inst, ok := m.instances[id]
	m.mu.RUnlock()

	if !ok {
		return fmt.Errorf("instance %s not found", id)
	}

	if inst.ProviderType == provider.TypeOpenCode {
		port, err := m.portPool.Allocate()
		if err != nil {
			return err
		}
		inst.Port = port
		_ = m.store.UpdateInstancePort(id, port)
		if ocp, ok := inst.Provider.(*provider.OpenCodeProvider); ok {
			ocp.SetPort(port)
		}
	}

	return m.startInstance(inst)
}

func (m *Manager) startInstance(inst *Instance) error {
	inst.SetStatus(StatusStarting)

	if err := inst.Provider.Start(m.ctx); err != nil {
		inst.SetStatus(StatusFailed)
		return err
	}

	inst.SetStatus(StatusRunning)
	_ = m.store.UpdateInstanceStatus(inst.ID, string(StatusRunning))

	// For OpenCode: wait for ready and watch for crashes
	if ocp, ok := inst.Provider.(*provider.OpenCodeProvider); ok {
		go func() {
			slog.Info("waiting for instance to be ready", "name", inst.Name)
			if err := ocp.WaitReady(m.ctx, 60*time.Second); err != nil {
				if m.ctx.Err() == nil {
					slog.Error("instance not ready", "name", inst.Name, "error", err)
				}
				return
			}
			slog.Info("instance ready", "name", inst.Name)
		}()

		go m.watchOpenCodeInstance(inst, ocp, 0)
	}

	return nil
}

func (m *Manager) watchOpenCodeInstance(inst *Instance, ocp *provider.OpenCodeProvider, restartCount int) {
	err := ocp.Wait()
	if err == nil {
		return
	}

	if m.ctx.Err() != nil {
		return
	}

	if inst.Status() == StatusStopped {
		return
	}

	slog.Error("instance crashed", "name", inst.Name, "error", err, "restarts", restartCount, "stderr", ocp.Stderr())
	inst.SetStatus(StatusFailed)
	_ = m.store.UpdateInstanceStatus(inst.ID, string(StatusFailed))

	if restartCount >= m.maxRestarts {
		slog.Error("instance exceeded max restarts", "name", inst.Name)
		if m.onCrash != nil {
			m.onCrash(inst, fmt.Errorf("exceeded max restarts (%d): %w", m.maxRestarts, err))
		}
		m.portPool.Release(inst.Port)
		return
	}

	delay := time.Duration(1<<uint(restartCount)) * time.Second
	slog.Info("restarting instance", "name", inst.Name, "delay", delay)

	select {
	case <-time.After(delay):
	case <-m.ctx.Done():
		return
	}

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
	ocp.SetPort(port)
	_ = m.store.UpdateInstancePort(inst.ID, port)

	if err := inst.Provider.Start(m.ctx); err != nil {
		slog.Error("failed to restart instance", "name", inst.Name, "error", err)
		if m.onCrash != nil {
			m.onCrash(inst, fmt.Errorf("restart failed: %w", err))
		}
		m.portPool.Release(inst.Port)
		return
	}

	inst.SetStatus(StatusRunning)
	_ = m.store.UpdateInstanceStatus(inst.ID, string(StatusRunning))

	// Wait for ready again
	go func() {
		if err := ocp.WaitReady(m.ctx, 60*time.Second); err != nil && m.ctx.Err() == nil {
			slog.Error("restarted instance not ready", "name", inst.Name, "error", err)
		}
	}()

	go m.watchOpenCodeInstance(inst, ocp, restartCount+1)
}

func (m *Manager) StopInstance(id string) error {
	m.mu.RLock()
	inst, ok := m.instances[id]
	m.mu.RUnlock()

	if !ok {
		return fmt.Errorf("instance %s not found", id)
	}

	inst.SetStatus(StatusStopped)

	if err := inst.Provider.Stop(); err != nil {
		return err
	}

	if inst.ProviderType == provider.TypeOpenCode {
		m.portPool.Release(inst.Port)
	}
	_ = m.store.UpdateInstanceStatus(id, string(StatusStopped))
	return nil
}

func (m *Manager) DeleteInstance(id string) error {
	m.mu.Lock()
	inst, ok := m.instances[id]
	if ok {
		delete(m.instances, id)
	}
	m.mu.Unlock()

	if ok && inst.Status() == StatusRunning {
		_ = inst.Provider.Stop()
		if inst.ProviderType == provider.TypeOpenCode {
			m.portPool.Release(inst.Port)
		}
	}

	return m.store.DeleteInstance(id)
}

func (m *Manager) GetInstance(id string) *Instance {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.instances[id]
}

func (m *Manager) GetInstanceByName(name string) *Instance {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, inst := range m.instances {
		if inst.Name == name {
			return inst
		}
	}
	return nil
}

func (m *Manager) ListInstances() []*Instance {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make([]*Instance, 0, len(m.instances))
	for _, inst := range m.instances {
		result = append(result, inst)
	}
	return result
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
		provType := provider.Type(dbInst.ProviderType)
		prov := m.createProvider(provType, dbInst.Directory, dbInst.Port, dbInst.Password, dbInst.ID)
		m.instances[dbInst.ID] = &Instance{
			ID:           dbInst.ID,
			Name:         dbInst.Name,
			Directory:    dbInst.Directory,
			Port:         dbInst.Port,
			Password:     dbInst.Password,
			ProviderType: provType,
			Provider:     prov,
			status:       InstanceStatus(dbInst.Status),
		}
	}
	return nil
}

func (m *Manager) Shutdown() {
	m.cancel()

	m.mu.RLock()
	defer m.mu.RUnlock()

	for _, inst := range m.instances {
		if inst.Status() == StatusRunning {
			_ = inst.Provider.Stop()
		}
	}
}

func generatePassword() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generating password: %w", err)
	}
	return hex.EncodeToString(b), nil
}
