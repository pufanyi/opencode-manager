package bot

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/pufanyi/opencode-manager/internal/firebase"
	"github.com/pufanyi/opencode-manager/internal/gitops"
	"github.com/pufanyi/opencode-manager/internal/provider"
	"github.com/pufanyi/opencode-manager/internal/store"
)

const (
	maxMessageLen   = 4096
	fileFallbackLen = 12000
)

// toolStatus tracks a single tool invocation's display state.
type toolStatus struct {
	Name   string
	State  string // "running", "completed", "error"
	Detail string // Short description (e.g., Agent task description)
}

// StreamContext manages streaming provider events to a Telegram message.
type StreamContext struct {
	mu               sync.Mutex
	b                *bot.Bot
	store            store.Store
	tgState          *firebase.TelegramState
	chatID           int64
	instanceID       string
	sessionID        string
	instanceName     string
	sessionTitle     string
	workDir          string // instance working directory for git merge-back
	location         string // board display: "🌿 worktree" or "📂 main dir"
	replyToMessageID int    // original user message ID for Telegram reply
	startedAt        time.Time
	manager          *StreamManager
	taskID           int

	// Content tracked separately: text + tools
	textContent string
	tools       []toolStatus

	// Merge failure info (from provider's merge_failed event)
	mergeError  string
	mergeBranch string

	dirty           bool
	done            bool
	superseded      bool // true if replaced by a newer stream for the same session
	cancel          context.CancelFunc
	promptCancel    context.CancelFunc
	abortFunc       func()
	cleanupFiles    []string // temp files to remove when stream ends
	onDoneCallbacks []func()
}

// AddCleanupFile registers a temp file to be deleted when the stream ends.
func (sc *StreamContext) AddCleanupFile(path string) {
	sc.mu.Lock()
	sc.cleanupFiles = append(sc.cleanupFiles, path)
	sc.mu.Unlock()
}

// OnDone registers a callback to run when the stream finishes normally
// (not when superseded by a newer stream for the same session).
func (sc *StreamContext) OnDone(fn func()) {
	sc.mu.Lock()
	sc.onDoneCallbacks = append(sc.onDoneCallbacks, fn)
	sc.mu.Unlock()
}

// Result returns the accumulated response text and tool calls.
// Safe to call from OnDone callbacks (lock is not held).
func (sc *StreamContext) Result() (text string, tools []store.ToolCall) {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	result := make([]store.ToolCall, len(sc.tools))
	for i, t := range sc.tools {
		result[i] = store.ToolCall{
			Name:   t.Name,
			Status: t.State,
			Detail: t.Detail,
		}
	}
	return sc.textContent, result
}

// MarkSuperseded marks this stream as replaced by a newer one. Its OnDone
// callbacks will be skipped during cleanup.
func (sc *StreamContext) MarkSuperseded() {
	sc.mu.Lock()
	sc.superseded = true
	sc.mu.Unlock()
}

// consumeStream reads from the provider's event channel and accumulates content.
func (sc *StreamContext) consumeStream(ctx context.Context, ch <-chan provider.StreamEvent) {
	for {
		select {
		case <-ctx.Done():
			return
		case evt, ok := <-ch:
			if !ok {
				sc.mu.Lock()
				sc.done = true
				sc.dirty = true
				sc.mu.Unlock()
				return
			}

			sc.mu.Lock()
			switch evt.Type {
			case "text":
				sc.textContent = evt.Text
				sc.dirty = true
			case "tool_use":
				sc.updateTool(evt.ToolName, evt.ToolState, evt.ToolDetail)
				sc.dirty = true
			case "done":
				sc.done = true
				sc.dirty = true
			case "error":
				sc.textContent = fmt.Sprintf("Error: %s", evt.Error)
				sc.done = true
				sc.dirty = true
			case "merge_failed":
				sc.mergeError = evt.Error
				sc.mergeBranch = evt.MergeBranch
				sc.dirty = true
			}
			sc.mu.Unlock()
		}
	}
}

// updateTool adds or updates a tool's status and detail.
func (sc *StreamContext) updateTool(name, state, detail string) {
	for i := range sc.tools {
		if sc.tools[i].Name == name && sc.tools[i].State == "running" {
			sc.tools[i].State = state
			if detail != "" {
				sc.tools[i].Detail = detail
			}
			return
		}
	}
	sc.tools = append(sc.tools, toolStatus{Name: name, State: state, Detail: detail})
}

func (sc *StreamContext) flushLoop(ctx context.Context, semaphore chan struct{}) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	defer sc.cleanup()
	defer sc.manager.onStreamDone(sc.sessionID)

	for {
		select {
		case <-ctx.Done():
			// Aborted — don't send partial response
			return
		case <-ticker.C:
			if sc.isDone() {
				sc.sendResponse(semaphore)
				if sc.hasMergeError() {
					sc.sendMergeFailureNotification()
				} else {
					sc.mergeBack()
				}
				return
			}
		}
	}
}

