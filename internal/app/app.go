package app

import (
	"context"
	"log/slog"

	"github.com/pufanyi/opencode-manager/internal/bot"
	"github.com/pufanyi/opencode-manager/internal/config"
	"github.com/pufanyi/opencode-manager/internal/process"
	"github.com/pufanyi/opencode-manager/internal/provider"
	"github.com/pufanyi/opencode-manager/internal/store"
)

type App struct {
	cfg     *config.Config
	store   *store.Store
	procMgr *process.Manager
	bot     *bot.Bot
}

func New(cfg *config.Config) (*App, error) {
	st, err := store.New(cfg.Storage.Database)
	if err != nil {
		return nil, err
	}

	portPool := process.NewPortPool(cfg.Process.PortRange.Start, cfg.Process.PortRange.End)
	procMgr := process.NewManager(
		context.Background(),
		cfg.Process.OpencodeBinary,
		cfg.Process.ClaudeCodeBinary,
		portPool,
		st,
		cfg.Process.HealthCheckInterval,
		cfg.Process.MaxRestartAttempts,
	)

	tgBot, err := bot.New(&cfg.Telegram, procMgr, st)
	if err != nil {
		st.Close()
		return nil, err
	}

	procMgr.SetCrashCallback(func(inst *process.Instance, err error) {
		tgBot.NotifyCrash(inst.Name, err)
	})

	return &App{
		cfg:     cfg,
		store:   st,
		procMgr: procMgr,
		bot:     tgBot,
	}, nil
}

func (a *App) Start(ctx context.Context) error {
	if err := a.procMgr.RestoreInstances(); err != nil {
		slog.Error("failed to restore instances", "error", err)
	}

	if err := a.procMgr.LoadStopped(); err != nil {
		slog.Error("failed to load stopped instances", "error", err)
	}

	for _, proj := range a.cfg.Projects {
		provType := provider.Type(proj.Provider)
		if provType == "" {
			provType = provider.TypeOpenCode
		}

		existing := a.procMgr.GetInstanceByName(proj.Name)
		if existing != nil {
			if proj.AutoStart && existing.Status() != process.StatusRunning {
				if err := a.procMgr.StartInstance(existing.ID); err != nil {
					slog.Error("failed to auto-start project", "name", proj.Name, "error", err)
				}
			}
			continue
		}

		_, err := a.procMgr.CreateAndStart(proj.Name, proj.Directory, proj.AutoStart, provType)
		if err != nil {
			slog.Error("failed to create project", "name", proj.Name, "error", err)
		}
	}

	a.procMgr.StartHealthChecks()
	a.bot.Start(ctx)

	return nil
}

func (a *App) Shutdown() {
	slog.Info("shutting down")
	a.bot.Stop()
	a.procMgr.Shutdown()
	a.store.Close()
}
