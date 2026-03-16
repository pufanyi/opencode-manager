package bot

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/pufanyi/opencode-manager/internal/opencode"
	"github.com/pufanyi/opencode-manager/internal/process"
	"github.com/pufanyi/opencode-manager/internal/store"
)

type Handlers struct {
	procMgr       *process.Manager
	store         *store.Store
	streamMgr     *StreamManager
	sseSubscribers map[string]context.CancelFunc // instance ID -> SSE cancel
}

func NewHandlers(procMgr *process.Manager, st *store.Store, streamMgr *StreamManager) *Handlers {
	return &Handlers{
		procMgr:        procMgr,
		store:          st,
		streamMgr:      streamMgr,
		sseSubscribers: make(map[string]context.CancelFunc),
	}
}

func (h *Handlers) HandleStart(ctx context.Context, b *bot.Bot, update *models.Update) {
	text := `Welcome to *OpenCode Manager*!

Commands:
/new <name> <path> — Create & start a new instance
/list — List all instances
/switch <name> — Switch active instance
/stop [name] — Stop an instance
/start\_inst <name> — Start a stopped instance
/status — Current instance & session info
/session [new] — Show or create session
/sessions — List sessions
/abort — Abort running prompt
/help — Show this help

Send any text to prompt the active instance.`

	_, _ = b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:    update.Message.Chat.ID,
		Text:      text,
		ParseMode: models.ParseModeMarkdown,
	})
}

func (h *Handlers) HandleHelp(ctx context.Context, b *bot.Bot, update *models.Update) {
	h.HandleStart(ctx, b, update)
}

func (h *Handlers) HandleNew(ctx context.Context, b *bot.Bot, update *models.Update) {
	parts := strings.Fields(update.Message.Text)
	if len(parts) < 3 {
		_, _ = b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID: update.Message.Chat.ID,
			Text:   "Usage: /new <name> <path>",
		})
		return
	}

	name := parts[1]
	dir := strings.Join(parts[2:], " ")

	// Check if name already exists
	if inst := h.procMgr.GetInstanceByName(name); inst != nil {
		_, _ = b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID: update.Message.Chat.ID,
			Text:   fmt.Sprintf("Instance '%s' already exists.", name),
		})
		return
	}

	inst, err := h.procMgr.CreateAndStart(name, dir, false)
	if err != nil {
		_, _ = b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID: update.Message.Chat.ID,
			Text:   fmt.Sprintf("Failed to create instance: %s", err),
		})
		return
	}

	// Auto-switch to new instance
	userID := update.Message.From.ID
	_ = h.store.SetActiveInstance(userID, inst.ID)

	// Start SSE listener for this instance
	h.startSSEListener(inst)

	_, _ = b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID: update.Message.Chat.ID,
		Text:   fmt.Sprintf("Instance *%s* created and started on port %d.\nSwitched to this instance.", escapeMarkdown(name), inst.Port),
		ParseMode: models.ParseModeMarkdown,
		ReplyMarkup: instanceActionsKeyboard(inst),
	})
}

func (h *Handlers) HandleList(ctx context.Context, b *bot.Bot, update *models.Update) {
	instances := h.procMgr.ListInstances()
	if len(instances) == 0 {
		_, _ = b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID: update.Message.Chat.ID,
			Text:   "No instances. Use /new <name> <path> to create one.",
		})
		return
	}

	var sb strings.Builder
	sb.WriteString("*Instances:*\n\n")
	for _, inst := range instances {
		status := "stopped"
		if inst.Status() == process.StatusRunning {
			status = "running"
		} else if inst.Status() == process.StatusFailed {
			status = "failed"
		}
		sb.WriteString(fmt.Sprintf("• *%s* — %s (`%s`)\n", escapeMarkdown(inst.Name), status, inst.Directory))
	}

	_, _ = b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:      update.Message.Chat.ID,
		Text:        sb.String(),
		ParseMode:   models.ParseModeMarkdown,
		ReplyMarkup: instanceListKeyboard(instances),
	})
}

