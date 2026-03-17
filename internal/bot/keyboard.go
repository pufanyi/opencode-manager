package bot

import (
	"fmt"

	"github.com/go-telegram/bot/models"
	"github.com/pufanyi/opencode-manager/internal/process"
)

func instanceListKeyboard(instances []*process.Instance) *models.InlineKeyboardMarkup {
	var rows [][]models.InlineKeyboardButton
	for _, inst := range instances {
		statusIcon := "🔴"
		if inst.Status() == process.StatusRunning {
			statusIcon = "🟢"
		} else if inst.Status() == process.StatusStarting {
			statusIcon = "🟡"
		}

		rows = append(rows, []models.InlineKeyboardButton{
			{
				Text:         fmt.Sprintf("%s %s", statusIcon, inst.Name),
				CallbackData: fmt.Sprintf("switch:%s", inst.ID),
			},
		})
	}
	return &models.InlineKeyboardMarkup{InlineKeyboard: rows}
}

func instanceActionsKeyboard(inst *process.Instance) *models.InlineKeyboardMarkup {
	var buttons []models.InlineKeyboardButton

	if inst.Status() == process.StatusRunning {
		buttons = append(buttons,
			models.InlineKeyboardButton{Text: "Stop", CallbackData: fmt.Sprintf("stop:%s", inst.ID)},
			models.InlineKeyboardButton{Text: "Switch", CallbackData: fmt.Sprintf("switch:%s", inst.ID)},
		)
	} else {
		buttons = append(buttons,
			models.InlineKeyboardButton{Text: "Start", CallbackData: fmt.Sprintf("start:%s", inst.ID)},
			models.InlineKeyboardButton{Text: "Delete", CallbackData: fmt.Sprintf("delete:%s", inst.ID)},
		)
	}

	return &models.InlineKeyboardMarkup{
		InlineKeyboard: [][]models.InlineKeyboardButton{buttons},
	}
}

func sessionListKeyboard(sessions []sessionEntry) *models.InlineKeyboardMarkup {
	var rows [][]models.InlineKeyboardButton

	// Limit to 20 sessions for keyboard
	limit := len(sessions)
	if limit > 20 {
		limit = 20
	}

	for _, s := range sessions[:limit] {
		label := s.Title
		if label == "" {
			label = s.ID[:min(12, len(s.ID))]
		}
		if len(label) > 28 {
			label = label[:28] + "..."
		}

		// Add active indicator and message count
		prefix := ""
		if s.IsActive {
			prefix = "▶ "
		}
		suffix := ""
		if s.MessageCount > 0 {
			suffix = fmt.Sprintf(" [%d]", s.MessageCount)
		}
		label = prefix + label + suffix

		row := []models.InlineKeyboardButton{
			{
				Text:         label,
				CallbackData: fmt.Sprintf("session:%s", s.ID),
			},
			{
				Text:         "🗑",
				CallbackData: fmt.Sprintf("delsession:%s", s.ID),
			},
		}
		rows = append(rows, row)
	}

	// Add "New Session" button at the bottom
	rows = append(rows, []models.InlineKeyboardButton{
		{
			Text:         "+ New Session",
			CallbackData: "newsession",
		},
	})

	return &models.InlineKeyboardMarkup{InlineKeyboard: rows}
}

type sessionEntry struct {
	ID           string
	Title        string
	MessageCount int
	IsActive     bool
}

