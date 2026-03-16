package bot

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/pufanyi/opencode-manager/internal/opencode"
)

const (
	maxMessageLen    = 4096
	fileFallbackLen  = 12000
	editInterval     = 1500 * time.Millisecond
)

// StreamContext manages streaming SSE events to a Telegram message.
type StreamContext struct {
	mu         sync.Mutex
	b          *bot.Bot
	chatID     int64
	messageID  int
	sessionID  string
	instanceName string

	content    strings.Builder
	dirty      bool
	done       bool
	messages   []int // IDs of continuation messages
	cancel     context.CancelFunc
	lastEdit   time.Time
}

// StreamManager handles all active streams and global rate limiting.
type StreamManager struct {
	mu       sync.Mutex
	streams  map[string]*StreamContext // keyed by sessionID
	semaphore chan struct{}
}

func NewStreamManager() *StreamManager {
	return &StreamManager{
		streams:   make(map[string]*StreamContext),
		semaphore: make(chan struct{}, 25), // Global rate limit
	}
}

func (sm *StreamManager) StartStream(b *bot.Bot, chatID int64, messageID int, sessionID, instanceName string) *StreamContext {
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
	// Cancel existing stream for this session
	if old, ok := sm.streams[sessionID]; ok {
		old.cancel()
	}
	sm.streams[sessionID] = sc
	sm.mu.Unlock()

	// Start coalescing timer
	go sc.editLoop(ctx, sm.semaphore)

	return sc
}

func (sm *StreamManager) GetStream(sessionID string) *StreamContext {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	return sm.streams[sessionID]
}

func (sm *StreamManager) RemoveStream(sessionID string) {
	sm.mu.Lock()
	if sc, ok := sm.streams[sessionID]; ok {
		sc.cancel()
		delete(sm.streams, sessionID)
	}
	sm.mu.Unlock()
}

// HandleSSEEvent processes an incoming SSE event for streaming to Telegram.
func (sc *StreamContext) HandleSSEEvent(eventType string, data json.RawMessage) {
	if eventType != opencode.EventMessageUpdated && eventType != opencode.EventMessageCreated {
		return
	}

	var msg opencode.Message
	if err := json.Unmarshal(data, &msg); err != nil {
		slog.Error("failed to unmarshal SSE message", "error", err)
		return
	}

	if msg.SessionID != sc.sessionID {
		return
	}

	// Only process assistant messages
	if msg.Role != "assistant" {
		return
	}

	// Extract text from message parts
	var text strings.Builder
	for _, part := range msg.Parts {
		switch part.Type {
		case "text":
			text.WriteString(part.Text)
		case "tool-invocation":
			if part.ToolName != "" {
				icon := "🔧"
				switch part.State {
				case "running", "pending":
					icon = "⏳"
				case "completed":
					icon = "✅"
				case "error":
					icon = "❌"
				}
				text.WriteString(fmt.Sprintf("\n%s `%s`", icon, part.ToolName))
			}
		}
	}

	if text.Len() == 0 {
		return
	}

	sc.mu.Lock()
	sc.content.Reset()
	sc.content.WriteString(text.String())
	sc.dirty = true

	// Check if message is done (has timing info with finished > 0)
	if msg.Time != nil && msg.Time.Finished > 0 {
		sc.done = true
	}
	sc.mu.Unlock()
}

func (sc *StreamContext) editLoop(ctx context.Context, semaphore chan struct{}) {
	ticker := time.NewTicker(editInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			// Final flush
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

	// Add header
	header := fmt.Sprintf("*[%s]*\n\n", escapeMarkdown(sc.instanceName))

	fullContent := header + content

	// If content is too long for Telegram, send as file
	if len(fullContent) > fileFallbackLen {
		sc.sendAsFile(content)
		return
	}

	// Split if needed
	if len(fullContent) > maxMessageLen {
		sc.handleLongMessage(header, content, semaphore)
		return
	}

	// Acquire semaphore for rate limiting
	semaphore <- struct{}{}
	defer func() { <-semaphore }()

	// Determine which message to edit
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
		ParseMode: models.ParseModeMarkdown,
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
	// Finalize current message with truncated content
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
		ParseMode: models.ParseModeMarkdown,
	})

	<-semaphore

	// Send continuation as new message
	remaining := content[available:]
	if len(remaining) > maxMessageLen-len(header) {
		remaining = remaining[:maxMessageLen-len(header)-20] + "\n..."
	}

	semaphore <- struct{}{}
	msg, err := sc.b.SendMessage(context.Background(), &bot.SendMessageParams{
		ChatID:    sc.chatID,
		Text:      header + remaining,
		ParseMode: models.ParseModeMarkdown,
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
		ChatID:   sc.chatID,
		Document: fileData,
		Caption:  fmt.Sprintf("*[%s]* Response too long, sent as file.", escapeMarkdown(sc.instanceName)),
		ParseMode: models.ParseModeMarkdown,
	})
	if err != nil {
		slog.Error("failed to send file", "error", err)
	}
}

func (sc *StreamContext) MarkDone() {
	sc.mu.Lock()
	sc.done = true
	sc.dirty = true
	sc.mu.Unlock()
}

func escapeMarkdown(s string) string {
	replacer := strings.NewReplacer(
		"_", "\\_",
		"*", "\\*",
		"[", "\\[",
		"]", "\\]",
		"`", "\\`",
	)
	return replacer.Replace(s)
}