func (h *Handlers) HandleSwitch(ctx context.Context, b *bot.Bot, update *models.Update) {
	parts := strings.Fields(update.Message.Text)
	if len(parts) < 2 {
		_, _ = b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID: update.Message.Chat.ID,
			Text:   "Usage: /switch <name>",
		})
		return
	}

	name := parts[1]
	inst := h.procMgr.GetInstanceByName(name)
	if inst == nil {
		_, _ = b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID: update.Message.Chat.ID,
			Text:   fmt.Sprintf("Instance '%s' not found.", name),
		})
		return
	}

	userID := update.Message.From.ID
	_ = h.store.SetActiveInstance(userID, inst.ID)

	_, _ = b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:    update.Message.Chat.ID,
		Text:      fmt.Sprintf("Switched to *%s*", escapeMarkdown(inst.Name)),
		ParseMode: models.ParseModeMarkdown,
	})
}

func (h *Handlers) HandleStop(ctx context.Context, b *bot.Bot, update *models.Update) {
	parts := strings.Fields(update.Message.Text)
	userID := update.Message.From.ID

	var inst *process.Instance

	if len(parts) >= 2 {
		name := parts[1]
		inst = h.procMgr.GetInstanceByName(name)
	} else {
		// Stop active instance
		state, _ := h.store.GetUserState(userID)
		if state != nil && state.ActiveInstanceID != "" {
			inst = h.procMgr.GetInstance(state.ActiveInstanceID)
		}
	}

	if inst == nil {
		_, _ = b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID: update.Message.Chat.ID,
			Text:   "No instance specified or active. Usage: /stop [name]",
		})
		return
	}

	// Cancel SSE subscriber
	if cancel, ok := h.sseSubscribers[inst.ID]; ok {
		cancel()
		delete(h.sseSubscribers, inst.ID)
	}

	if err := h.procMgr.StopInstance(inst.ID); err != nil {
		_, _ = b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID: update.Message.Chat.ID,
			Text:   fmt.Sprintf("Failed to stop: %s", err),
		})
		return
	}

	// Clear user state if this was their active instance
	_ = h.store.ClearUserState(userID, inst.ID)

	_, _ = b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:    update.Message.Chat.ID,
		Text:      fmt.Sprintf("Instance *%s* stopped.", escapeMarkdown(inst.Name)),
		ParseMode: models.ParseModeMarkdown,
	})
}

func (h *Handlers) HandleStartInst(ctx context.Context, b *bot.Bot, update *models.Update) {
	parts := strings.Fields(update.Message.Text)
	if len(parts) < 2 {
		_, _ = b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID: update.Message.Chat.ID,
			Text:   "Usage: /start_inst <name>",
		})
		return
	}

	name := parts[1]
	inst := h.procMgr.GetInstanceByName(name)
	if inst == nil {
		_, _ = b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID: update.Message.Chat.ID,
			Text:   fmt.Sprintf("Instance '%s' not found.", name),
		})
		return
	}

	if inst.Status() == process.StatusRunning {
		_, _ = b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID: update.Message.Chat.ID,
			Text:   fmt.Sprintf("Instance '%s' is already running.", name),
		})
		return
	}

	if err := h.procMgr.StartInstance(inst.ID); err != nil {
		_, _ = b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID: update.Message.Chat.ID,
			Text:   fmt.Sprintf("Failed to start: %s", err),
		})
		return
	}

	// Start SSE listener
	h.startSSEListener(inst)

	_, _ = b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:    update.Message.Chat.ID,
		Text:      fmt.Sprintf("Instance *%s* started.", escapeMarkdown(inst.Name)),
		ParseMode: models.ParseModeMarkdown,
	})
}

func (h *Handlers) HandleStatus(ctx context.Context, b *bot.Bot, update *models.Update) {
	userID := update.Message.From.ID
	state, _ := h.store.GetUserState(userID)

	if state == nil || state.ActiveInstanceID == "" {
		_, _ = b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID: update.Message.Chat.ID,
			Text:   "No active instance. Use /list to select one.",
		})
		return
	}

	inst := h.procMgr.GetInstance(state.ActiveInstanceID)
	if inst == nil {
		_, _ = b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID: update.Message.Chat.ID,
			Text:   "Active instance not found. Use /list to select another.",
		})
		return
	}

	status := string(inst.Status())
	sessionInfo := "none"
	if state.ActiveSessionID != "" {
		sessionInfo = state.ActiveSessionID
	}

	text := fmt.Sprintf("*Active Instance:* %s\n*Status:* %s\n*Directory:* `%s`\n*Port:* %d\n*Session:* %s",
		escapeMarkdown(inst.Name), status, inst.Directory, inst.Port, sessionInfo)

	_, _ = b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:    update.Message.Chat.ID,
		Text:      text,
		ParseMode: models.ParseModeMarkdown,
	})
}

