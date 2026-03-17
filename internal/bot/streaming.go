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
)

// StreamContext manages streaming provider events to a Telegram message.
type StreamContext struct {
	mu           sync.Mutex
	b            *bot.Bot
	chatID       int64
	messageID    int
	sessionID    string
	instanceName string

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

	sc := &StreamContext{
		b:            b,
		chatID:       chatID,
		messageID:    messageID,
		sessionID:    sessionID,
		instanceName: instanceName,
		cancel:       cancel,
		lastEdit:     time.Now(),
	}

	sm.mu.Lock()
	if old, ok := sm.streams[sessionID]; ok {
		old.cancel()
	}
	sm.streams[sessionID] = sc
	sm.mu.Unlock()

	// Consume events from provider channel
	go sc.consumeStream(ctx, ch)
	// Periodic flush to Telegram
	go sc.editLoop(ctx, sm.semaphore)

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
	var textBuf strings.Builder

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
				textBuf.Reset()
				textBuf.WriteString(evt.Text)
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
				// Append tool line after current text
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

func (sc *StreamContext) editLoop(ctx context.Context, semaphore chan struct{}) {
	ticker := time.NewTicker(editInterval)
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
		return
	}

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

	if done || final {
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
