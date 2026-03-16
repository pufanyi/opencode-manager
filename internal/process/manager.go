package process

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/pufanyi/opencode-manager/internal/store"
)

type CrashCallback func(inst *Instance, err error)

type Manager struct {
	mu       sync.RWMutex
	instances map[string]*Instance // keyed by ID
	binary   string
	portPool *PortPool
	store    *store.Store
	healthInterval time.Duration
	maxRestarts    int
	onCrash  CrashCallback

	ctx    context.Context
	cancel context.CancelFunc
}

func NewManager(ctx context.Context, binary string, portPool *PortPool, st *store.Store, healthInterval time.Duration, maxRestarts int) *Manager {
	mCtx, cancel := context.WithCancel(ctx)
	return &Manager{
		instances:      make(map[string]*Instance),
		binary:         binary,
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

func (m *Manager) CreateAndStart(name, directory string, autoStart bool) (*Instance, error) {
	port, err := m.portPool.Allocate()
	if err != nil {
		return nil, err
	}

	password := generatePassword()
	id := uuid.New().String()

	inst := &Instance{
		ID:        id,
		Name:      name,
		Directory: directory,
		Port:      port,
		Password:  password,
		status:    StatusStopped,
	}

	// Save to DB
	if err := m.store.CreateInstance(&store.Instance{
		ID:        id,
		Name:      name,
		Directory: directory,
		Port:      port,
		Password:  password,
		Status:    string(StatusStopped),
		AutoStart: autoStart,
	}); err != nil {
		m.portPool.Release(port)
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

func (m *Manager) StartInstance(id string) error {
	m.mu.RLock()
	inst, ok := m.instances[id]
	m.mu.RUnlock()

	if !ok {
		return fmt.Errorf("instance %s not found", id)
	}

	// Reallocate port if needed
	if inst.Status() == StatusStopped {
		port, err := m.portPool.Allocate()
		if err != nil {
			return err
		}
		inst.Port = port
		_ = m.store.UpdateInstancePort(id, port)
	}

	return m.startInstance(inst)
}

func (m *Manager) startInstance(inst *Instance) error {
	if err := inst.Start(m.ctx, m.binary); err != nil {
		return err
	}

	_ = m.store.UpdateInstanceStatus(inst.ID, string(StatusRunning))

	// Monitor for crashes
	go m.watchInstance(inst, 0)

	return nil
}

func (m *Manager) watchInstance(inst *Instance, restartCount int) {
	err := inst.Wait()
	if err == nil {
		return // Graceful shutdown
	}

	// Check if context was cancelled (intentional stop)
	if m.ctx.Err() != nil {
		return
	}

	if inst.Status() == StatusStopped {
		return // Was stopped intentionally
	}

	slog.Error("instance crashed", "name", inst.Name, "error", err, "restarts", restartCount)
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

	// Exponential backoff
	delay := time.Duration(1<<uint(restartCount)) * time.Second
	slog.Info("restarting instance", "name", inst.Name, "delay", delay)

	select {
	case <-time.After(delay):
	case <-m.ctx.Done():
		return
	}

	// Reallocate port for restart
	m.portPool.Release(inst.Port)
	port, err2 := m.portPool.Allocate()
	if err2 != nil {
		slog.Error("failed to allocate port for restart", "name", inst.Name, "error", err2)
		if m.onCrash != nil {
			m.onCrash(inst, fmt.Errorf("port allocation failed during restart: %w", err2))
		}
		return
	}
	inst.Port = port
	_ = m.store.UpdateInstancePort(inst.ID, port)

	if err := inst.Start(m.ctx, m.binary); err != nil {
		slog.Error("failed to restart instance", "name", inst.Name, "error", err)
		if m.onCrash != nil {
			m.onCrash(inst, fmt.Errorf("restart failed: %w", err))
		}
		m.portPool.Release(inst.Port)
		return
	}

	_ = m.store.UpdateInstanceStatus(inst.ID, string(StatusRunning))
	go m.watchInstance(inst, restartCount+1)
}

func (m *Manager) StopInstance(id string) error {
	m.mu.RLock()
	inst, ok := m.instances[id]
	m.mu.RUnlock()

	if !ok {
		return fmt.Errorf("instance %s not found", id)
	}

	if err := inst.Stop(); err != nil {
		return err
	}

	m.portPool.Release(inst.Port)
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
		_ = inst.Stop()
		m.portPool.Release(inst.Port)
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

func (m *Manager) HealthCheck(inst *Instance) bool {
	client := &http.Client{Timeout: 5 * time.Second}
	req, err := http.NewRequest("GET", inst.BaseURL()+"/", nil)
	if err != nil {
		return false
	}
	req.SetBasicAuth("", inst.Password)

	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
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
						if !m.HealthCheck(inst) {
							slog.Warn("health check failed", "name", inst.Name)
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

// RestoreInstances loads instances from DB and restarts those that should be running.
func (m *Manager) RestoreInstances() error {
	instances, err := m.store.GetRunningInstances()
	if err != nil {
		return err
	}

	for _, dbInst := range instances {
		inst := &Instance{
			ID:        dbInst.ID,
			Name:      dbInst.Name,
			Directory: dbInst.Directory,
			Port:      dbInst.Port,
			Password:  dbInst.Password,
			status:    StatusStopped,
		}

		m.mu.Lock()
		m.instances[inst.ID] = inst
		m.mu.Unlock()

		// Allocate a new port for restart
		port, err := m.portPool.Allocate()
		if err != nil {
			slog.Error("failed to allocate port for restored instance", "name", inst.Name, "error", err)
			continue
		}
		inst.Port = port
		_ = m.store.UpdateInstancePort(inst.ID, port)

		if err := m.startInstance(inst); err != nil {
			slog.Error("failed to restore instance", "name", inst.Name, "error", err)
			_ = m.store.UpdateInstanceStatus(inst.ID, string(StatusFailed))
		}
	}

	return nil
}

// LoadStopped loads stopped instances from DB so they appear in list commands.
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
		m.instances[dbInst.ID] = &Instance{
			ID:        dbInst.ID,
			Name:      dbInst.Name,
			Directory: dbInst.Directory,
			Port:      dbInst.Port,
			Password:  dbInst.Password,
			status:    InstanceStatus(dbInst.Status),
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
			_ = inst.Stop()
		}
	}
}

func generatePassword() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
