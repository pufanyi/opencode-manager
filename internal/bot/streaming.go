package bot

import (
	"context"
	"fmt"
	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/pufanyi/opencode-manager/internal/gitops"
	"github.com/pufanyi/opencode-manager/internal/provider"
	"github.com/pufanyi/opencode-manager/internal/store"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"
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

// boardEntry holds snapshot data for one stream in the status board.
type boardEntry struct {
	taskID       int
	instanceName string
	sessionTitle string
	location     string       // e.g. "🌿 worktree" or "📂 main dir"
	tools        []toolStatus // recent tools for display (completed + running)
	elapsed      time.Duration
	hiddenTools  int // number of completed tools not shown
}

// StreamContext manages streaming provider events to a Telegram message.
type StreamContext struct {
	mu               sync.Mutex
	b                *bot.Bot
	store            store.Store
	chatID           int64
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

// StreamManager handles all active streams and global rate limiting.
type StreamManager struct {
	mu        sync.Mutex
	streams   map[string]*StreamContext // keyed by sessionID
	taskMap   map[int]*StreamContext    // keyed by taskID
	semaphore chan struct{}
	nextID    int

	// Status board
	b             *bot.Bot
	boardMu       sync.Mutex
	boardMsgs     map[int64]int    // chatID -> board message ID
	boardContent  map[int64]string // chatID -> last sent content
	boardRepos    map[int64]bool   // chatID -> needs reposition (new msg appeared)
	boardStarted  bool
	boardInterval time.Duration
}

func NewStreamManager(boardInterval time.Duration) *StreamManager {
	if boardInterval <= 0 {
		boardInterval = 2 * time.Second
	}
	return &StreamManager{
		streams:       make(map[string]*StreamContext),
		taskMap:       make(map[int]*StreamContext),
		semaphore:     make(chan struct{}, 25),
		boardMsgs:     make(map[int64]int),
		boardContent:  make(map[int64]string),
		boardRepos:    make(map[int64]bool),
		boardInterval: boardInterval,
	}
}

func (sm *StreamManager) StartStream(b *bot.Bot, st store.Store, chatID int64, sessionID, instanceName, sessionTitle, workDir string, replyToMessageID int, ch <-chan provider.StreamEvent, promptCancel context.CancelFunc, abortFunc func()) *StreamContext {
	ctx, cancel := context.WithCancel(context.Background())

	sm.mu.Lock()
	sm.nextID++
	taskID := sm.nextID

	if old, ok := sm.streams[sessionID]; ok {
		old.MarkSuperseded()
		old.cancel()
		delete(sm.taskMap, old.taskID)
	}

	sc := &StreamContext{
		b:                b,
		store:            st,
		chatID:           chatID,
		sessionID:        sessionID,
		instanceName:     instanceName,
		sessionTitle:     sessionTitle,
		workDir:          workDir,
		replyToMessageID: replyToMessageID,
		startedAt:        time.Now(),
		manager:          sm,
		taskID:           taskID,
		cancel:           cancel,
		promptCancel:     promptCancel,
		abortFunc:        abortFunc,
	}

	// Look up session location for board display
	if st != nil {
		if cs, err := st.GetClaudeSession(sessionID); err == nil && cs != nil {
			if cs.Branch != "" {
				sc.location = "🌿 worktree"
			} else {
				sc.location = "📂 main dir"
			}
		}
	}

	sm.streams[sessionID] = sc
	sm.taskMap[taskID] = sc

	if !sm.boardStarted {
		sm.b = b
		sm.boardStarted = true
		go sm.boardLoop()
	}
	sm.mu.Unlock()

	go sc.consumeStream(ctx, ch)
	go sc.flushLoop(ctx, sm.semaphore)

	// New message (user prompt) appeared — reposition board to bottom
	sm.NotifyNewMessage(chatID)
	go sm.refreshBoard()

	return sc
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

// RemoveStream cancels and removes a stream by sessionID (used by /abort).
func (sm *StreamManager) RemoveStream(sessionID string) {
	sm.mu.Lock()
	if sc, ok := sm.streams[sessionID]; ok {
		sc.cancel()
		if sc.promptCancel != nil {
			sc.promptCancel()
		}
		delete(sm.taskMap, sc.taskID)
		delete(sm.streams, sessionID)
	}
	sm.mu.Unlock()
	go sm.refreshBoard()
}

// StopTask cancels and removes a stream by taskID (used by board stop buttons).
func (sm *StreamManager) StopTask(taskID int) bool {
	sm.mu.Lock()
	sc, ok := sm.taskMap[taskID]
	if !ok {
		sm.mu.Unlock()
		return false
	}
	sc.cancel()
	promptCancel := sc.promptCancel
	abortFn := sc.abortFunc
	delete(sm.streams, sc.sessionID)
	delete(sm.taskMap, taskID)
	sm.mu.Unlock()

	if promptCancel != nil {
		promptCancel()
	}
	if abortFn != nil {
		abortFn()
	}
	go sm.refreshBoard()
	return true
}

// NotifyNewMessage marks a chat as needing the board repositioned to the bottom.
// Call this whenever a new message is sent to the chat (response, merge notification, etc.).
func (sm *StreamManager) NotifyNewMessage(chatID int64) {
	sm.boardMu.Lock()
	sm.boardRepos[chatID] = true
	sm.boardMu.Unlock()
}

// onStreamDone is called by flushLoop when a stream finishes naturally.
func (sm *StreamManager) onStreamDone(sessionID string) {
	sm.mu.Lock()
	if sc, ok := sm.streams[sessionID]; ok {
		delete(sm.taskMap, sc.taskID)
	}
	delete(sm.streams, sessionID)
	sm.mu.Unlock()
	go sm.refreshBoard()
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

	if sc.store != nil && msg != nil {
		_ = sc.store.SetMessageSession(sc.chatID, msg.ID, sc.sessionID)
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
	if sc.store != nil {
		_ = sc.store.SetMessageSession(sc.chatID, msg.ID, sc.sessionID)
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
	if sc.store != nil {
		_ = sc.store.SetMessageSession(sc.chatID, msg2.ID, sc.sessionID)
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

// ---------------------------------------------------------------------------
// Status Board — consolidated <pre> table of all active streams per chat
// ---------------------------------------------------------------------------

func (sm *StreamManager) boardLoop() {
	ticker := time.NewTicker(sm.boardInterval)
	defer ticker.Stop()
	for range ticker.C {
		sm.refreshBoard()
	}
}

func (sm *StreamManager) refreshBoard() {
	sm.boardMu.Lock()
	defer sm.boardMu.Unlock()

	sm.mu.Lock()
	b := sm.b
	if b == nil {
		sm.mu.Unlock()
		return
	}

	// Collect active stream info grouped by chatID
	chatEntries := make(map[int64][]boardEntry)
	for _, sc := range sm.streams {
		sc.mu.Lock()
		if !sc.done {
			// Collect recent tools: all running + last few completed
			var running, completed []toolStatus
			for _, t := range sc.tools {
				if t.State == "running" {
					running = append(running, t)
				} else {
					completed = append(completed, t)
				}
			}
			// Keep last 3 completed + all running, max 8 total
			const maxCompleted = 3
			const maxDisplay = 8
			hidden := 0
			if len(completed) > maxCompleted {
				hidden = len(completed) - maxCompleted
				completed = completed[len(completed)-maxCompleted:]
			}
			display := make([]toolStatus, 0, len(completed)+len(running))
			display = append(display, completed...)
			display = append(display, running...)
			if len(display) > maxDisplay {
				overflow := len(display) - maxDisplay
				hidden += overflow
				display = display[overflow:]
			}
			if len(display) == 0 {
				display = []toolStatus{{State: "running", Detail: "Thinking..."}}
			}

			chatEntries[sc.chatID] = append(chatEntries[sc.chatID], boardEntry{
				taskID:       sc.taskID,
				instanceName: sc.instanceName,
				sessionTitle: sc.sessionTitle,
				location:     sc.location,
				tools:        display,
				elapsed:      time.Since(sc.startedAt),
				hiddenTools:  hidden,
			})
		}
		sc.mu.Unlock()
	}
	sm.mu.Unlock()

	// Update board for each chat with active streams
	for chatID, entries := range chatEntries {
		content := buildBoardHTML(entries)
		keyboard := boardKeyboard(entries)
		needRepos := sm.boardRepos[chatID]
		oldID := sm.boardMsgs[chatID]
		contentChanged := sm.boardContent[chatID] != content

		if !contentChanged && !needRepos {
			continue
		}

		if needRepos {
			delete(sm.boardRepos, chatID)
		}

		// If a new message appeared, delete+resend to keep board at bottom.
		// Otherwise, just edit in place.
		if needRepos || oldID == 0 {
			if oldID != 0 {
				sm.semaphore <- struct{}{}
				_, _ = b.DeleteMessage(context.Background(), &bot.DeleteMessageParams{
					ChatID:    chatID,
					MessageID: oldID,
				})
				<-sm.semaphore
			}

			sm.semaphore <- struct{}{}
			msg, err := b.SendMessage(context.Background(), &bot.SendMessageParams{
				ChatID:              chatID,
				Text:                content,
				ParseMode:           models.ParseModeHTML,
				DisableNotification: true,
				ReplyMarkup:         keyboard,
			})
			<-sm.semaphore
			if err != nil {
				slog.Warn("failed to send board message", "error", err)
				delete(sm.boardMsgs, chatID)
				continue
			}
			sm.boardMsgs[chatID] = msg.ID
			sm.boardContent[chatID] = content
		} else {
			// Edit existing board message in place
			sm.semaphore <- struct{}{}
			_, err := b.EditMessageText(context.Background(), &bot.EditMessageTextParams{
				ChatID:      chatID,
				MessageID:   oldID,
				Text:        content,
				ParseMode:   models.ParseModeHTML,
				ReplyMarkup: keyboard,
			})
			<-sm.semaphore
			if err != nil {
				slog.Warn("failed to edit board message, will resend", "error", err)
				// Fallback: delete and resend
				_, _ = b.DeleteMessage(context.Background(), &bot.DeleteMessageParams{
					ChatID:    chatID,
					MessageID: oldID,
				})
				msg, err := b.SendMessage(context.Background(), &bot.SendMessageParams{
					ChatID:              chatID,
					Text:                content,
					ParseMode:           models.ParseModeHTML,
					DisableNotification: true,
					ReplyMarkup:         keyboard,
				})
				if err != nil {
					slog.Warn("failed to resend board message", "error", err)
					delete(sm.boardMsgs, chatID)
					continue
				}
				sm.boardMsgs[chatID] = msg.ID
			}
			sm.boardContent[chatID] = content
		}
	}

	// Show empty state for chats with no active streams
	emptyContent := "⚡ <b>ACTIVE TASKS</b>  ·  No tasks running"
	for chatID, msgID := range sm.boardMsgs {
		if _, has := chatEntries[chatID]; !has && msgID != 0 {
			if sm.boardContent[chatID] == emptyContent {
				continue
			}
			sm.semaphore <- struct{}{}
			_, _ = b.EditMessageText(context.Background(), &bot.EditMessageTextParams{
				ChatID:    chatID,
				MessageID: msgID,
				Text:      emptyContent,
				ParseMode: models.ParseModeHTML,
			})
			<-sm.semaphore
			sm.boardContent[chatID] = emptyContent
		}
	}
}

// ---------------------------------------------------------------------------
// Board rendering — Unicode box-drawing table
// ---------------------------------------------------------------------------

func buildBoardHTML(entries []boardEntry) string {
	var sb strings.Builder

	// Compact header — no long lines that wrap on mobile
	taskWord := "task"
	if len(entries) != 1 {
		taskWord = "tasks"
	}
	sb.WriteString(fmt.Sprintf("⚡ <b>ACTIVE TASKS</b>  ·  %d %s\n", len(entries), taskWord))

	for _, e := range entries {
		title := strings.ToValidUTF8(e.sessionTitle, "\uFFFD")
		if title == "" {
			title = "(new)"
		}
		runes := []rune(title)
		if len(runes) > 30 {
			title = string(runes[:30]) + "…"
		}

		inst := strings.ToValidUTF8(e.instanceName, "\uFFFD")

		// Each task as a blockquote card — Telegram renders with left border
		sb.WriteString("\n<blockquote>")

		// Task header: ID, instance name, elapsed time
		sb.WriteString(fmt.Sprintf("<b>#%d  %s</b>  ·  ⏱ %s\n", e.taskID, escapeHTML(inst), formatElapsed(e.elapsed)))

		// Location + session title
		if e.location != "" {
			sb.WriteString(fmt.Sprintf("%s <i>%s</i>\n", e.location, escapeHTML(title)))
		} else {
			sb.WriteString(fmt.Sprintf("<i>%s</i>\n", escapeHTML(title)))
		}

		// Blank line separates header from tools
		sb.WriteString("\n")

		// Hidden tools indicator
		if e.hiddenTools > 0 {
			sb.WriteString(fmt.Sprintf("<i>… %d earlier</i>\n", e.hiddenTools))
		}

		// Tool list
		for _, t := range e.tools {
			// "Thinking" state — no tool name
			if t.Name == "" {
				sb.WriteString(fmt.Sprintf("💭 <i>%s</i>\n", escapeHTML(t.Detail)))
				continue
			}

			icon := toolStateIcon(t.State)
			detail := strings.ToValidUTF8(t.Detail, "\uFFFD")
			detailRunes := []rune(detail)
			if len(detailRunes) > 40 {
				detail = string(detailRunes[:40]) + "…"
			}

			name := escapeHTML(t.Name)
			if detail != "" {
				sb.WriteString(fmt.Sprintf("%s <b>%s</b>  <code>%s</code>\n", icon, name, escapeHTML(detail)))
			} else {
				sb.WriteString(fmt.Sprintf("%s <b>%s</b>\n", icon, name))
			}
		}

		sb.WriteString("</blockquote>")
	}

	return sb.String()
}

// toolStateIcon returns the emoji for a tool state.
func toolStateIcon(state string) string {
	switch state {
	case "running":
		return "⏳"
	case "completed":
		return "✅"
	case "error":
		return "❌"
	default:
		return "🔧"
	}
}

// boardKeyboard builds inline stop buttons for each active task.
func boardKeyboard(entries []boardEntry) *models.InlineKeyboardMarkup {
	if len(entries) == 0 {
		return nil
	}
	var row []models.InlineKeyboardButton
	var rows [][]models.InlineKeyboardButton
	for _, e := range entries {
		label := fmt.Sprintf("Stop #%d", e.taskID)
		row = append(row, models.InlineKeyboardButton{
			Text:         label,
			CallbackData: fmt.Sprintf("stoptask:%d", e.taskID),
		})
		if len(row) >= 4 {
			rows = append(rows, row)
			row = nil
		}
	}
	if len(row) > 0 {
		rows = append(rows, row)
	}
	return &models.InlineKeyboardMarkup{InlineKeyboard: rows}
}

func formatElapsed(d time.Duration) string {
	s := int(d.Seconds())
	if s < 60 {
		return fmt.Sprintf("%ds", s)
	}
	m := s / 60
	if m < 60 {
		return fmt.Sprintf("%dm%02ds", m, s%60)
	}
	h := m / 60
	return fmt.Sprintf("%dh%02dm", h, m%60)
}
