package bot

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/pufanyi/opencode-manager/internal/process"
	"github.com/pufanyi/opencode-manager/internal/provider"
	"github.com/pufanyi/opencode-manager/internal/store"
)

// pendingPrompt stores a prompt waiting for the user's worktree/conflict choice.
type pendingPrompt struct {
	text         string // prompt content (empty for pure session creation)
	inst         *process.Instance
	userID       int64
	chatID       int64
	replyMsgID   int
	titleHint    string
	cleanupFiles []string // temp files to remove on discard
	choiceMsgID  int      // ID of the "where to work?" / conflict message
	sessionID    string   // non-empty when continuing an existing session
}

type Handlers struct {
	procMgr   *process.Manager
	store     *store.Store
	streamMgr *StreamManager

	pendingMu      sync.Mutex
	pendingPrompts map[int64]*pendingPrompt // userID -> pending
}

func NewHandlers(procMgr *process.Manager, st *store.Store, streamMgr *StreamManager) *Handlers {
	return &Handlers{
		procMgr:        procMgr,
		store:          st,
		streamMgr:      streamMgr,
		pendingPrompts: make(map[int64]*pendingPrompt),
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
	chatID := update.Message.Chat.ID
	text := update.Message.Text

	inst, err := h.getActiveInstance(userID)
	if err != nil {
		_, _ = b.SendMessage(ctx, &bot.SendMessageParams{ChatID: chatID, Text: err.Error()})
		return
	}

	// Try to continue existing session from reply
	if update.Message.ReplyToMessage != nil {
		if sessionID, _ := h.store.GetSessionByMessage(chatID, update.Message.ReplyToMessage.ID); sessionID != "" {
			// Check main-dir busy for main-dir sessions
			cs, _ := h.store.GetClaudeSession(sessionID)
			if cs != nil && cs.WorktreePath == "" {
				if locker, ok := inst.Provider.(provider.MainDirLocker); ok && locker.IsMainDirBusy(sessionID) {
					h.showMainDirConflict(ctx, b, inst, userID, chatID, update.Message.ID, text, text, sessionID, nil)
					return
				}
			}
			_ = h.store.SetActiveSession(userID, sessionID)
			title := ""
			if cs != nil {
				title = cs.Title
			}
			h.startPrompt(ctx, b, inst, chatID, sessionID, title, text, update.Message.ID, nil)
			return
		}
	}

	// New session needed — ask about worktree if supported
	if inst.Provider.SupportsWorktree() {
		h.showWorktreeChoice(ctx, b, inst, userID, chatID, update.Message.ID, text, text, nil)
		return
	}

	// No worktree support — create session directly
	h.createSessionAndPrompt(ctx, b, inst, userID, chatID, update.Message.ID, text, text, false, nil)
}

func (h *Handlers) HandlePhoto(ctx context.Context, b *bot.Bot, update *models.Update) {
	userID := update.Message.From.ID
	chatID := update.Message.Chat.ID
	caption := update.Message.Caption

	inst, err := h.getActiveInstance(userID)
	if err != nil {
		_, _ = b.SendMessage(ctx, &bot.SendMessageParams{ChatID: chatID, Text: err.Error()})
		return
	}

	titleHint := caption
	if titleHint == "" {
		titleHint = "(image)"
	}

	// Try to continue existing session from reply
	if update.Message.ReplyToMessage != nil {
		if sessionID, _ := h.store.GetSessionByMessage(chatID, update.Message.ReplyToMessage.ID); sessionID != "" {
			// Check main-dir busy for main-dir sessions
			cs, _ := h.store.GetClaudeSession(sessionID)
			if cs != nil && cs.WorktreePath == "" {
				if locker, ok := inst.Provider.(provider.MainDirLocker); ok && locker.IsMainDirBusy(sessionID) {
					localPath, prompt, photoErr := h.buildPhotoPrompt(ctx, b, update, inst, caption)
					if photoErr != nil {
						_, _ = b.SendMessage(ctx, &bot.SendMessageParams{ChatID: chatID, Text: photoErr.Error()})
						return
					}
					h.showMainDirConflict(ctx, b, inst, userID, chatID, update.Message.ID, prompt, titleHint, sessionID, []string{localPath})
					return
				}
			}
			_ = h.store.SetActiveSession(userID, sessionID)
			title := ""
			if cs != nil {
				title = cs.Title
			}
			localPath, prompt, err := h.buildPhotoPrompt(ctx, b, update, inst, caption)
			if err != nil {
				_, _ = b.SendMessage(ctx, &bot.SendMessageParams{ChatID: chatID, Text: err.Error()})
				return
			}
			h.startPrompt(ctx, b, inst, chatID, sessionID, title, prompt, update.Message.ID, []string{localPath})
			return
		}
	}

	// Download photo first (needed regardless of worktree choice)
	localPath, prompt, err := h.buildPhotoPrompt(ctx, b, update, inst, caption)
	if err != nil {
		_, _ = b.SendMessage(ctx, &bot.SendMessageParams{ChatID: chatID, Text: err.Error()})
		return
	}

	// New session needed — ask about worktree if supported
	if inst.Provider.SupportsWorktree() {
		h.showWorktreeChoice(ctx, b, inst, userID, chatID, update.Message.ID, prompt, titleHint, []string{localPath})
		return
	}

	// No worktree support — create session directly
	h.createSessionAndPrompt(ctx, b, inst, userID, chatID, update.Message.ID, prompt, titleHint, false, []string{localPath})
}

// buildPhotoPrompt downloads the photo and builds the prompt text.
func (h *Handlers) buildPhotoPrompt(ctx context.Context, b *bot.Bot, update *models.Update, inst *process.Instance, caption string) (string, string, error) {
	photos := update.Message.Photo
	photo := photos[len(photos)-1]

	localPath, err := h.downloadTelegramFile(ctx, b, photo.FileID, inst.ID)
	if err != nil {
		return "", "", fmt.Errorf("failed to download image: %s", err)
	}

	var prompt string
	if caption != "" {
		prompt = fmt.Sprintf("%s\n\nImage file: %s", caption, localPath)
	} else {
		prompt = fmt.Sprintf("Please analyze this image.\n\nImage file: %s", localPath)
	}
	return localPath, prompt, nil
}

// downloadTelegramFile downloads a Telegram file to /tmp/opencode-manager/{instanceID}/images/.
func (h *Handlers) downloadTelegramFile(ctx context.Context, b *bot.Bot, fileID, instanceID string) (string, error) {
	file, err := b.GetFile(ctx, &bot.GetFileParams{FileID: fileID})
	if err != nil {
		return "", fmt.Errorf("getFile: %w", err)
	}

	downloadURL := b.FileDownloadLink(file)

	// Determine extension from Telegram's file path
	ext := filepath.Ext(file.FilePath)
	if ext == "" {
		ext = ".jpg"
	}

	// Create temp directory
	dir := filepath.Join(os.TempDir(), "opencode-manager", instanceID, "images")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("mkdir: %w", err)
	}

	// Generate filename: timestamp-random
	var randBytes [4]byte
	_, _ = rand.Read(randBytes[:])
	filename := fmt.Sprintf("%s-%s%s", time.Now().Format("20060102-150405"), hex.EncodeToString(randBytes[:]), ext)
	localPath := filepath.Join(dir, filename)

	// Download
	resp, err := http.Get(downloadURL)
	if err != nil {
		return "", fmt.Errorf("download: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download returned %d", resp.StatusCode)
	}

	out, err := os.Create(localPath)
	if err != nil {
		return "", fmt.Errorf("create file: %w", err)
	}
	defer out.Close()

	if _, err := io.Copy(out, resp.Body); err != nil {
		os.Remove(localPath)
		return "", fmt.Errorf("write file: %w", err)
	}

	slog.Info("downloaded telegram photo", "path", localPath, "size", file.FileSize)
	return localPath, nil
}

