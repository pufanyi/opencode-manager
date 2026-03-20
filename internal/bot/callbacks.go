package bot

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/pufanyi/opencode-manager/internal/process"
	"github.com/pufanyi/opencode-manager/internal/provider"
)

func (h *Handlers) HandleCallback(ctx context.Context, b *bot.Bot, update *models.Update) {
	if update.CallbackQuery == nil {
		return
	}

	data := update.CallbackQuery.Data
	userID := update.CallbackQuery.From.ID
	chatID := update.CallbackQuery.Message.Message.Chat.ID

	_, _ = b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{
		CallbackQueryID: update.CallbackQuery.ID,
	})

	parts := strings.SplitN(data, ":", 2)
	action := parts[0]
	var param string
	if len(parts) > 1 {
		param = parts[1]
	}

	switch action {
	case "switch":
		h.callbackSwitch(ctx, b, chatID, userID, param)
	case "stop":
		h.callbackStop(ctx, b, chatID, userID, param)
	case "start":
		h.callbackStart(ctx, b, chatID, userID, param)
	case "delete":
		h.callbackDelete(ctx, b, chatID, userID, param)
	case "session":
		h.callbackSession(ctx, b, chatID, userID, param)
	case "abort":
		h.callbackAbort(ctx, b, chatID, userID, param)
	case "newsession":
		h.callbackNewSession(ctx, b, chatID, userID)
	case "delsession":
		h.callbackDeleteSession(ctx, b, chatID, userID, param)
	case "wt":
		h.callbackWorktreeChoice(ctx, b, chatID, userID, param)
	case "stoptask":
		taskID, _ := strconv.Atoi(param)
		h.callbackStopTask(ctx, b, chatID, taskID)
	case "mergefix":
		h.callbackMergeFix(ctx, b, chatID, userID, param)
	case "mainq":
		h.callbackMainDirConflict(ctx, b, chatID, userID, param)
	default:
		slog.Warn("unknown callback action", "action", action)
	}
}

func (h *Handlers) callbackSwitch(ctx context.Context, b *bot.Bot, chatID int64, userID int64, instanceID string) {
	inst := h.procMgr.GetInstance(instanceID)
	if inst == nil {
		_, _ = b.SendMessage(ctx, &bot.SendMessageParams{ChatID: chatID, Text: "Instance not found."})
		return
	}

	_ = h.tgState.SetActiveInstance(ctx, userID, instanceID)

	_, _ = b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:    chatID,
		Text:      fmt.Sprintf("Switched to <b>%s</b>", escapeHTML(inst.Name)),
		ParseMode: models.ParseModeHTML,
	})
}

func (h *Handlers) callbackStop(ctx context.Context, b *bot.Bot, chatID int64, userID int64, instanceID string) {
	inst := h.procMgr.GetInstance(instanceID)
	if inst == nil {
		_, _ = b.SendMessage(ctx, &bot.SendMessageParams{ChatID: chatID, Text: "Instance not found."})
		return
	}

	if err := h.procMgr.StopInstance(instanceID); err != nil {
		_, _ = b.SendMessage(ctx, &bot.SendMessageParams{ChatID: chatID, Text: fmt.Sprintf("Failed to stop: %s", err)})
		return
	}

	_ = h.tgState.ClearUserState(ctx, userID, instanceID)

	_, _ = b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:    chatID,
		Text:      fmt.Sprintf("Instance <b>%s</b> stopped.", escapeHTML(inst.Name)),
		ParseMode: models.ParseModeHTML,
	})
}

func (h *Handlers) callbackStart(ctx context.Context, b *bot.Bot, chatID int64, userID int64, instanceID string) {
	inst := h.procMgr.GetInstance(instanceID)
	if inst == nil {
		_, _ = b.SendMessage(ctx, &bot.SendMessageParams{ChatID: chatID, Text: "Instance not found."})
		return
	}

	if inst.Status() == process.StatusRunning {
		_, _ = b.SendMessage(ctx, &bot.SendMessageParams{ChatID: chatID, Text: "Already running."})
		return
	}

	if err := h.procMgr.StartInstance(instanceID); err != nil {
		_, _ = b.SendMessage(ctx, &bot.SendMessageParams{ChatID: chatID, Text: fmt.Sprintf("Failed to start: %s", err)})
		return
	}

	_, _ = b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:    chatID,
		Text:      fmt.Sprintf("Instance <b>%s</b> started.", escapeHTML(inst.Name)),
		ParseMode: models.ParseModeHTML,
	})
}

