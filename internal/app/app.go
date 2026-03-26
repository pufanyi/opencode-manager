package app

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
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
	store    store.Store
	procMgr  *process.Manager
	bot      *bot.Bot
	web      *web.Server
	firebase *firebase.Client
	devMode  bool
}

func New(cfg *config.Config, st store.Store, fbClient *firebase.Client, devMode bool) (*App, error) {
	portPool := process.NewPortPool(cfg.Process.PortRange.Start, cfg.Process.PortRange.End)
	procMgr := process.NewManager(
		context.Background(),
		fbClient.ClientID(),
		cfg.Process.OpencodeBinary,
		cfg.Process.ClaudeCodeBinary,
		portPool,
		st,
		cfg.Process.HealthCheckInterval,
		cfg.Process.MaxRestartAttempts,
	)

	app := &App{
		cfg:      cfg,
		store:    st,
		procMgr:  procMgr,
		firebase: fbClient,
		devMode:  devMode,
	}

	// Firebase streamer for real-time events.
	procMgr.SetFirebaseStreamer(fbClient.Streamer)

	// Telegram bot (optional — failure does not block startup).
	if cfg.TelegramReady() {
		tgState := firebase.NewTelegramState(fbClient.RTDB, fbClient.UID())
		tgBot, err := bot.New(&cfg.Telegram, procMgr, st, tgState)
		if err != nil {
			slog.Warn("telegram bot unavailable, web dashboard will still work", "error", err)
		} else {
			app.bot = tgBot
			app.bot.SetFirebase(fbClient)
			procMgr.SetCrashCallback(func(inst *process.Instance, err error) {
				tgBot.NotifyCrash(inst.Name, err)
			})
		}
	} else {
		slog.Info("telegram not configured, skipping bot startup")
	}

	// Web dashboard (always enabled).
	addr := cfg.Web.Addr
	if addr == "" {
		addr = ":8080"
	}
	app.web = web.NewServer(addr, procMgr, st)
	app.web.SetStatusFunc(app.settingsStatus)

	return app, nil
}

func (a *App) Start(ctx context.Context) error {
	// Register this client in Firestore.
	hostname, _ := os.Hostname()
	if err := a.store.RegisterClient(&store.ClientInfo{
		ClientID:  a.firebase.ClientID(),
		Hostname:  hostname,
		StartedAt: time.Now(),
	}); err != nil {
		slog.Warn("failed to register client", "error", err)
	}

	if err := a.procMgr.RestoreInstances(); err != nil {
		slog.Error("failed to restore instances", "error", err)
	}

	if err := a.procMgr.LoadStopped(); err != nil {
		slog.Error("failed to load stopped instances", "error", err)
	}

	a.procMgr.StartHealthChecks()

	// Start Firebase background services.
	var instanceIDs []string
	for _, inst := range a.procMgr.ListInstances() {
		instanceIDs = append(instanceIDs, inst.ID)
	}

	// Command handler for web frontend.
	a.firebase.SetCommandHandler(a.handleFirebaseCommand)

	a.firebase.StartBackground(ctx, instanceIDs)

	// Start web dashboard.
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

	// Start bot (blocking). If no bot, block on context.
	if a.bot != nil {
		a.bot.Start(ctx)
	} else {
		slog.Info("running without telegram bot, web dashboard at " + a.web.Addr())
		<-ctx.Done()
	}

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
		// Ownership check: only process instances owned by this client.
		inst := a.procMgr.GetInstance(instanceID)
		if inst != nil && inst.ClientID != "" && inst.ClientID != a.firebase.ClientID() {
			return nil, fmt.Errorf("instance owned by different client")
		}
		if err := a.procMgr.StartInstance(instanceID); err != nil {
			return nil, err
		}
		return map[string]string{"status": "started"}, nil

	case "stop":
		inst := a.procMgr.GetInstance(instanceID)
		if inst != nil && inst.ClientID != "" && inst.ClientID != a.firebase.ClientID() {
			return nil, fmt.Errorf("instance owned by different client")
		}
		if err := a.procMgr.StopInstance(instanceID); err != nil {
			return nil, err
		}
		return map[string]string{"status": "stopped"}, nil

	case "delete":
		inst := a.procMgr.GetInstance(instanceID)
		if inst != nil && inst.ClientID != "" && inst.ClientID != a.firebase.ClientID() {
			return nil, fmt.Errorf("instance owned by different client")
		}
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
		if inst.ClientID != "" && inst.ClientID != a.firebase.ClientID() {
			return nil, fmt.Errorf("instance owned by different client")
		}
		ch, err := inst.Provider.Prompt(ctx, p.SessionID, p.Content)
		if err != nil {
			return nil, err
		}
		// Wrap with Firebase streaming.
		ch = a.procMgr.WrapEventsIfFirebase(p.SessionID, ch)
		// Consume events, accumulate content, persist on completion.
		go func() {
			var textContent string
			var toolCalls []store.ToolCall
			for evt := range ch {
				switch evt.Type {
				case "text":
					textContent = evt.Text
				case "tool_use":
					toolCalls = appendToolCall(toolCalls, evt)
				}
			}

			// Save user message.
			_ = a.store.SaveMessage(instanceID, p.SessionID, &store.Message{
				Role:    "user",
				Content: p.Content,
			})
			// Save assistant response.
			if textContent != "" || len(toolCalls) > 0 {
				_ = a.store.SaveMessage(instanceID, p.SessionID, &store.Message{
					Role:      "assistant",
					Content:   textContent,
					ToolCalls: toolCalls,
				})
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

// appendToolCall adds or updates a tool call entry from a stream event.
func appendToolCall(calls []store.ToolCall, evt provider.StreamEvent) []store.ToolCall {
	for i, tc := range calls {
		if tc.Name == evt.ToolName && tc.Status == "running" {
			calls[i].Status = evt.ToolState
			if evt.ToolDetail != "" {
				calls[i].Detail = evt.ToolDetail
			}
			return calls
		}
	}
	return append(calls, store.ToolCall{
		Name:   evt.ToolName,
		Status: evt.ToolState,
		Detail: evt.ToolDetail,
	})
}

func (a *App) settingsStatus() map[string]any {
	status := map[string]any{
		"telegram_configured": a.cfg.TelegramReady(),
		"telegram_connected":  a.bot != nil,
	}
	return status
}

func (a *App) Shutdown() {
	slog.Info("shutting down")
	if a.bot != nil {
		a.bot.Stop()
	}
	a.web.Stop()
	a.procMgr.Shutdown()
	// Note: store is closed by main, not by app
}
