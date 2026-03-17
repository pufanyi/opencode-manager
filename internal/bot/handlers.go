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
	text := `Welcome to <b>OpenCode Manager</b>!

Commands:
/new &lt;name&gt; &lt;path&gt; — Create instance (Claude Code)
/newopencode &lt;name&gt; &lt;path&gt; — Create OpenCode instance
/list — List all instances
/switch &lt;name&gt; — Switch active instance
/stop [name] — Stop an instance
/start_inst &lt;name&gt; — Start a stopped instance
/status — Current instance &amp; session info
/session [new] — Show or create session
/sessions — List &amp; manage sessions
/abort — Abort running prompt
/help — Show this help

Send any text to prompt the active instance.`

	_, _ = b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:    update.Message.Chat.ID,
		Text:      text,
		ParseMode: models.ParseModeHTML,
	})
}

func (h *Handlers) HandleHelp(ctx context.Context, b *bot.Bot, update *models.Update) {
	h.HandleStart(ctx, b, update)
}

func (h *Handlers) HandleNew(ctx context.Context, b *bot.Bot, update *models.Update) {
	h.handleNewInstance(ctx, b, update, provider.TypeClaudeCode)
}

func (h *Handlers) HandleNewOpenCode(ctx context.Context, b *bot.Bot, update *models.Update) {
	h.handleNewInstance(ctx, b, update, provider.TypeOpenCode)
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
		Text:        fmt.Sprintf("%s instance <b>%s</b> created.\nSwitched to this instance.", label, escapeHTML(name)),
		ParseMode:   models.ParseModeHTML,
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
			Text:   "No instances. Use /new or /newopencode to create one.",
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
		Text:      fmt.Sprintf("Switched to <b>%s</b>", escapeHTML(inst.Name)),
		ParseMode: models.ParseModeHTML,
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
		Text:      fmt.Sprintf("Instance <b>%s</b> stopped.", escapeHTML(inst.Name)),
		ParseMode: models.ParseModeHTML,
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
		Text:      fmt.Sprintf("Instance <b>%s</b> started.", escapeHTML(inst.Name)),
		ParseMode: models.ParseModeHTML,
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
		cs, _ := h.store.GetClaudeSession(state.ActiveSessionID)
		if cs != nil && cs.Title != "" {
			sessionInfo = fmt.Sprintf("%s (%d msgs)", cs.Title, cs.MessageCount)
		} else {
			sessionInfo = state.ActiveSessionID[:min(12, len(state.ActiveSessionID))]
		}
	}

	text := fmt.Sprintf("<b>Active Instance:</b> %s\n<b>Provider:</b> %s\n<b>Status:</b> %s\n<b>Directory:</b> <code>%s</code>\n<b>Session:</b> %s",
		escapeHTML(inst.Name), provLabel, string(inst.Status()), escapeHTML(inst.Directory), escapeHTML(sessionInfo))

	_, _ = b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:    update.Message.Chat.ID,
		Text:      text,
		ParseMode: models.ParseModeHTML,
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
			Text:      fmt.Sprintf("<b>[%s]</b> New session created: <code>%s</code>", escapeHTML(inst.Name), session.ID),
			ParseMode: models.ParseModeHTML,
		})
		return
	}

	state, _ := h.store.GetUserState(userID)
	if state.ActiveSessionID == "" {
		_, _ = b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:    update.Message.Chat.ID,
			Text:      "No active session. Use <code>/session new</code> to create one.",
			ParseMode: models.ParseModeHTML,
		})
		return
	}

	cs, err := h.store.GetClaudeSession(state.ActiveSessionID)
	if err != nil || cs == nil {
		_, _ = b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID: update.Message.Chat.ID,
			Text:   fmt.Sprintf("Failed to get session: session not found"),
		})
		return
	}

	title := cs.Title
	if title == "" {
		title = "(untitled)"
	}

	timeAgo := formatTimeAgo(cs.UpdatedAt)

	_, _ = b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:    update.Message.Chat.ID,
		Text:      fmt.Sprintf("<b>[%s]</b> Session: <code>%s</code>\nTitle: %s\nMessages: %d\nLast active: %s", escapeHTML(inst.Name), cs.ID, escapeHTML(title), cs.MessageCount, timeAgo),
		ParseMode: models.ParseModeHTML,
	})
}

func (h *Handlers) HandleSessions(ctx context.Context, b *bot.Bot, update *models.Update) {
	userID := update.Message.From.ID
	state, _ := h.store.GetUserState(userID)

	inst, err := h.getActiveInstance(userID)
	if err != nil {
		_, _ = b.SendMessage(ctx, &bot.SendMessageParams{ChatID: update.Message.Chat.ID, Text: err.Error()})
		return
	}

	dbSessions, err := h.store.ListClaudeSessions(inst.ID)
	if err != nil {
		_, _ = b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID: update.Message.Chat.ID,
			Text:   fmt.Sprintf("Failed to list sessions: %s", err),
		})
		return
	}

	if len(dbSessions) == 0 {
		_, _ = b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:    update.Message.Chat.ID,
			Text:      fmt.Sprintf("<b>[%s]</b> No sessions. Use <code>/session new</code> or send a message to create one.", escapeHTML(inst.Name)),
			ParseMode: models.ParseModeHTML,
		})
		return
	}

	activeSessionID := ""
	if state != nil {
		activeSessionID = state.ActiveSessionID
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("<b>[%s]</b> Sessions:\n\n", escapeHTML(inst.Name)))
	for i, s := range dbSessions {
		if i >= 20 {
			sb.WriteString(fmt.Sprintf("\n... and %d more", len(dbSessions)-20))
			break
		}
		title := s.Title
		if title == "" {
			title = "(untitled)"
		}
		icon := "  "
		if s.ID == activeSessionID {
			icon = "▶ "
		}
		timeAgo := formatTimeAgo(s.UpdatedAt)
		sb.WriteString(fmt.Sprintf("%s<b>%s</b>\n   %d msgs · %s\n", icon, escapeHTML(title), s.MessageCount, timeAgo))
	}

	var entries []sessionEntry
	for _, s := range dbSessions {
		entries = append(entries, sessionEntry{
			ID:           s.ID,
			Title:        s.Title,
			MessageCount: s.MessageCount,
			IsActive:     s.ID == activeSessionID,
		})
	}

	_, _ = b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:      update.Message.Chat.ID,
		Text:        sb.String(),
		ParseMode:   models.ParseModeHTML,
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
		Text:      fmt.Sprintf("<b>[%s]</b> Aborted.", escapeHTML(inst.Name)),
		ParseMode: models.ParseModeHTML,
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

		// Auto-title session from first prompt
		title := text
		if len(title) > 60 {
			title = title[:60] + "..."
		}
		_ = h.store.UpdateClaudeSessionTitle(session.ID, title)
	}

	// Track session activity
	_ = h.store.UpdateClaudeSessionActivity(state.ActiveSessionID)

	// Send placeholder
	placeholder, err := b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID: update.Message.Chat.ID,
		Text:   fmt.Sprintf("[%s] Thinking...", inst.Name),
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
			Text:      fmt.Sprintf("<b>[%s]</b> Failed to send prompt: %s", escapeHTML(inst.Name), err),
			ParseMode: models.ParseModeHTML,
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