func (h *Handlers) HandleSession(ctx context.Context, b *bot.Bot, update *models.Update) {
	userID := update.Message.From.ID
	inst, client, err := h.getActiveClient(userID)
	if err != nil {
		_, _ = b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID: update.Message.Chat.ID,
			Text:   err.Error(),
		})
		return
	}

	parts := strings.Fields(update.Message.Text)

	if len(parts) >= 2 && parts[1] == "new" {
		session, err := client.CreateSession()
		if err != nil {
			_, _ = b.SendMessage(ctx, &bot.SendMessageParams{
				ChatID: update.Message.Chat.ID,
				Text:   fmt.Sprintf("Failed to create session: %s", err),
			})
			return
		}

		_ = h.store.SetActiveSession(userID, session.ID)

		_, _ = b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:    update.Message.Chat.ID,
			Text:      fmt.Sprintf("*[%s]* New session created: `%s`", escapeMarkdown(inst.Name), session.ID),
			ParseMode: models.ParseModeMarkdown,
		})
		return
	}

	// Show current session
	state, _ := h.store.GetUserState(userID)
	if state.ActiveSessionID == "" {
		_, _ = b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID: update.Message.Chat.ID,
			Text:   "No active session. Use `/session new` to create one.",
			ParseMode: models.ParseModeMarkdown,
		})
		return
	}

	session, err := client.GetSession(state.ActiveSessionID)
	if err != nil {
		_, _ = b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID: update.Message.Chat.ID,
			Text:   fmt.Sprintf("Failed to get session: %s", err),
		})
		return
	}

	title := session.Title
	if title == "" {
		title = "(untitled)"
	}

	_, _ = b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:    update.Message.Chat.ID,
		Text:      fmt.Sprintf("*[%s]* Session: `%s`\nTitle: %s", escapeMarkdown(inst.Name), session.ID, title),
		ParseMode: models.ParseModeMarkdown,
	})
}

func (h *Handlers) HandleSessions(ctx context.Context, b *bot.Bot, update *models.Update) {
	userID := update.Message.From.ID
	inst, client, err := h.getActiveClient(userID)
	if err != nil {
		_, _ = b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID: update.Message.Chat.ID,
			Text:   err.Error(),
		})
		return
	}

	sessions, err := client.ListSessions()
	if err != nil {
		_, _ = b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID: update.Message.Chat.ID,
			Text:   fmt.Sprintf("Failed to list sessions: %s", err),
		})
		return
	}

	if len(sessions) == 0 {
		_, _ = b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID: update.Message.Chat.ID,
			Text:   fmt.Sprintf("*[%s]* No sessions. Use `/session new` to create one.", escapeMarkdown(inst.Name)),
			ParseMode: models.ParseModeMarkdown,
		})
		return
	}

	var entries []sessionEntry
	for _, s := range sessions {
		entries = append(entries, sessionEntry{ID: s.ID, Title: s.Title})
	}

	_, _ = b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:      update.Message.Chat.ID,
		Text:        fmt.Sprintf("*[%s]* Sessions:", escapeMarkdown(inst.Name)),
		ParseMode:   models.ParseModeMarkdown,
		ReplyMarkup: sessionListKeyboard(entries),
	})
}

func (h *Handlers) HandleAbort(ctx context.Context, b *bot.Bot, update *models.Update) {
	userID := update.Message.From.ID
	inst, client, err := h.getActiveClient(userID)
	if err != nil {
		_, _ = b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID: update.Message.Chat.ID,
			Text:   err.Error(),
		})
		return
	}

	state, _ := h.store.GetUserState(userID)
	if state.ActiveSessionID == "" {
		_, _ = b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID: update.Message.Chat.ID,
			Text:   "No active session.",
		})
		return
	}

	if err := client.Abort(state.ActiveSessionID); err != nil {
		_, _ = b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID: update.Message.Chat.ID,
			Text:   fmt.Sprintf("Failed to abort: %s", err),
		})
		return
	}

	// Stop the stream
	h.streamMgr.RemoveStream(state.ActiveSessionID)

	_, _ = b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:    update.Message.Chat.ID,
		Text:      fmt.Sprintf("*[%s]* Aborted.", escapeMarkdown(inst.Name)),
		ParseMode: models.ParseModeMarkdown,
	})
}