func (h *Handlers) callbackDelete(ctx context.Context, b *bot.Bot, chatID int64, userID int64, instanceID string) {
	inst := h.procMgr.GetInstance(instanceID)
	if inst == nil {
		_, _ = b.SendMessage(ctx, &bot.SendMessageParams{ChatID: chatID, Text: "Instance not found."})
		return
	}

	name := inst.Name

	if err := h.procMgr.DeleteInstance(instanceID); err != nil {
		_, _ = b.SendMessage(ctx, &bot.SendMessageParams{ChatID: chatID, Text: fmt.Sprintf("Failed to delete: %s", err)})
		return
	}

	_ = h.tgState.ClearUserState(ctx, userID, instanceID)

	_, _ = b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:    chatID,
		Text:      fmt.Sprintf("Instance <b>%s</b> deleted.", escapeHTML(name)),
		ParseMode: models.ParseModeHTML,
	})
}

func (h *Handlers) callbackSession(ctx context.Context, b *bot.Bot, chatID int64, userID int64, sessionID string) {
	_ = h.tgState.SetActiveSession(ctx, userID, sessionID)

	state, _ := h.tgState.GetUserState(ctx, userID)
	inst := h.procMgr.GetInstance(state.ActiveInstanceID)

	instName := "unknown"
	if inst != nil {
		instName = inst.Name
	}

	label := sessionID
	if cs, _ := h.store.GetClaudeSession(state.ActiveInstanceID, sessionID); cs != nil && cs.Title != "" {
		label = fmt.Sprintf("%s (%d msgs)", cs.Title, cs.MessageCount)
	}

	_, _ = b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:    chatID,
		Text:      fmt.Sprintf("<b>[%s]</b> Switched to session: %s", escapeHTML(instName), escapeHTML(label)),
		ParseMode: models.ParseModeHTML,
	})
}

func (h *Handlers) callbackAbort(ctx context.Context, b *bot.Bot, chatID int64, userID int64, sessionID string) {
	inst, err := h.getActiveInstance(ctx, userID)
	if err != nil {
		_, _ = b.SendMessage(ctx, &bot.SendMessageParams{ChatID: chatID, Text: err.Error()})
		return
	}

	if err := inst.Provider.Abort(ctx, sessionID); err != nil {
		_, _ = b.SendMessage(ctx, &bot.SendMessageParams{ChatID: chatID, Text: fmt.Sprintf("Failed to abort: %s", err)})
		return
	}

	h.streamMgr.RemoveStream(sessionID)

	_, _ = b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:    chatID,
		Text:      fmt.Sprintf("<b>[%s]</b> Aborted.", escapeHTML(inst.Name)),
		ParseMode: models.ParseModeHTML,
	})
}

func (h *Handlers) callbackStopTask(ctx context.Context, b *bot.Bot, chatID int64, taskID int) {
	if !h.streamMgr.StopTask(taskID) {
		_, _ = b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID: chatID,
			Text:   fmt.Sprintf("Task #%d not found or already completed.", taskID),
		})
		return
	}
	_, _ = b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID: chatID,
		Text:   fmt.Sprintf("Task #%d stopped.", taskID),
	})
}