// showWorktreeChoice stores a pending prompt and shows the worktree choice keyboard.
func (h *Handlers) showWorktreeChoice(ctx context.Context, b *bot.Bot, inst *process.Instance, userID, chatID int64, replyMsgID int, text, titleHint string, cleanupFiles []string) {
	h.pendingMu.Lock()
	old := h.pendingPrompts[userID]
	pp := &pendingPrompt{
		text:         text,
		inst:         inst,
		userID:       userID,
		chatID:       chatID,
		replyMsgID:   replyMsgID,
		titleHint:    titleHint,
		cleanupFiles: cleanupFiles,
	}
	h.pendingPrompts[userID] = pp
	h.pendingMu.Unlock()

	// Clean up old pending if any
	if old != nil {
		if old.choiceMsgID != 0 {
			_, _ = b.DeleteMessage(ctx, &bot.DeleteMessageParams{ChatID: old.chatID, MessageID: old.choiceMsgID})
		}
		for _, f := range old.cleanupFiles {
			os.Remove(f)
		}
	}

	label := "New task"
	if text == "" {
		label = "New session"
	}
	params := &bot.SendMessageParams{
		ChatID:      chatID,
		Text:        fmt.Sprintf("<b>[%s]</b> %s — where should changes be made?", escapeHTML(inst.Name), label),
		ParseMode:   models.ParseModeHTML,
		ReplyMarkup: worktreeChoiceKeyboard(),
	}
	if replyMsgID != 0 {
		params.ReplyParameters = &models.ReplyParameters{
			MessageID:                replyMsgID,
			AllowSendingWithoutReply: true,
		}
	}
	msg, _ := b.SendMessage(ctx, params)
	if msg != nil {
		h.pendingMu.Lock()
		if current, ok := h.pendingPrompts[userID]; ok && current == pp {
			current.choiceMsgID = msg.ID
		}
		h.pendingMu.Unlock()
	}
}