func (h *Handlers) HandlePrompt(ctx context.Context, b *bot.Bot, update *models.Update) {
	userID := update.Message.From.ID
	text := update.Message.Text

	inst, client, err := h.getActiveClient(userID)
	if err != nil {
		_, _ = b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID: update.Message.Chat.ID,
			Text:   err.Error(),
		})
		return
	}

	state, _ := h.store.GetUserState(userID)

	// Auto-create session if none exists
	if state.ActiveSessionID == "" {
		session, err := client.CreateSession()
		if err != nil {
			_, _ = b.SendMessage(ctx, &bot.SendMessageParams{
				ChatID: update.Message.Chat.ID,
				Text:   fmt.Sprintf("Failed to create session: %s", err),
			})
			return
		}
		_ = h.store.SetActiveSession(userID, session.ID)
		state.ActiveSessionID = session.ID
	}

	// Send placeholder message
	placeholder, err := b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:    update.Message.Chat.ID,
		Text:      fmt.Sprintf("*[%s]* _Thinking..._", escapeMarkdown(inst.Name)),
		ParseMode: models.ParseModeMarkdown,
	})
	if err != nil {
		slog.Error("failed to send placeholder", "error", err)
		return
	}

	// Start stream context
	sc := h.streamMgr.StartStream(b, update.Message.Chat.ID, placeholder.ID, state.ActiveSessionID, inst.Name)

	// Register SSE handler for this stream
	_ = sc // SSE handlers are set up in startSSEListener

	// Fire prompt
	if err := client.PromptAsync(state.ActiveSessionID, text); err != nil {
		_, _ = b.EditMessageText(ctx, &bot.EditMessageTextParams{
			ChatID:    update.Message.Chat.ID,
			MessageID: placeholder.ID,
			Text:      fmt.Sprintf("*[%s]* Failed to send prompt: %s", escapeMarkdown(inst.Name), err),
			ParseMode: models.ParseModeMarkdown,
		})
		h.streamMgr.RemoveStream(state.ActiveSessionID)
		return
	}
}

// getActiveClient returns the active instance and its HTTP client for a user.
func (h *Handlers) getActiveClient(userID int64) (*process.Instance, *opencode.Client, error) {
	state, err := h.store.GetUserState(userID)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get user state: %s", err)
	}

	if state.ActiveInstanceID == "" {
		return nil, nil, fmt.Errorf("no active instance. Use /list to select one")
	}

	inst := h.procMgr.GetInstance(state.ActiveInstanceID)
	if inst == nil {
		return nil, nil, fmt.Errorf("active instance not found. Use /list to select another")
	}

	if inst.Status() != process.StatusRunning {
		return nil, nil, fmt.Errorf("instance '%s' is not running. Use /start_inst %s", inst.Name, inst.Name)
	}

	client := opencode.NewClient(inst.BaseURL(), inst.Password)
	return inst, client, nil
}

func (h *Handlers) startSSEListener(inst *process.Instance) {
	// Cancel existing listener
	if cancel, ok := h.sseSubscribers[inst.ID]; ok {
		cancel()
	}

	client := opencode.NewClient(inst.BaseURL(), inst.Password)
	subscriber := opencode.NewSSESubscriber(client)

	ctx, cancel := context.WithCancel(context.Background())
	h.sseSubscribers[inst.ID] = cancel

	// Register wildcard handler that routes to active streams
	subscriber.On("*", func(eventType string, data json.RawMessage) {
		// Try to extract sessionID from the event to find the right stream
		var msg opencode.Message
		if err := json.Unmarshal(data, &msg); err != nil {
			return
		}

		if sc := h.streamMgr.GetStream(msg.SessionID); sc != nil {
			sc.HandleSSEEvent(eventType, data)
		}
	})

	go func() {
		if err := subscriber.Subscribe(ctx); err != nil && ctx.Err() == nil {
			slog.Error("SSE subscriber failed", "instance", inst.Name, "error", err)
		}
	}()
}

// StartSSEForRunning starts SSE listeners for all running instances.
func (h *Handlers) StartSSEForRunning() {
	for _, inst := range h.procMgr.ListInstances() {
		if inst.Status() == process.StatusRunning {
			h.startSSEListener(inst)
		}
	}
}

// StopAllSSE cancels all SSE subscribers.
func (h *Handlers) StopAllSSE() {
	for id, cancel := range h.sseSubscribers {
		cancel()
		delete(h.sseSubscribers, id)
	}
}