func (h *Handlers) callbackMergeFix(ctx context.Context, b *bot.Bot, chatID int64, userID int64, sessionID string) {
	state, _ := h.tgState.GetUserState(ctx, userID)
	activeInstID := ""
	if state != nil {
		activeInstID = state.ActiveInstanceID
	}
	cs, err := h.store.GetClaudeSession(activeInstID, sessionID)
	if err != nil || cs == nil {
		_, _ = b.SendMessage(ctx, &bot.SendMessageParams{ChatID: chatID, Text: "Session not found."})
		return
	}
	if cs.Branch == "" {
		_, _ = b.SendMessage(ctx, &bot.SendMessageParams{ChatID: chatID, Text: "No branch info for this session."})
		return
	}

	inst := h.procMgr.GetInstance(cs.InstanceID)
	if inst == nil {
		_, _ = b.SendMessage(ctx, &bot.SendMessageParams{ChatID: chatID, Text: "Instance not found."})
		return
	}
	if inst.Status() != process.StatusRunning {
		_, _ = b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID: chatID,
			Text:   fmt.Sprintf("Instance '%s' is not running. Use /start_inst %s", inst.Name, inst.Name),
		})
		return
	}

	// Check if main dir is busy before creating a merge-fix session
	if locker, ok := inst.Provider.(provider.MainDirLocker); ok && locker.IsMainDirBusy("") {
		prompt := fmt.Sprintf(
			"Please merge the git branch `%s` into the current branch and resolve any merge conflicts. "+
				"Run `git merge %s`, resolve all conflicts, then commit the result.",
			cs.Branch, cs.Branch,
		)
		title := fmt.Sprintf("Merge fix: %s", cs.Branch)
		h.showMainDirConflict(ctx, b, inst, userID, chatID, 0, prompt, title, "", nil)
		return
	}

	// Create a new session in the main directory (no worktree) to fix the merge
	newSession, err := inst.Provider.CreateSession(ctx, nil)
	if err != nil {
		_, _ = b.SendMessage(ctx, &bot.SendMessageParams{ChatID: chatID, Text: fmt.Sprintf("Failed to create session: %s", err)})
		return
	}

	_ = h.tgState.SetActiveInstance(ctx, userID, inst.ID)
	_ = h.tgState.SetActiveSession(ctx, userID, newSession.ID)

	title := fmt.Sprintf("Merge fix: %s", cs.Branch)
	_ = h.store.UpdateClaudeSessionTitle(inst.ID, newSession.ID, title)

	prompt := fmt.Sprintf(
		"Please merge the git branch `%s` into the current branch and resolve any merge conflicts. "+
			"Run `git merge %s`, resolve all conflicts, then commit the result.",
		cs.Branch, cs.Branch,
	)

	h.startPrompt(ctx, b, inst, chatID, newSession.ID, title, prompt, 0, nil)
}

func (h *Handlers) callbackDeleteSession(ctx context.Context, b *bot.Bot, chatID int64, userID int64, sessionID string) {
	// If deleting the active session, clear it
	state, _ := h.tgState.GetUserState(ctx, userID)
	if state != nil && state.ActiveSessionID == sessionID {
		_ = h.tgState.SetActiveSession(ctx, userID, "")
	}

	instanceID := ""
	if state != nil {
		instanceID = state.ActiveInstanceID
	}

	cs, _ := h.store.GetClaudeSession(instanceID, sessionID)
	label := sessionID
	if cs != nil && cs.Title != "" {
		label = cs.Title
	}

	// Use provider's DeleteSession if available (handles worktree cleanup)
	inst, _ := h.getActiveInstance(ctx, userID)
	if inst != nil {
		if ccp, ok := inst.Provider.(interface {
			DeleteSession(ctx context.Context, sessionID string) error
		}); ok {
			if err := ccp.DeleteSession(ctx, sessionID); err != nil {
				_, _ = b.SendMessage(ctx, &bot.SendMessageParams{ChatID: chatID, Text: fmt.Sprintf("Failed to delete session: %s", err)})
				return
			}
		} else {
			if err := h.store.DeleteClaudeSession(inst.ID, sessionID); err != nil {
				_, _ = b.SendMessage(ctx, &bot.SendMessageParams{ChatID: chatID, Text: fmt.Sprintf("Failed to delete session: %s", err)})
				return
			}
		}
	} else {
		if err := h.store.DeleteClaudeSession(instanceID, sessionID); err != nil {
			_, _ = b.SendMessage(ctx, &bot.SendMessageParams{ChatID: chatID, Text: fmt.Sprintf("Failed to delete session: %s", err)})
			return
		}
	}

	_, _ = b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:    chatID,
		Text:      fmt.Sprintf("Session <b>%s</b> deleted.", escapeHTML(label)),
		ParseMode: models.ParseModeHTML,
	})
}

func (h *Handlers) callbackNewSession(ctx context.Context, b *bot.Bot, chatID int64, userID int64) {
	inst, err := h.getActiveInstance(ctx, userID)
	if err != nil {
		_, _ = b.SendMessage(ctx, &bot.SendMessageParams{ChatID: chatID, Text: err.Error()})
		return
	}

	if inst.Provider.SupportsWorktree() {
		h.showWorktreeChoice(ctx, b, inst, userID, chatID, 0, "", "", nil)
		return
	}

	session, err := inst.Provider.CreateSession(ctx, nil)
	if err != nil {
		_, _ = b.SendMessage(ctx, &bot.SendMessageParams{ChatID: chatID, Text: fmt.Sprintf("Failed to create session: %s", err)})
		return
	}

	_ = h.tgState.SetActiveSession(ctx, userID, session.ID)

	_, _ = b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:    chatID,
		Text:      fmt.Sprintf("<b>[%s]</b> New session: <code>%s</code>", escapeHTML(inst.Name), session.ID),
		ParseMode: models.ParseModeHTML,
	})
}

