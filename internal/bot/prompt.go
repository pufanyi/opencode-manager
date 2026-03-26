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
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/pufanyi/opencode-manager/internal/process"
	"github.com/pufanyi/opencode-manager/internal/provider"
	"github.com/pufanyi/opencode-manager/internal/store"
)

func (h *Handlers) HandlePrompt(ctx context.Context, b *bot.Bot, update *models.Update) {
	userID := update.Message.From.ID
	chatID := update.Message.Chat.ID
	text := update.Message.Text

	inst, err := h.getActiveInstance(ctx, userID)
	if err != nil {
		_, _ = b.SendMessage(ctx, &bot.SendMessageParams{ChatID: chatID, Text: err.Error()})
		return
	}

	// Try to continue existing session from reply
	if update.Message.ReplyToMessage != nil {
		if sessionID, _ := h.tgState.GetSessionByMessage(ctx, chatID, update.Message.ReplyToMessage.ID); sessionID != "" {
			// Check main-dir busy for main-dir sessions
			cs, _ := h.store.GetClaudeSession(inst.ID, sessionID)
			if cs != nil && cs.WorktreePath == "" {
				if locker, ok := inst.Provider.(provider.MainDirLocker); ok && locker.IsMainDirBusy(sessionID) {
					h.showMainDirConflict(ctx, b, inst, userID, chatID, update.Message.ID, text, text, sessionID, nil)
					return
				}
			}
			_ = h.tgState.SetActiveSession(ctx, userID, sessionID)
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

	inst, err := h.getActiveInstance(ctx, userID)
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
		if sessionID, _ := h.tgState.GetSessionByMessage(ctx, chatID, update.Message.ReplyToMessage.ID); sessionID != "" {
			// Check main-dir busy for main-dir sessions
			cs, _ := h.store.GetClaudeSession(inst.ID, sessionID)
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
			_ = h.tgState.SetActiveSession(ctx, userID, sessionID)
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
	_ = h.tgState.SetActiveSession(ctx, userID, session.ID)

	title := titleHint
	if len(title) > 60 {
		title = truncateUTF8(title, 60) + "..."
	}
	_ = h.store.UpdateClaudeSessionTitle(inst.ID, session.ID, title)

	h.startPrompt(ctx, b, inst, chatID, session.ID, title, text, replyMsgID, cleanupFiles)
}

// startPrompt starts a prompt stream for an existing session.
// It acquires the main-dir lock for main-dir sessions and registers a
// release callback on the stream.
func (h *Handlers) startPrompt(ctx context.Context, b *bot.Bot, inst *process.Instance, chatID int64, sessionID, sessionTitle, text string, replyMsgID int, cleanupFiles []string) {
	_ = h.store.UpdateClaudeSessionActivity(inst.ID, sessionID)
	_ = h.tgState.SetMessageSession(ctx, chatID, replyMsgID, sessionID)

	// Acquire main-dir lock if this is a main-dir session.
	var releaseFunc func()
	if locker, ok := inst.Provider.(provider.MainDirLocker); ok {
		cs, _ := h.store.GetClaudeSession(inst.ID, sessionID)
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

	// Wrap with Firebase streaming if configured.
	ch = h.procMgr.WrapEventsIfFirebase(sessionID, ch)

	abortFunc := func() { _ = inst.Provider.Abort(context.Background(), sessionID) }
	sc := h.streamMgr.StartStream(b, h.store, h.tgState, chatID, inst.ID, sessionID, inst.Name, sessionTitle, inst.Directory, replyMsgID, ch, promptCancel, abortFunc)
	for _, f := range cleanupFiles {
		sc.AddCleanupFile(f)
	}
	if releaseFunc != nil {
		sc.OnDone(releaseFunc)
	}

	// Persist conversation history.
	promptText := text
	instID := inst.ID
	sc.OnDone(func() {
		// Save user message.
		_ = h.store.SaveMessage(instID, sessionID, &store.Message{
			Role:    "user",
			Content: promptText,
		})
		// Save assistant response.
		responseText, toolCalls := sc.Result()
		if responseText != "" || len(toolCalls) > 0 {
			_ = h.store.SaveMessage(instID, sessionID, &store.Message{
				Role:      "assistant",
				Content:   responseText,
				ToolCalls: toolCalls,
			})
		}
	})
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
			if cs, _ := h.store.GetClaudeSession(pp.inst.ID, pp.sessionID); cs != nil && cs.Title != "" {
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
						if cs, _ := h.store.GetClaudeSession(pp.inst.ID, pp.sessionID); cs != nil && cs.Title != "" {
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
