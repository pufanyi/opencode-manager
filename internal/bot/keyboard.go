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
	for _, s := range sessions {
		label := s.ID
		if s.Title != "" {
			label = s.Title
		}
		if len(label) > 30 {
			label = label[:30] + "..."
		}
		rows = append(rows, []models.InlineKeyboardButton{
			{
				Text:         label,
				CallbackData: fmt.Sprintf("session:%s", s.ID),
			},
		})
	}
	return &models.InlineKeyboardMarkup{InlineKeyboard: rows}
}

type sessionEntry struct {
	ID    string
	Title string
}

func promptDoneKeyboard(sessionID string) *models.InlineKeyboardMarkup {
	return &models.InlineKeyboardMarkup{
		InlineKeyboard: [][]models.InlineKeyboardButton{
			{
				{Text: "Abort", CallbackData: fmt.Sprintf("abort:%s", sessionID)},
				{Text: "New Session", CallbackData: "newsession"},
			},
		},
	}
}
