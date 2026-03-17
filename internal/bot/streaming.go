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
	"github.com/pufanyi/opencode-manager/internal/gitops"
	"github.com/pufanyi/opencode-manager/internal/provider"
	"github.com/pufanyi/opencode-manager/internal/store"
)

const (
	maxMessageLen   = 4096
	fileFallbackLen = 12000
	boardInterval   = 5 * time.Second
)

// toolStatus tracks a single tool invocation's display state.
type toolStatus struct {
	Name  string
	State string // "running", "completed", "error"
}

// boardEntry holds snapshot data for one stream in the status board.
type boardEntry struct {
	taskID       int
	instanceName string
	sessionTitle string
	currentTool  string
	elapsed      time.Duration
}

// StreamContext manages streaming provider events to a Telegram message.
type StreamContext struct {
	mu               sync.Mutex
	b                *bot.Bot
	store            *store.Store
	chatID           int64
	sessionID        string
	instanceName     string
	sessionTitle     string
	workDir          string // instance working directory for git merge-back
	replyToMessageID int    // original user message ID for Telegram reply
	startedAt        time.Time
	manager          *StreamManager
	taskID           int

	// Content tracked separately: text + tools
	textContent string
	tools       []toolStatus

	dirty        bool
	done         bool
	cancel       context.CancelFunc
	promptCancel context.CancelFunc
	abortFunc    func()
	cleanupFiles []string // temp files to remove when stream ends
}

// StreamManager handles all active streams and global rate limiting.
type StreamManager struct {
	mu        sync.Mutex
	streams   map[string]*StreamContext // keyed by sessionID
	taskMap   map[int]*StreamContext    // keyed by taskID
	semaphore chan struct{}
	nextID    int

	// Status board
	b            *bot.Bot
	boardMu      sync.Mutex
	boardMsgs    map[int64]int    // chatID -> board message ID
	boardContent map[int64]string // chatID -> last sent content
	boardStarted bool
}

func NewStreamManager() *StreamManager {
	return &StreamManager{
		streams:      make(map[string]*StreamContext),
		taskMap:      make(map[int]*StreamContext),
		semaphore:    make(chan struct{}, 25),
		boardMsgs:    make(map[int64]int),
		boardContent: make(map[int64]string),
	}
}

func (sm *StreamManager) StartStream(b *bot.Bot, st *store.Store, chatID int64, sessionID, instanceName, sessionTitle, workDir string, replyToMessageID int, ch <-chan provider.StreamEvent, promptCancel context.CancelFunc, abortFunc func()) *StreamContext {
	ctx, cancel := context.WithCancel(context.Background())

	sm.mu.Lock()
	sm.nextID++
	taskID := sm.nextID

	if old, ok := sm.streams[sessionID]; ok {
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

	// Immediate board update to show new task
	go sm.refreshBoard()

	return sc
}

// AddCleanupFile registers a temp file to be deleted when the stream ends.
func (sc *StreamContext) AddCleanupFile(path string) {
	sc.mu.Lock()
	sc.cleanupFiles = append(sc.cleanupFiles, path)
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
				sc.updateTool(evt.ToolName, evt.ToolState)
				sc.dirty = true
			case "done":
				sc.done = true
				sc.dirty = true
			case "error":
				sc.textContent = fmt.Sprintf("Error: %s", evt.Error)
				sc.done = true
				sc.dirty = true
			}
			sc.mu.Unlock()
		}
	}
}

// updateTool adds or updates a tool's status.
func (sc *StreamContext) updateTool(name, state string) {
	for i := range sc.tools {
		if sc.tools[i].Name == name && sc.tools[i].State == "running" {
			sc.tools[i].State = state
			return
		}
	}
	sc.tools = append(sc.tools, toolStatus{Name: name, State: state})
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
				sc.mergeBack()
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
}

