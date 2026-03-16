package app

import (
	"context"
	"log/slog"

	"github.com/pufanyi/opencode-manager/internal/bot"
	"github.com/pufanyi/opencode-manager/internal/config"
	"github.com/pufanyi/opencode-manager/internal/process"
	"github.com/pufanyi/opencode-manager/internal/store"
)

type App struct {
	cfg     *config.Config
	store   *store.Store
	procMgr *process.Manager
	bot     *bot.Bot
}

func New(cfg *config.Config) (*App, error) {
	// Initialize store
	st, err := store.New(cfg.Storage.Database)
	if err != nil {
		return nil, err
	}

	// Initialize process manager
	portPool := process.NewPortPool(cfg.Process.PortRange.Start, cfg.Process.PortRange.End)
	procMgr := process.NewManager(
		context.Background(),
		cfg.Process.OpencodeBinary,
		portPool,
		st,
		cfg.Process.HealthCheckInterval,
		cfg.Process.MaxRestartAttempts,
	)

	// Initialize Telegram bot
	tgBot, err := bot.New(&cfg.Telegram, procMgr, st)
	if err != nil {
		st.Close()
		return nil, err
	}

	// Set crash callback to notify via Telegram
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
	// Restore instances from DB
	if err := a.procMgr.RestoreInstances(); err != nil {
		slog.Error("failed to restore instances", "error", err)
	}

	// Load stopped instances so they appear in list
	if err := a.procMgr.LoadStopped(); err != nil {
		slog.Error("failed to load stopped instances", "error", err)
	}

	// Pre-register projects from config
	for _, proj := range a.cfg.Projects {
		existing := a.procMgr.GetInstanceByName(proj.Name)
		if existing != nil {
			if proj.AutoStart && existing.Status() != process.StatusRunning {
				if err := a.procMgr.StartInstance(existing.ID); err != nil {
					slog.Error("failed to auto-start project", "name", proj.Name, "error", err)
				}
			}
			continue
		}

		_, err := a.procMgr.CreateAndStart(proj.Name, proj.Directory, proj.AutoStart)
		if err != nil {
			slog.Error("failed to create project", "name", proj.Name, "error", err)
		}
	}

	// Start health checks
	a.procMgr.StartHealthChecks()

	// Start bot (blocking)
	a.bot.Start(ctx)

	return nil
}

func (a *App) Shutdown() {
	slog.Info("shutting down")
	a.bot.Stop()
	a.procMgr.Shutdown()
	a.store.Close()
}
