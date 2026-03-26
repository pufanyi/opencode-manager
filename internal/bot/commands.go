package bot

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/pufanyi/opencode-manager/internal/process"
	"github.com/pufanyi/opencode-manager/internal/provider"
)

func (h *Handlers) HandleLink(ctx context.Context, b *bot.Bot, update *models.Update) {
	if h.firebase == nil {
		_, _ = b.SendMessage(ctx, &bot.SendMessageParams{ChatID: update.Message.Chat.ID, Text: "Firebase is not enabled."})
		return
	}

	parts := strings.Fields(update.Message.Text)
	if len(parts) < 2 {
		_, _ = b.SendMessage(ctx, &bot.SendMessageParams{ChatID: update.Message.Chat.ID, Text: "Usage: /link <code>"})
		return
	}
	code := parts[1]

	var data map[string]interface{}
	err := h.firebase.RTDB.Get(ctx, "link_codes/"+code, &data)
	if err != nil || data == nil {
		_, _ = b.SendMessage(ctx, &bot.SendMessageParams{ChatID: update.Message.Chat.ID, Text: "Invalid or expired link code."})
		return
	}

	uid, ok := data["uid"].(string)
	if !ok || uid == "" {
		_, _ = b.SendMessage(ctx, &bot.SendMessageParams{ChatID: update.Message.Chat.ID, Text: "Invalid link code data."})
		return
	}

	expiresFloat, ok := data["expires"].(float64)
	if !ok {
		_, _ = b.SendMessage(ctx, &bot.SendMessageParams{ChatID: update.Message.Chat.ID, Text: "Invalid link code data."})
		return
	}

	if time.Now().UnixMilli() > int64(expiresFloat) {
		_, _ = b.SendMessage(ctx, &bot.SendMessageParams{ChatID: update.Message.Chat.ID, Text: "Link code has expired."})
		return
	}

	err = h.firebase.RTDB.Set(ctx, "users/"+uid+"/telegram_id", update.Message.From.ID)
	if err != nil {
		_, _ = b.SendMessage(ctx, &bot.SendMessageParams{ChatID: update.Message.Chat.ID, Text: "Failed to link account."})
		return
	}

	_ = h.firebase.RTDB.Delete(ctx, "link_codes/"+code)

	_, _ = b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID: update.Message.Chat.ID,
		Text:   "✅ Successfully linked your Telegram account to the Web Dashboard! You can now use the dashboard.",
	})
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
	_ = h.tgState.SetActiveInstance(ctx, userID, inst.ID)

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

	_ = h.tgState.SetActiveInstance(ctx, update.Message.From.ID, inst.ID)

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
		state, _ := h.tgState.GetUserState(ctx, userID)
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

	_ = h.tgState.ClearUserState(ctx, userID, inst.ID)

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
	state, _ := h.tgState.GetUserState(ctx, userID)

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
		cs, _ := h.store.GetClaudeSession(state.ActiveInstanceID, state.ActiveSessionID)
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
	inst, err := h.getActiveInstance(ctx, userID)
	if err != nil {
		_, _ = b.SendMessage(ctx, &bot.SendMessageParams{ChatID: update.Message.Chat.ID, Text: err.Error()})
		return
	}

	parts := strings.Fields(update.Message.Text)

	if len(parts) >= 2 && parts[1] == "new" {
		if inst.Provider.SupportsWorktree() {
			h.showWorktreeChoice(ctx, b, inst, userID, update.Message.Chat.ID, 0, "", "", nil)
			return
		}
		session, err := inst.Provider.CreateSession(ctx, nil)
		if err != nil {
			_, _ = b.SendMessage(ctx, &bot.SendMessageParams{
				ChatID: update.Message.Chat.ID,
				Text:   fmt.Sprintf("Failed to create session: %s", err),
			})
			return
		}

		_ = h.tgState.SetActiveSession(ctx, userID, session.ID)

		_, _ = b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:    update.Message.Chat.ID,
			Text:      fmt.Sprintf("<b>[%s]</b> New session created: <code>%s</code>", escapeHTML(inst.Name), session.ID),
			ParseMode: models.ParseModeHTML,
		})
		return
	}

	state, _ := h.tgState.GetUserState(ctx, userID)
	if state.ActiveSessionID == "" {
		_, _ = b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:    update.Message.Chat.ID,
			Text:      "No active session. Use <code>/session new</code> to create one.",
			ParseMode: models.ParseModeHTML,
		})
		return
	}

	cs, err := h.store.GetClaudeSession(inst.ID, state.ActiveSessionID)
	if err != nil || cs == nil {
		_, _ = b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID: update.Message.Chat.ID,
			Text:   "Failed to get session: session not found",
		})
		return
	}

	title := cs.Title
	if title == "" {
		title = "(untitled)"
	}

	timeAgo := formatTimeAgo(cs.UpdatedAt)

	branchInfo := ""
	if cs.Branch != "" {
		branchInfo = fmt.Sprintf("\nBranch: <code>%s</code>", escapeHTML(cs.Branch))
	}

	_, _ = b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:    update.Message.Chat.ID,
		Text:      fmt.Sprintf("<b>[%s]</b> Session: <code>%s</code>\nTitle: %s\nMessages: %d\nLast active: %s%s", escapeHTML(inst.Name), cs.ID, escapeHTML(title), cs.MessageCount, timeAgo, branchInfo),
		ParseMode: models.ParseModeHTML,
	})
}

func (h *Handlers) HandleSessions(ctx context.Context, b *bot.Bot, update *models.Update) {
	userID := update.Message.From.ID
	state, _ := h.tgState.GetUserState(ctx, userID)

	inst, err := h.getActiveInstance(ctx, userID)
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
	inst, err := h.getActiveInstance(ctx, userID)
	if err != nil {
		_, _ = b.SendMessage(ctx, &bot.SendMessageParams{ChatID: update.Message.Chat.ID, Text: err.Error()})
		return
	}

	state, _ := h.tgState.GetUserState(ctx, userID)
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
