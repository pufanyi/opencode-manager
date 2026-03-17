package bot

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/pufanyi/opencode-manager/internal/provider"
)

const (
	maxMessageLen   = 4096
	fileFallbackLen = 12000
	editInterval    = 5 * time.Second
	draftInterval   = 2 * time.Second
)

// toolStatus tracks a single tool invocation's display state.
type toolStatus struct {
	Name  string
	State string // "running", "completed", "error"
}

// StreamContext manages streaming provider events to a Telegram message.
type StreamContext struct {
	mu           sync.Mutex
	b            *bot.Bot
	chatID       int64
	messageID    int
	sessionID    string
	instanceName string

	// Draft mode (private chats)
	useDraft bool
	draftID  string

	// Content tracked separately: text + tools
	textContent string
	tools       []toolStatus

	dirty    bool
	done     bool
	messages []int // IDs of continuation messages
	cancel   context.CancelFunc
	lastEdit time.Time
}

// StreamManager handles all active streams and global rate limiting.
type StreamManager struct {
	mu        sync.Mutex
	streams   map[string]*StreamContext // keyed by sessionID
	semaphore chan struct{}
}

func NewStreamManager() *StreamManager {
	return &StreamManager{
		streams:   make(map[string]*StreamContext),
		semaphore: make(chan struct{}, 25),
	}
}

func (sm *StreamManager) StartStream(b *bot.Bot, chatID int64, messageID int, sessionID, instanceName string, ch <-chan provider.StreamEvent) *StreamContext {
	ctx, cancel := context.WithCancel(context.Background())

	useDraft := chatID > 0
	draftID := ""
	if useDraft {
		draftID = fmt.Sprintf("%d", time.Now().UnixNano())
	}

	sc := &StreamContext{
		b:            b,
		chatID:       chatID,
		messageID:    messageID,
		sessionID:    sessionID,
		instanceName: instanceName,
		useDraft:     useDraft,
		draftID:      draftID,
		cancel:       cancel,
		lastEdit:     time.Now(),
	}

	sm.mu.Lock()
	if old, ok := sm.streams[sessionID]; ok {
		old.cancel()
	}
	sm.streams[sessionID] = sc
	sm.mu.Unlock()

	go sc.consumeStream(ctx, ch)
	go sc.flushLoop(ctx, sm.semaphore)

	return sc
}

func (sm *StreamManager) RemoveStream(sessionID string) {
	sm.mu.Lock()
	if sc, ok := sm.streams[sessionID]; ok {
		sc.cancel()
		delete(sm.streams, sessionID)
	}
	sm.mu.Unlock()
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
	interval := editInterval
	if sc.useDraft {
		interval = draftInterval
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			sc.flush(semaphore, true)
			return
		case <-ticker.C:
			if sc.isDone() {
				sc.flush(semaphore, true)
				return
			}
			sc.flush(semaphore, false)
		}
	}
}

func (sc *StreamContext) isDone() bool {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	return sc.done
}

