package app

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/pufanyi/opencode-manager/internal/bot"
	"github.com/pufanyi/opencode-manager/internal/config"
	"github.com/pufanyi/opencode-manager/internal/firebase"
	"github.com/pufanyi/opencode-manager/internal/process"
	"github.com/pufanyi/opencode-manager/internal/provider"
	"github.com/pufanyi/opencode-manager/internal/store"
	"github.com/pufanyi/opencode-manager/internal/web"
)

type App struct {
	cfg      *config.Config
	store    *store.Store
	procMgr  *process.Manager
	bot      *bot.Bot
	web      *web.Server
	firebase *firebase.Client
	devMode  bool
}

func New(cfg *config.Config, st *store.Store, devMode bool) (*App, error) {
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
		devMode: devMode,
	}

	// Firebase integration (optional).
	if cfg.Firebase.Enabled {
		fbClient, err := firebase.NewClient(firebase.Config{
			APIKey:      cfg.Firebase.APIKey,
			DatabaseURL: cfg.Firebase.DatabaseURL,
			Email:       cfg.Firebase.Email,
			Password:    cfg.Firebase.Password,
		})
		if err != nil {
			slog.Error("firebase initialization failed (continuing without)", "error", err)
		} else {
			app.firebase = fbClient
			procMgr.SetFirebaseStreamer(fbClient.Streamer)
		}
	}

	// In dev mode, force-enable web dashboard
	if devMode && !cfg.Web.Enabled {
		cfg.Web.Enabled = true
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

	a.procMgr.StartHealthChecks()

	// Start Firebase background services.
	if a.firebase != nil {
		var instanceIDs []string
		for _, inst := range a.procMgr.ListInstances() {
			instanceIDs = append(instanceIDs, inst.ID)
		}

		// Command handler for web frontend.
		a.firebase.SetCommandHandler(a.handleFirebaseCommand)

		a.firebase.StartBackground(ctx, instanceIDs)

		// Sync instance list to RTDB periodically.
		a.firebase.StartInstanceSync(ctx, func() []firebase.InstanceInfo {
			instances := a.procMgr.ListInstances()
			result := make([]firebase.InstanceInfo, len(instances))
			for i, inst := range instances {
				result[i] = firebase.InstanceInfo{
					ID:           inst.ID,
					Name:         inst.Name,
					Directory:    inst.Directory,
					Status:       string(inst.Status()),
					ProviderType: string(inst.ProviderType),
				}
			}
			return result
		}, 2*time.Second)
	}

	// Start web dashboard
	if a.web != nil {
		if a.devMode {
			dp, err := web.StartDevProxy("web")
			if err != nil {
				return fmt.Errorf("starting angular dev server: %w", err)
			}
			a.web.SetDevProxy(dp)
		}
		if err := a.web.Start(ctx); err != nil {
			slog.Error("failed to start web dashboard", "error", err)
		}
	}

	// Start bot (blocking)
	a.bot.Start(ctx)

	return nil
}

// handleFirebaseCommand dispatches commands from the web frontend.
func (a *App) handleFirebaseCommand(ctx context.Context, instanceID, commandID string, cmd firebase.Command) (interface{}, error) {
	switch cmd.Action {
	case "create":
		var p struct {
			Name      string `json:"name"`
			Directory string `json:"directory"`
			Provider  string `json:"provider"`
		}
		if err := json.Unmarshal(cmd.Payload, &p); err != nil {
			return nil, fmt.Errorf("invalid create payload: %w", err)
		}
		provType := provider.Type(p.Provider)
		if provType == "" {
			provType = provider.TypeClaudeCode
		}
		inst, err := a.procMgr.CreateAndStart(p.Name, p.Directory, false, provType)
		if err != nil {
			return nil, err
		}
		return map[string]string{"id": inst.ID, "status": "created"}, nil

	case "start":
		if err := a.procMgr.StartInstance(instanceID); err != nil {
			return nil, err
		}
		return map[string]string{"status": "started"}, nil

	case "stop":
		if err := a.procMgr.StopInstance(instanceID); err != nil {
			return nil, err
		}
		return map[string]string{"status": "stopped"}, nil

	case "delete":
		if err := a.procMgr.DeleteInstance(instanceID); err != nil {
			return nil, err
		}
		return map[string]string{"status": "deleted"}, nil

	case "prompt":
		var p firebase.PromptPayload
		if err := json.Unmarshal(cmd.Payload, &p); err != nil {
			return nil, fmt.Errorf("invalid prompt payload: %w", err)
		}
		inst := a.procMgr.GetInstance(instanceID)
		if inst == nil {
			return nil, fmt.Errorf("instance not found")
		}
		ch, err := inst.Provider.Prompt(ctx, p.SessionID, p.Content)
		if err != nil {
			return nil, err
		}
		// Wrap with Firebase streaming.
		ch = a.procMgr.WrapEventsIfFirebase(p.SessionID, ch)
		// Consume events (streaming happens via the wrapper).
		go func() {
			for range ch {
			}
		}()
		return map[string]string{"status": "started"}, nil

	case "create_session":
		inst := a.procMgr.GetInstance(instanceID)
		if inst == nil {
			return nil, fmt.Errorf("instance not found")
		}
		session, err := inst.Provider.CreateSession(ctx, nil)
		if err != nil {
			return nil, err
		}
		return session, nil

	case "list_sessions":
		inst := a.procMgr.GetInstance(instanceID)
		if inst == nil {
			return nil, fmt.Errorf("instance not found")
		}
		sessions, err := inst.Provider.ListSessions(ctx)
		if err != nil {
			return nil, err
		}
		return sessions, nil

	default:
		return nil, fmt.Errorf("unknown action: %s", cmd.Action)
	}
}

func (a *App) Shutdown() {
	slog.Info("shutting down")
	a.bot.Stop()
	if a.web != nil {
		a.web.Stop()
	}
	a.procMgr.Shutdown()
	// Note: store is closed by main, not by app
}
