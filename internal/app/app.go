package app

import (
	"context"
	"log/slog"

	"github.com/pufanyi/opencode-manager/internal/bot"
	"github.com/pufanyi/opencode-manager/internal/config"
	"github.com/pufanyi/opencode-manager/internal/process"
	"github.com/pufanyi/opencode-manager/internal/provider"
	"github.com/pufanyi/opencode-manager/internal/store"
	"github.com/pufanyi/opencode-manager/internal/web"
)

type App struct {
	cfg     *config.Config
	store   *store.Store
	procMgr *process.Manager
	bot     *bot.Bot
	web     *web.Server
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

	app := &App{
		cfg:     cfg,
		store:   st,
		procMgr: procMgr,
		bot:     tgBot,
	}

	// Web dashboard
	if cfg.Web.Enabled {
		addr := cfg.Web.Addr
		if addr == "" {
			addr = ":8080"
		}
		app.web = web.NewServer(addr, procMgr, st)
	}

	return app, nil
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
			provType = provider.TypeClaudeCode
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

	// Start web dashboard
	if a.web != nil {
		if err := a.web.Start(ctx); err != nil {
			slog.Error("failed to start web dashboard", "error", err)
		}
	}

	// Start bot (blocking)
	a.bot.Start(ctx)

	return nil
}

func (a *App) Shutdown() {
	slog.Info("shutting down")
	a.bot.Stop()
	if a.web != nil {
		a.web.Stop()
	}
	a.procMgr.Shutdown()
	a.store.Close()
}
