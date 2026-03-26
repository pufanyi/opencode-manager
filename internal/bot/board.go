package bot

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

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