// cleanup removes any temp files registered via AddCleanupFile.
func (sc *StreamContext) cleanup() {
	sc.mu.Lock()
	files := sc.cleanupFiles
	sc.cleanupFiles = nil
	sc.mu.Unlock()

	for _, f := range files {
		if err := os.Remove(f); err != nil && !os.IsNotExist(err) {
			slog.Warn("failed to clean up temp file", "path", f, "error", err)
		} else {
			slog.Debug("cleaned up temp file", "path", f)
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
	tools := make([]toolStatus, len(sc.tools))
	copy(tools, sc.tools)
	sc.mu.Unlock()

	// --- Build HTML content ---
	header := fmt.Sprintf("<b>[%s]</b>", escapeHTML(sc.instanceName))
	toolsHTML := formatToolsHTML(tools)

	var renderedText string
	if text != "" {
		renderedText = markdownToTelegramHTML(text)
		if renderedText == "" {
			renderedText = escapeHTML(text)
		}
	}

	fullContent := header
	if toolsHTML != "" {
		fullContent += "\n" + toolsHTML
	}
	if renderedText != "" {
		fullContent += "\n\n" + renderedText
	}
	if toolsHTML == "" && renderedText == "" {
		fullContent += " Done."
	}

	// --- Build plain-text fallback ---
	toolsPlain := formatToolsPlain(tools)
	var rawParts []string
	if toolsPlain != "" {
		rawParts = append(rawParts, toolsPlain)
	}
	if text != "" {
		rawParts = append(rawParts, text)
	}
	rawContent := strings.Join(rawParts, "\n\n")
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
		ChatID:      sc.chatID,
		Text:        fullContent,
		ParseMode:   models.ParseModeHTML,
		ReplyMarkup: promptDoneKeyboard(sc.sessionID),
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
		ChatID:      sc.chatID,
		Text:        contText,
		ParseMode:   models.ParseModeHTML,
		ReplyMarkup: promptDoneKeyboard(sc.sessionID),
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
}

// stripHTML is a simple helper to remove HTML tags for plain-text fallback.
func stripHTML(s string) string {
	r := strings.NewReplacer(
		"<b>", "", "</b>", "",
		"<i>", "", "</i>", "",
		"<code>", "", "</code>", "",
		"<pre>", "", "</pre>", "",
		"&amp;", "&",
		"&lt;", "<",
		"&gt;", ">",
	)
	return r.Replace(s)
}

// ---------------------------------------------------------------------------
// Status Board — consolidated <pre> table of all active streams per chat
// ---------------------------------------------------------------------------

func (sm *StreamManager) boardLoop() {
	ticker := time.NewTicker(boardInterval)
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
			var tool string
			for j := len(sc.tools) - 1; j >= 0; j-- {
				if sc.tools[j].State == "running" {
					tool = sc.tools[j].Name
					break
				}
			}
			chatEntries[sc.chatID] = append(chatEntries[sc.chatID], boardEntry{
				taskID:       sc.taskID,
				instanceName: sc.instanceName,
				sessionTitle: sc.sessionTitle,
				currentTool:  tool,
				elapsed:      time.Since(sc.startedAt),
			})
		}
		sc.mu.Unlock()
	}
	sm.mu.Unlock()

	// Update board for each chat with active streams
	for chatID, entries := range chatEntries {
		content := buildBoardHTML(entries)
		keyboard := boardKeyboard(entries)

		if sm.boardContent[chatID] == content {
			continue
		}

		msgID := sm.boardMsgs[chatID]
		if msgID == 0 {
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
				continue
			}
			sm.boardMsgs[chatID] = msg.ID
		} else {
			sm.semaphore <- struct{}{}
			_, err := b.EditMessageText(context.Background(), &bot.EditMessageTextParams{
				ChatID:      chatID,
				MessageID:   msgID,
				Text:        content,
				ParseMode:   models.ParseModeHTML,
				ReplyMarkup: keyboard,
			})
			<-sm.semaphore
			if err != nil && !strings.Contains(err.Error(), "message is not modified") {
				slog.Warn("board edit failed, recreating", "error", err)
				sm.semaphore <- struct{}{}
				msg, err := b.SendMessage(context.Background(), &bot.SendMessageParams{
					ChatID:              chatID,
					Text:                content,
					ParseMode:           models.ParseModeHTML,
					DisableNotification: true,
					ReplyMarkup:         keyboard,
				})
				<-sm.semaphore
				if err == nil {
					sm.boardMsgs[chatID] = msg.ID
				}
			}
		}
		sm.boardContent[chatID] = content
	}

	// Clean up boards for chats with no active streams
	for chatID, msgID := range sm.boardMsgs {
		if _, has := chatEntries[chatID]; !has && msgID != 0 {
			sm.semaphore <- struct{}{}
			_, _ = b.DeleteMessage(context.Background(), &bot.DeleteMessageParams{
				ChatID:    chatID,
				MessageID: msgID,
			})
			<-sm.semaphore
			delete(sm.boardMsgs, chatID)
			delete(sm.boardContent, chatID)
		}
	}
}

// ---------------------------------------------------------------------------
// Board rendering — Unicode box-drawing table
// ---------------------------------------------------------------------------

func buildBoardHTML(entries []boardEntry) string {
	var sb strings.Builder
	sb.WriteString("📋 <b>Active Tasks</b>\n")

	for _, e := range entries {
		title := strings.ToValidUTF8(e.sessionTitle, "\uFFFD")
		if title == "" {
			title = "(new)"
		}
		runes := []rune(title)
		if len(runes) > 30 {
			title = string(runes[:30]) + ".."
		}

		inst := strings.ToValidUTF8(e.instanceName, "\uFFFD")

		status := "thinking"
		if e.currentTool != "" {
			status = strings.ToValidUTF8(e.currentTool, "\uFFFD")
		}

		sb.WriteString(fmt.Sprintf("\n<b>#%d</b> %s\n", e.taskID, escapeHTML(inst)))
		sb.WriteString(fmt.Sprintf("  %s | ⏳ %s (%s)\n", escapeHTML(title), escapeHTML(status), formatElapsed(e.elapsed)))
	}

	return sb.String()
}

// boardKeyboard builds inline stop buttons for each active task.
func boardKeyboard(entries []boardEntry) *models.InlineKeyboardMarkup {
	if len(entries) == 0 {
		return nil
	}
	var row []models.InlineKeyboardButton
	var rows [][]models.InlineKeyboardButton
	for _, e := range entries {
		row = append(row, models.InlineKeyboardButton{
			Text:         fmt.Sprintf("Stop #%d", e.taskID),
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
	return fmt.Sprintf("%dm%ds", s/60, s%60)
}