func (h *Handlers) callbackWorktreeChoice(ctx context.Context, b *bot.Bot, chatID int64, userID int64, mode string) {
	h.pendingMu.Lock()
	pp, ok := h.pendingPrompts[userID]
	if ok {
		delete(h.pendingPrompts, userID)
	}
	h.pendingMu.Unlock()

	if !ok || pp == nil {
		_, _ = b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID: chatID,
			Text:   "No pending task found. Please send your message again.",
		})
		return
	}

	// Delete the choice message
	if pp.choiceMsgID != 0 {
		_, _ = b.DeleteMessage(ctx, &bot.DeleteMessageParams{ChatID: chatID, MessageID: pp.choiceMsgID})
	}

	useWorktree := mode == "worktree"

	// If user chose "Main Directory", check if main dir is busy
	if !useWorktree {
		if locker, ok := pp.inst.Provider.(provider.MainDirLocker); ok && locker.IsMainDirBusy("") {
			h.showMainDirConflict(ctx, b, pp.inst, pp.userID, pp.chatID, pp.replyMsgID, pp.text, pp.titleHint, "", pp.cleanupFiles)
			return
		}
	}

	if pp.text != "" {
		// Prompt pending — create session and run prompt
		h.createSessionAndPrompt(ctx, b, pp.inst, pp.userID, pp.chatID, pp.replyMsgID, pp.text, pp.titleHint, useWorktree, pp.cleanupFiles)
	} else {
		// Pure session creation (from /session new or newsession button)
		opts := &provider.CreateSessionOpts{UseWorktree: useWorktree}
		session, err := pp.inst.Provider.CreateSession(ctx, opts)
		if err != nil {
			_, _ = b.SendMessage(ctx, &bot.SendMessageParams{ChatID: chatID, Text: fmt.Sprintf("Failed to create session: %s", err)})
			return
		}
		_ = h.tgState.SetActiveSession(ctx, pp.userID, session.ID)

		locLabel := "📂 main dir"
		if useWorktree {
			locLabel = "🌿 worktree"
		}
		_, _ = b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:    chatID,
			Text:      fmt.Sprintf("<b>[%s]</b> New session (%s): <code>%s</code>", escapeHTML(pp.inst.Name), locLabel, session.ID),
			ParseMode: models.ParseModeHTML,
		})
	}
}

func (h *Handlers) callbackMainDirConflict(ctx context.Context, b *bot.Bot, chatID int64, userID int64, mode string) {
	h.pendingMu.Lock()
	pp, ok := h.pendingPrompts[userID]
	if ok {
		delete(h.pendingPrompts, userID)
	}
	h.pendingMu.Unlock()

	if !ok || pp == nil {
		_, _ = b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID: chatID,
			Text:   "No pending task found. Please send your message again.",
		})
		return
	}

	// Delete the conflict message
	if pp.choiceMsgID != 0 {
		_, _ = b.DeleteMessage(ctx, &bot.DeleteMessageParams{ChatID: chatID, MessageID: pp.choiceMsgID})
	}

	switch mode {
	case "queue":
		h.queueMainDirPrompt(b, pp)
	case "worktree":
		if pp.text != "" {
			h.createSessionAndPrompt(ctx, b, pp.inst, pp.userID, pp.chatID, pp.replyMsgID, pp.text, pp.titleHint, true, pp.cleanupFiles)
		} else {
			// Pure session creation with worktree
			opts := &provider.CreateSessionOpts{UseWorktree: true}
			session, err := pp.inst.Provider.CreateSession(ctx, opts)
			if err != nil {
				_, _ = b.SendMessage(ctx, &bot.SendMessageParams{ChatID: chatID, Text: fmt.Sprintf("Failed to create session: %s", err)})
				return
			}
			_ = h.tgState.SetActiveSession(ctx, pp.userID, session.ID)
			_, _ = b.SendMessage(ctx, &bot.SendMessageParams{
				ChatID:    chatID,
				Text:      fmt.Sprintf("<b>[%s]</b> New session (🌿 worktree): <code>%s</code>", escapeHTML(pp.inst.Name), session.ID),
				ParseMode: models.ParseModeHTML,
			})
		}
	case "cancel":
		_, _ = b.SendMessage(ctx, &bot.SendMessageParams{ChatID: chatID, Text: "Cancelled."})
		for _, f := range pp.cleanupFiles {
			os.Remove(f)
		}
	}
}
