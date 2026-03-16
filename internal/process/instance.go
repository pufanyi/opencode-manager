package process

import (
	"sync"

	"github.com/pufanyi/opencode-manager/internal/provider"
)

type InstanceStatus string

const (
	StatusRunning  InstanceStatus = "running"
	StatusStopped  InstanceStatus = "stopped"
	StatusStarting InstanceStatus = "starting"
	StatusFailed   InstanceStatus = "failed"
)

type Instance struct {
	ID           string
	Name         string
	Directory    string
	Port         int
	Password     string
	ProviderType provider.Type
	Provider     provider.Provider

	mu     sync.Mutex
	status InstanceStatus
}

func (i *Instance) Status() InstanceStatus {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.status
}

func (i *Instance) SetStatus(s InstanceStatus) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.status = s
}