func (sc *StreamContext) flush(semaphore chan struct{}, final bool) {
	sc.mu.Lock()
	if !sc.dirty {
		sc.mu.Unlock()
		return
	}
	sc.dirty = false
	text := sc.textContent
	tools := make([]toolStatus, len(sc.tools))
	copy(tools, sc.tools)
	done := sc.done
	sc.mu.Unlock()

	if text == "" && len(tools) == 0 {
		return
	}

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

	// Assemble: header + tools (top) + text
	fullContent := header
	if toolsHTML != "" {
		fullContent += "\n" + toolsHTML
	}
	if renderedText != "" {
		fullContent += "\n\n" + renderedText
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

	// --- Dispatch ---
	if len(fullContent) > fileFallbackLen {
		sc.sendAsFile(rawContent)
		if sc.useDraft {
			sc.finalizeDraft("")
		}
		return
	}

	if sc.useDraft {
		sc.flushDraft(fullContent, rawContent, done || final, semaphore)
	} else {
		sc.flushEdit(fullContent, rawContent, header, done || final, semaphore)
	}
}

// flushDraft sends updates via sendMessageDraft, finalizes with sendMessage when done.
func (sc *StreamContext) flushDraft(fullContent, rawContent string, final bool, semaphore chan struct{}) {
	if len(fullContent) > maxMessageLen {
		fullContent = truncateUTF8(fullContent, maxMessageLen-10) + "\n..."
	}

	semaphore <- struct{}{}
	defer func() { <-semaphore }()

	if !final {
		display := fullContent + " ▌"
		if len(display) > maxMessageLen {
			display = fullContent
		}

		_, err := sc.b.SendMessageDraft(context.Background(), &bot.SendMessageDraftParams{
			ChatID:    sc.chatID,
			DraftID:   sc.draftID,
			Text:      display,
			ParseMode: models.ParseModeHTML,
		})
		if err != nil {
			slog.Warn("draft failed, falling back to edit", "error", err)
			sc.useDraft = false
			sc.doEdit(fullContent, rawContent, false)
		}
		return
	}

	sc.finalizeDraft(fullContent)
}

// finalizeDraft converts the draft into a permanent message.
func (sc *StreamContext) finalizeDraft(fullContent string) {
	params := &bot.SendMessageParams{
		ChatID:    sc.chatID,
		ParseMode: models.ParseModeHTML,
	}

	if fullContent != "" {
		params.Text = fullContent
	} else {
		params.Text = fmt.Sprintf("<b>[%s]</b> Done.", escapeHTML(sc.instanceName))
	}
	params.ReplyMarkup = promptDoneKeyboard(sc.sessionID)

	msg, err := sc.b.SendMessage(context.Background(), params)
	if err != nil {
		slog.Warn("finalize draft with HTML failed, retrying plain text", "error", err)
		params.ParseMode = ""
		params.Text = fmt.Sprintf("[%s]\n\n%s", sc.instanceName, stripHTML(fullContent))
		if len(params.Text) > maxMessageLen {
			params.Text = truncateUTF8(params.Text, maxMessageLen-10) + "\n..."
		}
		msg, err = sc.b.SendMessage(context.Background(), params)
		if err != nil {
			slog.Error("finalize draft failed completely", "error", err)
			return
		}
	}

	// Delete the placeholder message
	_, _ = sc.b.DeleteMessage(context.Background(), &bot.DeleteMessageParams{
		ChatID:    sc.chatID,
		MessageID: sc.messageID,
	})

	sc.mu.Lock()
	sc.messages = append(sc.messages, msg.ID)
	sc.mu.Unlock()
}

// flushEdit is the fallback for group chats — uses editMessageText.
func (sc *StreamContext) flushEdit(fullContent, rawContent, header string, final bool, semaphore chan struct{}) {
	if len(fullContent) > maxMessageLen {
		sc.handleLongMessage(header, fullContent, rawContent, semaphore)
		return
	}

	semaphore <- struct{}{}
	defer func() { <-semaphore }()

	sc.doEdit(fullContent, rawContent, final)
}

// doEdit performs the actual editMessageText, with plain-text fallback on HTML error.
func (sc *StreamContext) doEdit(fullContent, rawContent string, final bool) {
	sc.mu.Lock()
	msgID := sc.messageID
	if len(sc.messages) > 0 {
		msgID = sc.messages[len(sc.messages)-1]
	}
	sc.mu.Unlock()

	params := &bot.EditMessageTextParams{
		ChatID:    sc.chatID,
		MessageID: msgID,
		Text:      fullContent,
		ParseMode: models.ParseModeHTML,
	}

	if final {
		params.ReplyMarkup = promptDoneKeyboard(sc.sessionID)
	}

	_, err := sc.b.EditMessageText(context.Background(), params)
	if err != nil {
		errStr := err.Error()
		if strings.Contains(errStr, "message is not modified") {
			return
		}

		slog.Warn("edit message failed with HTML, retrying plain text", "error", err, "msgID", msgID)

		plainHeader := fmt.Sprintf("[%s]\n\n", sc.instanceName)
		plainContent := plainHeader + rawContent
		if len(plainContent) > maxMessageLen {
			plainContent = truncateUTF8(plainContent, maxMessageLen-10) + "\n..."
		}
		params.ParseMode = ""
		params.Text = plainContent
		_, err = sc.b.EditMessageText(context.Background(), params)
		if err != nil && !strings.Contains(err.Error(), "message is not modified") {
			slog.Error("edit message failed completely", "error", err, "msgID", msgID)
		}
	}
}

func (sc *StreamContext) handleLongMessage(header, fullContent, rawContent string, semaphore chan struct{}) {
	available := maxMessageLen - len(header) - 20
	body := fullContent[len(header):]
	if len(body) > available {
		body = truncateUTF8(body, available)
	}
	truncated := header + body + "\n..."

	semaphore <- struct{}{}

	sc.mu.Lock()
	msgID := sc.messageID
	if len(sc.messages) > 0 {
		msgID = sc.messages[len(sc.messages)-1]
	}
	sc.mu.Unlock()

	_, err := sc.b.EditMessageText(context.Background(), &bot.EditMessageTextParams{
		ChatID:    sc.chatID,
		MessageID: msgID,
		Text:      truncated,
		ParseMode: models.ParseModeHTML,
	})
	if err != nil && !strings.Contains(err.Error(), "message is not modified") {
		slog.Warn("edit long message failed", "error", err)
	}

	<-semaphore

	remaining := fullContent[len(header)+len(body):]
	if remaining == "" {
		return
	}
	if len(remaining) > maxMessageLen-len(header)-20 {
		remaining = truncateUTF8(remaining, maxMessageLen-len(header)-20) + "\n..."
	}

	semaphore <- struct{}{}
	msg, err := sc.b.SendMessage(context.Background(), &bot.SendMessageParams{
		ChatID:    sc.chatID,
		Text:      header + remaining,
		ParseMode: models.ParseModeHTML,
	})
	<-semaphore

	if err != nil {
		slog.Warn("send continuation message failed", "error", err)
		return
	}

	sc.mu.Lock()
	sc.messages = append(sc.messages, msg.ID)
	sc.mu.Unlock()
}

func (sc *StreamContext) sendAsFile(content string) {
	fileData := &models.InputFileUpload{
		Filename: "response.md",
		Data:     strings.NewReader(content),
	}

	_, err := sc.b.SendDocument(context.Background(), &bot.SendDocumentParams{
		ChatID:    sc.chatID,
		Document:  fileData,
		Caption:   fmt.Sprintf("<b>[%s]</b> Response too long, sent as file.", escapeHTML(sc.instanceName)),
		ParseMode: models.ParseModeHTML,
	})
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
