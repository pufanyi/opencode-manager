package bot

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/pufanyi/opencode-manager/internal/process"
	"github.com/pufanyi/opencode-manager/internal/provider"
	"github.com/pufanyi/opencode-manager/internal/store"
)

type Handlers struct {
	procMgr   *process.Manager
	store     *store.Store
	streamMgr *StreamManager
}

func NewHandlers(procMgr *process.Manager, st *store.Store, streamMgr *StreamManager) *Handlers {
	return &Handlers{
		procMgr:   procMgr,
		store:     st,
		streamMgr: streamMgr,
	}
}

func (h *Handlers) HandleStart(ctx context.Context, b *bot.Bot, update *models.Update) {
	text := `Welcome to *OpenCode Manager*!

Commands:
/new <name> <path> — Create OpenCode instance
/newclaude <name> <path> — Create Claude Code instance
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
	h.handleNewInstance(ctx, b, update, provider.TypeOpenCode)
}

func (h *Handlers) HandleNewClaude(ctx context.Context, b *bot.Bot, update *models.Update) {
	h.handleNewInstance(ctx, b, update, provider.TypeClaudeCode)
}

func (h *Handlers) handleNewInstance(ctx context.Context, b *bot.Bot, update *models.Update, provType provider.Type) {
	parts := strings.Fields(update.Message.Text)
	if len(parts) < 3 {
		_, _ = b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID: update.Message.Chat.ID,
			Text:   fmt.Sprintf("Usage: /%s <name> <path>", parts[0][1:]),
		})
		return
	}

	name := parts[1]
	dir := strings.Join(parts[2:], " ")

	if inst := h.procMgr.GetInstanceByName(name); inst != nil {
		_, _ = b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID: update.Message.Chat.ID,
			Text:   fmt.Sprintf("Instance '%s' already exists.", name),
		})
		return
	}

	inst, err := h.procMgr.CreateAndStart(name, dir, false, provType)
	if err != nil {
		_, _ = b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID: update.Message.Chat.ID,
			Text:   fmt.Sprintf("Failed to create instance: %s", err),
		})
		return
	}

	userID := update.Message.From.ID
	_ = h.store.SetActiveInstance(userID, inst.ID)

	label := "OpenCode"
	if provType == provider.TypeClaudeCode {
		label = "Claude Code"
	}

	_, _ = b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:      update.Message.Chat.ID,
		Text:        fmt.Sprintf("%s instance *%s* created.\nSwitched to this instance.", label, escapeMarkdown(name)),
		ParseMode:   models.ParseModeMarkdown,
		ReplyMarkup: instanceActionsKeyboard(inst),
	})
}

func (h *Handlers) HandleList(ctx context.Context, b *bot.Bot, update *models.Update) {
	slog.Info("HandleList called", "user", update.Message.From.ID)
	instances := h.procMgr.ListInstances()
	slog.Info("instances found", "count", len(instances))

	if len(instances) == 0 {
		_, _ = b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID: update.Message.Chat.ID,
			Text:   "No instances. Use /new or /newclaude to create one.",
		})
		return
	}

	var sb strings.Builder
	sb.WriteString("Instances:\n\n")
	for _, inst := range instances {
		status := "stopped"
		icon := "🔴"
		if inst.Status() == process.StatusRunning {
			status = "running"
			icon = "🟢"
		} else if inst.Status() == process.StatusStarting {
			status = "starting"
			icon = "🟡"
		} else if inst.Status() == process.StatusFailed {
			status = "failed"
		}
		provLabel := "OC"
		if inst.ProviderType == provider.TypeClaudeCode {
			provLabel = "CC"
		}
		sb.WriteString(fmt.Sprintf("%s [%s] %s — %s\n   %s\n", icon, provLabel, inst.Name, status, inst.Directory))
	}

	_, err := b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:      update.Message.Chat.ID,
		Text:        sb.String(),
		ReplyMarkup: instanceListKeyboard(instances),
	})
	if err != nil {
		slog.Error("failed to send list", "error", err)
	}
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

	_ = h.store.SetActiveInstance(update.Message.From.ID, inst.ID)

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
		inst = h.procMgr.GetInstanceByName(parts[1])
	} else {
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

	if err := h.procMgr.StopInstance(inst.ID); err != nil {
		_, _ = b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID: update.Message.Chat.ID,
			Text:   fmt.Sprintf("Failed to stop: %s", err),
		})
		return
	}

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

	provLabel := "OpenCode"
	if inst.ProviderType == provider.TypeClaudeCode {
		provLabel = "Claude Code"
	}

	sessionInfo := "none"
	if state.ActiveSessionID != "" {
		sessionInfo = state.ActiveSessionID
	}

	text := fmt.Sprintf("*Active Instance:* %s\n*Provider:* %s\n*Status:* %s\n*Directory:* `%s`\n*Session:* %s",
		escapeMarkdown(inst.Name), provLabel, string(inst.Status()), inst.Directory, sessionInfo)

	_, _ = b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:    update.Message.Chat.ID,
		Text:      text,
		ParseMode: models.ParseModeMarkdown,
	})
}

func (h *Handlers) HandleSession(ctx context.Context, b *bot.Bot, update *models.Update) {
	userID := update.Message.From.ID
	inst, err := h.getActiveInstance(userID)
	if err != nil {
		_, _ = b.SendMessage(ctx, &bot.SendMessageParams{ChatID: update.Message.Chat.ID, Text: err.Error()})
		return
	}

	parts := strings.Fields(update.Message.Text)

	if len(parts) >= 2 && parts[1] == "new" {
		session, err := inst.Provider.CreateSession(ctx)
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

	state, _ := h.store.GetUserState(userID)
	if state.ActiveSessionID == "" {
		_, _ = b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:    update.Message.Chat.ID,
			Text:      "No active session. Use `/session new` to create one.",
			ParseMode: models.ParseModeMarkdown,
		})
		return
	}

	session, err := inst.Provider.GetSession(ctx, state.ActiveSessionID)
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
	inst, err := h.getActiveInstance(userID)
	if err != nil {
		_, _ = b.SendMessage(ctx, &bot.SendMessageParams{ChatID: update.Message.Chat.ID, Text: err.Error()})
		return
	}

	sessions, err := inst.Provider.ListSessions(ctx)
	if err != nil {
		_, _ = b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID: update.Message.Chat.ID,
			Text:   fmt.Sprintf("Failed to list sessions: %s", err),
		})
		return
	}

	if len(sessions) == 0 {
		_, _ = b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:    update.Message.Chat.ID,
			Text:      fmt.Sprintf("*[%s]* No sessions. Use `/session new` to create one.", escapeMarkdown(inst.Name)),
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
	inst, err := h.getActiveInstance(userID)
	if err != nil {
		_, _ = b.SendMessage(ctx, &bot.SendMessageParams{ChatID: update.Message.Chat.ID, Text: err.Error()})
		return
	}

	state, _ := h.store.GetUserState(userID)
	if state.ActiveSessionID == "" {
		_, _ = b.SendMessage(ctx, &bot.SendMessageParams{ChatID: update.Message.Chat.ID, Text: "No active session."})
		return
	}

	if err := inst.Provider.Abort(ctx, state.ActiveSessionID); err != nil {
		_, _ = b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID: update.Message.Chat.ID,
			Text:   fmt.Sprintf("Failed to abort: %s", err),
		})
		return
	}

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

	inst, err := h.getActiveInstance(userID)
	if err != nil {
		_, _ = b.SendMessage(ctx, &bot.SendMessageParams{ChatID: update.Message.Chat.ID, Text: err.Error()})
		return
	}

	state, _ := h.store.GetUserState(userID)

	// Auto-create session if none exists
	if state.ActiveSessionID == "" {
		session, err := inst.Provider.CreateSession(ctx)
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

	// Send placeholder
	placeholder, err := b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:    update.Message.Chat.ID,
		Text:      fmt.Sprintf("*[%s]* _Thinking..._", escapeMarkdown(inst.Name)),
		ParseMode: models.ParseModeMarkdown,
	})
	if err != nil {
		slog.Error("failed to send placeholder", "error", err)
		return
	}

	// Start prompt via provider
	promptCtx, promptCancel := context.WithCancel(context.Background())
	ch, err := inst.Provider.Prompt(promptCtx, state.ActiveSessionID, text)
	if err != nil {
		promptCancel()
		_, _ = b.EditMessageText(ctx, &bot.EditMessageTextParams{
			ChatID:    update.Message.Chat.ID,
			MessageID: placeholder.ID,
			Text:      fmt.Sprintf("*[%s]* Failed to send prompt: %s", escapeMarkdown(inst.Name), err),
			ParseMode: models.ParseModeMarkdown,
		})
		return
	}

	// Wire up streaming
	sc := h.streamMgr.StartStream(b, update.Message.Chat.ID, placeholder.ID, state.ActiveSessionID, inst.Name, ch)
	_ = sc
	_ = promptCancel // cancel is held by the stream context
}

// getActiveInstance returns the active instance for a user.
func (h *Handlers) getActiveInstance(userID int64) (*process.Instance, error) {
	state, err := h.store.GetUserState(userID)
	if err != nil {
		return nil, fmt.Errorf("failed to get user state: %s", err)
	}

	if state.ActiveInstanceID == "" {
		return nil, fmt.Errorf("no active instance. Use /list to select one")
	}

	inst := h.procMgr.GetInstance(state.ActiveInstanceID)
	if inst == nil {
		return nil, fmt.Errorf("active instance not found. Use /list to select another")
	}

	if inst.Status() != process.StatusRunning {
		return nil, fmt.Errorf("instance '%s' is not running. Use /start_inst %s", inst.Name, inst.Name)
	}

	return inst, nil
}