// createSessionAndPrompt creates a new session and starts the prompt.
func (h *Handlers) createSessionAndPrompt(ctx context.Context, b *bot.Bot, inst *process.Instance, userID, chatID int64, replyMsgID int, text, titleHint string, useWorktree bool, cleanupFiles []string) {
	opts := &provider.CreateSessionOpts{UseWorktree: useWorktree}
	session, err := inst.Provider.CreateSession(ctx, opts)
	if err != nil {
		_, _ = b.SendMessage(ctx, &bot.SendMessageParams{ChatID: chatID, Text: "Failed to create session."})
		for _, f := range cleanupFiles {
			os.Remove(f)
		}
		return
	}
	_ = h.store.SetActiveSession(userID, session.ID)

	title := titleHint
	if len(title) > 60 {
		title = truncateUTF8(title, 60) + "..."
	}
	_ = h.store.UpdateClaudeSessionTitle(session.ID, title)

	h.startPrompt(ctx, b, inst, chatID, session.ID, title, text, replyMsgID, cleanupFiles)
}

// startPrompt starts a prompt stream for an existing session.
// It acquires the main-dir lock for main-dir sessions and registers a
// release callback on the stream.
func (h *Handlers) startPrompt(ctx context.Context, b *bot.Bot, inst *process.Instance, chatID int64, sessionID, sessionTitle, text string, replyMsgID int, cleanupFiles []string) {
	_ = h.store.UpdateClaudeSessionActivity(sessionID)
	_ = h.store.SetMessageSession(chatID, replyMsgID, sessionID)

	// Acquire main-dir lock if this is a main-dir session.
	var releaseFunc func()
	if locker, ok := inst.Provider.(provider.MainDirLocker); ok {
		cs, _ := h.store.GetClaudeSession(sessionID)
		if cs != nil && cs.WorktreePath == "" {
			if !locker.TryAcquireMainDir(sessionID) {
				slog.Warn("main dir busy at startPrompt (race)", "session", sessionID)
			} else {
				sid := sessionID
				releaseFunc = func() { locker.ReleaseMainDir(sid) }
			}
		}
	}

	promptCtx, promptCancel := context.WithCancel(context.Background())
	ch, err := inst.Provider.Prompt(promptCtx, sessionID, text)
	if err != nil {
		promptCancel()
		if releaseFunc != nil {
			releaseFunc()
		}
		_, _ = b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:    chatID,
			Text:      fmt.Sprintf("<b>[%s]</b> Failed to send prompt: %s", escapeHTML(inst.Name), err),
			ParseMode: models.ParseModeHTML,
		})
		for _, f := range cleanupFiles {
			os.Remove(f)
		}
		return
	}

	abortFunc := func() { _ = inst.Provider.Abort(context.Background(), sessionID) }
	sc := h.streamMgr.StartStream(b, h.store, chatID, sessionID, inst.Name, sessionTitle, inst.Directory, replyMsgID, ch, promptCancel, abortFunc)
	for _, f := range cleanupFiles {
		sc.AddCleanupFile(f)
	}
	if releaseFunc != nil {
		sc.OnDone(releaseFunc)
	}
}