// mergeBack attempts to merge the worktree branch back to main after prompt completion.
func (sc *StreamContext) mergeBack() {
	if sc.workDir == "" {
		return
	}

	result := gitops.MergeBack(sc.workDir)
	if result == nil {
		return
	}

	slog.Info("merge-back result", "instance", sc.instanceName, "merged", result.Merged, "message", result.Message)

	if !result.Merged {
		// Only notify if there was something noteworthy (not "no new commits" etc.)
		if result.Branch != "" && result.Message != "no new commits to merge" && result.Message != "already on main branch" {
			sc.sendMergeNotification(fmt.Sprintf("⚠️ %s", result.Message))
		}
		return
	}

	sc.sendMergeNotification(fmt.Sprintf("✅ %s", result.Message))
}

func (sc *StreamContext) sendMergeNotification(text string) {
	msg := fmt.Sprintf("<b>[%s]</b> Git: %s", escapeHTML(sc.instanceName), escapeHTML(text))
	_, err := sc.b.SendMessage(context.Background(), &bot.SendMessageParams{
		ChatID:    sc.chatID,
		Text:      msg,
		ParseMode: models.ParseModeHTML,
	})
	if err != nil {
		slog.Warn("failed to send merge notification", "error", err)
	}
	sc.manager.NotifyNewMessage(sc.chatID)
}

// hasMergeError returns true if the provider reported a merge failure.
func (sc *StreamContext) hasMergeError() bool {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	return sc.mergeError != ""
}

// sendMergeFailureNotification sends a Telegram message about the merge failure
// with an inline button that lets the user ask Claude Code to fix it.
func (sc *StreamContext) sendMergeFailureNotification() {
	sc.mu.Lock()
	errMsg := sc.mergeError
	branch := sc.mergeBranch
	sc.mu.Unlock()

	// Truncate long error messages
	if len(errMsg) > 300 {
		errMsg = errMsg[:300] + "..."
	}

	text := fmt.Sprintf(
		"<b>[%s]</b> ⚠️ Auto-merge failed for branch <code>%s</code>:\n%s",
		escapeHTML(sc.instanceName), escapeHTML(branch), escapeHTML(errMsg),
	)

	keyboard := &models.InlineKeyboardMarkup{
		InlineKeyboard: [][]models.InlineKeyboardButton{
			{
				{Text: "🔧 Fix with Claude", CallbackData: fmt.Sprintf("mergefix:%s", sc.sessionID)},
			},
		},
	}

	_, err := sc.b.SendMessage(context.Background(), &bot.SendMessageParams{
		ChatID:      sc.chatID,
		Text:        text,
		ParseMode:   models.ParseModeHTML,
		ReplyMarkup: keyboard,
	})
	if err != nil {
		slog.Warn("failed to send merge failure notification", "error", err)
	}
	sc.manager.NotifyNewMessage(sc.chatID)
}

// cleanup removes temp files and runs OnDone callbacks (unless superseded).
func (sc *StreamContext) cleanup() {
	sc.mu.Lock()
	files := sc.cleanupFiles
	sc.cleanupFiles = nil
	callbacks := sc.onDoneCallbacks
	sc.onDoneCallbacks = nil
	superseded := sc.superseded
	sc.mu.Unlock()

	for _, f := range files {
		if err := os.Remove(f); err != nil && !os.IsNotExist(err) {
			slog.Warn("failed to clean up temp file", "path", f, "error", err)
		} else {
			slog.Debug("cleaned up temp file", "path", f)
		}
	}

	// Run OnDone callbacks only if this stream was not superseded by a
	// newer stream for the same session.
	if !superseded {
		for _, fn := range callbacks {
			fn()
		}
	}
}

func (sc *StreamContext) isDone() bool {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	return sc.done
}

// sendResponse builds the final message content and sends it as a reply.
func (sc *StreamContext) sendResponse(semaphore chan struct{}) {
	sc.mu.Lock()
	text := sc.textContent
	sc.mu.Unlock()

	// --- Build HTML content (final response — no tools, board handles that) ---
	header := fmt.Sprintf("<b>[%s]</b>", escapeHTML(sc.instanceName))

	var renderedText string
	if text != "" {
		renderedText = markdownToTelegramHTML(text)
		if renderedText == "" {
			renderedText = escapeHTML(text)
		}
	}

	fullContent := header
	if renderedText != "" {
		fullContent += "\n\n" + renderedText
	} else {
		fullContent += " Done."
	}

	// --- Build plain-text fallback ---
	rawContent := text
	if rawContent == "" {
		rawContent = "Done."
	}

	// --- Dispatch based on size ---
	if len(fullContent) > fileFallbackLen {
		sc.sendAsFile(rawContent)
		return
	}
	if len(fullContent) > maxMessageLen {
		sc.sendSplitResponse(fullContent, rawContent, header, semaphore)
		return
	}
	sc.sendSingleMessage(fullContent, rawContent, semaphore)
}

