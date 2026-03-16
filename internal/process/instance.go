package process

import (
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"sync"
)

type InstanceStatus string

const (
	StatusRunning  InstanceStatus = "running"
	StatusStopped  InstanceStatus = "stopped"
	StatusStarting InstanceStatus = "starting"
	StatusFailed   InstanceStatus = "failed"
)

type Instance struct {
	ID        string
	Name      string
	Directory string
	Port      int
	Password  string

	mu     sync.Mutex
	cmd    *exec.Cmd
	cancel context.CancelFunc
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

func (i *Instance) BaseURL() string {
	return fmt.Sprintf("http://127.0.0.1:%d", i.Port)
}

func (i *Instance) Start(ctx context.Context, binary string) error {
	i.mu.Lock()
	defer i.mu.Unlock()

	if i.status == StatusRunning {
		return fmt.Errorf("instance %s already running", i.Name)
	}

	i.status = StatusStarting

	cmdCtx, cancel := context.WithCancel(ctx)
	i.cancel = cancel

	configJSON := fmt.Sprintf(`{"server":{"port":%d,"hostname":"127.0.0.1"}}`, i.Port)

	cmd := exec.CommandContext(cmdCtx, binary, "serve")
	cmd.Dir = i.Directory
	cmd.Env = append(cmd.Environ(),
		fmt.Sprintf("OPENCODE_CONFIG_CONTENT=%s", configJSON),
		fmt.Sprintf("OPENCODE_SERVER_PASSWORD=%s", i.Password),
	)

	i.cmd = cmd

	if err := cmd.Start(); err != nil {
		i.status = StatusFailed
		cancel()
		return fmt.Errorf("starting opencode serve: %w", err)
	}

	i.status = StatusRunning
	slog.Info("instance started", "name", i.Name, "port", i.Port, "pid", cmd.Process.Pid)

	return nil
}

func (i *Instance) Stop() error {
	i.mu.Lock()
	defer i.mu.Unlock()

	if i.cancel != nil {
		i.cancel()
		i.cancel = nil
	}

	if i.cmd != nil && i.cmd.Process != nil {
		_ = i.cmd.Process.Kill()
		_ = i.cmd.Wait()
		i.cmd = nil
	}

	i.status = StatusStopped
	slog.Info("instance stopped", "name", i.Name)
	return nil
}

func (i *Instance) Wait() error {
	i.mu.Lock()
	cmd := i.cmd
	i.mu.Unlock()

	if cmd == nil {
		return nil
	}
	return cmd.Wait()
}