// showMainDirConflict stores a pending prompt and shows the main-dir conflict keyboard.
func (h *Handlers) showMainDirConflict(ctx context.Context, b *bot.Bot, inst *process.Instance, userID, chatID int64, replyMsgID int, text, titleHint, sessionID string, cleanupFiles []string) {
	h.pendingMu.Lock()
	old := h.pendingPrompts[userID]
	pp := &pendingPrompt{
		text:         text,
		inst:         inst,
		userID:       userID,
		chatID:       chatID,
		replyMsgID:   replyMsgID,
		titleHint:    titleHint,
		sessionID:    sessionID,
		cleanupFiles: cleanupFiles,
	}
	h.pendingPrompts[userID] = pp
	h.pendingMu.Unlock()

	// Clean up old pending if any
	if old != nil {
		if old.choiceMsgID != 0 {
			_, _ = b.DeleteMessage(ctx, &bot.DeleteMessageParams{ChatID: old.chatID, MessageID: old.choiceMsgID})
		}
		for _, f := range old.cleanupFiles {
			os.Remove(f)
		}
	}

	params := &bot.SendMessageParams{
		ChatID:      chatID,
		Text:        fmt.Sprintf("<b>[%s]</b> Main directory is busy. What would you like to do?", escapeHTML(inst.Name)),
		ParseMode:   models.ParseModeHTML,
		ReplyMarkup: mainDirConflictKeyboard(),
	}
	if replyMsgID != 0 {
		params.ReplyParameters = &models.ReplyParameters{
			MessageID:                replyMsgID,
			AllowSendingWithoutReply: true,
		}
	}
	msg, _ := b.SendMessage(ctx, params)
	if msg != nil {
		h.pendingMu.Lock()
		if current, ok := h.pendingPrompts[userID]; ok && current == pp {
			current.choiceMsgID = msg.ID
		}
		h.pendingMu.Unlock()
	}
}

// queueMainDirPrompt waits for the main directory to become free, then
// creates a session and starts the prompt.
func (h *Handlers) queueMainDirPrompt(b *bot.Bot, pp *pendingPrompt) {
	locker, ok := pp.inst.Provider.(provider.MainDirLocker)
	if !ok {
		// No locking support — just proceed immediately
		if pp.sessionID != "" {
			title := pp.titleHint
			if cs, _ := h.store.GetClaudeSession(pp.sessionID); cs != nil && cs.Title != "" {
				title = cs.Title
			}
			h.startPrompt(context.Background(), b, pp.inst, pp.chatID, pp.sessionID, title, pp.text, pp.replyMsgID, pp.cleanupFiles)
		} else {
			h.createSessionAndPrompt(context.Background(), b, pp.inst, pp.userID, pp.chatID, pp.replyMsgID, pp.text, pp.titleHint, false, pp.cleanupFiles)
		}
		return
	}

	_, _ = b.SendMessage(context.Background(), &bot.SendMessageParams{
		ChatID:    pp.chatID,
		Text:      fmt.Sprintf("<b>[%s]</b> Queued — waiting for main directory...", escapeHTML(pp.inst.Name)),
		ParseMode: models.ParseModeHTML,
	})

	go func() {
		timeout := time.After(10 * time.Minute)
		for {
			freeCh := locker.WaitMainDirFree()
			select {
			case <-freeCh:
				sid := pp.sessionID
				if !locker.IsMainDirBusy(sid) {
					if pp.sessionID != "" {
						title := pp.titleHint
						if cs, _ := h.store.GetClaudeSession(pp.sessionID); cs != nil && cs.Title != "" {
							title = cs.Title
						}
						h.startPrompt(context.Background(), b, pp.inst, pp.chatID, pp.sessionID, title, pp.text, pp.replyMsgID, pp.cleanupFiles)
					} else {
						h.createSessionAndPrompt(context.Background(), b, pp.inst, pp.userID, pp.chatID, pp.replyMsgID, pp.text, pp.titleHint, false, pp.cleanupFiles)
					}
					return
				}
				// Another waiter grabbed it — wait again
			case <-timeout:
				_, _ = b.SendMessage(context.Background(), &bot.SendMessageParams{
					ChatID:    pp.chatID,
					Text:      fmt.Sprintf("<b>[%s]</b> Queue timeout — main directory was not freed in time.", escapeHTML(pp.inst.Name)),
					ParseMode: models.ParseModeHTML,
				})
				for _, f := range pp.cleanupFiles {
					os.Remove(f)
				}
				return
			}
		}
	}()
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