// sendSingleMessage sends the final response as a single Telegram message.
func (sc *StreamContext) sendSingleMessage(fullContent, rawContent string, semaphore chan struct{}) {
	semaphore <- struct{}{}
	defer func() { <-semaphore }()

	params := &bot.SendMessageParams{
		ChatID:    sc.chatID,
		Text:      fullContent,
		ParseMode: models.ParseModeHTML,
	}
	if sc.replyToMessageID != 0 {
		params.ReplyParameters = &models.ReplyParameters{
			MessageID:                sc.replyToMessageID,
			AllowSendingWithoutReply: true,
		}
	}

	msg, err := sc.b.SendMessage(context.Background(), params)
	if err != nil {
		slog.Warn("send response with HTML failed, retrying plain text", "error", err)
		params.ParseMode = ""
		params.Text = fmt.Sprintf("[%s]\n\n%s", sc.instanceName, rawContent)
		if len(params.Text) > maxMessageLen {
			params.Text = truncateUTF8(params.Text, maxMessageLen-10) + "\n..."
		}
		msg, err = sc.b.SendMessage(context.Background(), params)
		if err != nil {
			slog.Error("send response failed completely", "error", err)
			return
		}
	}

	if sc.tgState != nil && msg != nil {
		_ = sc.tgState.SetMessageSession(context.Background(), sc.chatID, msg.ID, sc.sessionID)
	}
	sc.manager.NotifyNewMessage(sc.chatID)
}

// sendSplitResponse sends a long response as two messages.
func (sc *StreamContext) sendSplitResponse(fullContent, rawContent, header string, semaphore chan struct{}) {
	available := maxMessageLen - len(header) - 20
	body := fullContent[len(header):]
	if len(body) > available {
		body = truncateHTML(body, available)
	}
	first := header + body + "\n..."

	// First chunk
	semaphore <- struct{}{}
	firstParams := &bot.SendMessageParams{
		ChatID:    sc.chatID,
		Text:      first,
		ParseMode: models.ParseModeHTML,
	}
	if sc.replyToMessageID != 0 {
		firstParams.ReplyParameters = &models.ReplyParameters{
			MessageID:                sc.replyToMessageID,
			AllowSendingWithoutReply: true,
		}
	}
	msg, err := sc.b.SendMessage(context.Background(), firstParams)
	<-semaphore
	if err != nil {
		slog.Warn("send first chunk failed, sending as file", "error", err)
		sc.sendAsFile(rawContent)
		return
	}
	if sc.tgState != nil {
		_ = sc.tgState.SetMessageSession(context.Background(), sc.chatID, msg.ID, sc.sessionID)
	}

	// Continuation
	remaining := fullContent[len(header)+len(body):]
	if remaining == "" {
		return
	}
	contText := header + "\n" + remaining
	if len(contText) > maxMessageLen {
		contText = truncateHTML(contText, maxMessageLen-20) + "\n..."
	}

	semaphore <- struct{}{}
	contParams := &bot.SendMessageParams{
		ChatID:    sc.chatID,
		Text:      contText,
		ParseMode: models.ParseModeHTML,
	}
	msg2, err := sc.b.SendMessage(context.Background(), contParams)
	<-semaphore
	if err != nil {
		slog.Warn("send continuation failed", "error", err)
		return
	}
	if sc.tgState != nil {
		_ = sc.tgState.SetMessageSession(context.Background(), sc.chatID, msg2.ID, sc.sessionID)
	}
	sc.manager.NotifyNewMessage(sc.chatID)
}

func (sc *StreamContext) sendAsFile(content string) {
	fileData := &models.InputFileUpload{
		Filename: "response.md",
		Data:     strings.NewReader(content),
	}

	docParams := &bot.SendDocumentParams{
		ChatID:    sc.chatID,
		Document:  fileData,
		Caption:   fmt.Sprintf("<b>[%s]</b> Response too long, sent as file.", escapeHTML(sc.instanceName)),
		ParseMode: models.ParseModeHTML,
	}
	if sc.replyToMessageID != 0 {
		docParams.ReplyParameters = &models.ReplyParameters{
			MessageID:                sc.replyToMessageID,
			AllowSendingWithoutReply: true,
		}
	}
	_, err := sc.b.SendDocument(context.Background(), docParams)
	if err != nil {
		slog.Error("failed to send file", "error", err)
	}
	sc.manager.NotifyNewMessage(sc.chatID)
}
