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
	editInterval    = 1500 * time.Millisecond
	draftInterval   = 500 * time.Millisecond // Faster updates for drafts
)

// StreamContext manages streaming provider events to a Telegram message.
type StreamContext struct {
	mu           sync.Mutex
	b            *bot.Bot
	chatID       int64
	messageID    int // Used for editMessageText fallback
	sessionID    string
	instanceName string

	// Draft mode (private chats)
	useDraft bool
	draftID  string

	content  strings.Builder
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

	// Private chats (chatID > 0) use sendMessageDraft for native streaming
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
				sc.content.Reset()
				sc.content.WriteString(evt.Text)
				sc.dirty = true
			case "tool_use":
				icon := "🔧"
				switch evt.ToolState {
				case "running", "pending":
					icon = "⏳"
				case "completed":
					icon = "✅"
				case "error":
					icon = "❌"
				}
				current := sc.content.String()
				sc.content.Reset()
				sc.content.WriteString(current)
				sc.content.WriteString(fmt.Sprintf("\n%s <code>%s</code>", icon, escapeHTML(evt.ToolName)))
				sc.dirty = true
			case "done":
				sc.done = true
				sc.dirty = true
			case "error":
				sc.content.Reset()
				sc.content.WriteString(fmt.Sprintf("Error: %s", evt.Error))
				sc.done = true
				sc.dirty = true
			}
			sc.mu.Unlock()
		}
	}
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
			sc.flush(semaphore, false)
			if sc.isDone() {
				return
			}
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
	content := sc.content.String()
	done := sc.done
	sc.mu.Unlock()

	if content == "" {
		return
	}

	header := fmt.Sprintf("<b>[%s]</b>\n\n", escapeHTML(sc.instanceName))
	rendered := markdownToTelegramHTML(content)
	fullContent := header + rendered

	if len(fullContent) > fileFallbackLen {
		sc.sendAsFile(content)
		if sc.useDraft {
			sc.finalizeDraft("")
		}
		return
	}

	if sc.useDraft {
		sc.flushDraft(fullContent, done || final, semaphore)
	} else {
		sc.flushEdit(fullContent, rendered, header, done || final, semaphore)
	}
}

// flushDraft sends updates via sendMessageDraft, finalizes with sendMessage when done.
func (sc *StreamContext) flushDraft(fullContent string, final bool, semaphore chan struct{}) {
	if len(fullContent) > maxMessageLen {
		// Draft doesn't support splitting; truncate with indicator
		fullContent = fullContent[:maxMessageLen-10] + "\n..."
	}

	semaphore <- struct{}{}
	defer func() { <-semaphore }()

	if !final {
		// Streaming update — add cursor indicator
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
			slog.Debug("draft failed, falling back to edit", "error", err)
			sc.useDraft = false
		}
		return
	}

	// Final — send as permanent message with keyboard
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
		slog.Error("failed to finalize draft", "error", err)
		return
	}

	// Delete the placeholder message (the "Thinking..." message)
	sc.b.DeleteMessage(context.Background(), &bot.DeleteMessageParams{
		ChatID:    sc.chatID,
		MessageID: sc.messageID,
	})

	sc.mu.Lock()
	sc.messages = append(sc.messages, msg.ID)
	sc.mu.Unlock()
}

// flushEdit is the fallback for group chats — uses editMessageText.
func (sc *StreamContext) flushEdit(fullContent, rendered, header string, final bool, semaphore chan struct{}) {
	if len(fullContent) > maxMessageLen {
		sc.handleLongMessage(header, rendered, semaphore)
		return
	}

	semaphore <- struct{}{}
	defer func() { <-semaphore }()

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
		slog.Debug("edit message failed", "error", err)
	}
}

func (sc *StreamContext) handleLongMessage(header, content string, semaphore chan struct{}) {
	available := maxMessageLen - len(header) - 20
	truncated := content[:available] + "\n..."

	semaphore <- struct{}{}

	sc.mu.Lock()
	msgID := sc.messageID
	if len(sc.messages) > 0 {
		msgID = sc.messages[len(sc.messages)-1]
	}
	sc.mu.Unlock()

	_, _ = sc.b.EditMessageText(context.Background(), &bot.EditMessageTextParams{
		ChatID:    sc.chatID,
		MessageID: msgID,
		Text:      header + truncated,
		ParseMode: models.ParseModeHTML,
	})

	<-semaphore

	remaining := content[available:]
	if len(remaining) > maxMessageLen-len(header) {
		remaining = remaining[:maxMessageLen-len(header)-20] + "\n..."
	}

	semaphore <- struct{}{}
	msg, err := sc.b.SendMessage(context.Background(), &bot.SendMessageParams{
		ChatID:    sc.chatID,
		Text:      header + remaining,
		ParseMode: models.ParseModeHTML,
	})
	<-semaphore

	if err == nil {
		sc.mu.Lock()
		sc.messages = append(sc.messages, msg.ID)
		sc.mu.Unlock()
	}
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
