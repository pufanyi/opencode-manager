package bot

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/pufanyi/opencode-manager/internal/config"
	"github.com/pufanyi/opencode-manager/internal/process"
	"github.com/pufanyi/opencode-manager/internal/store"
)

type Bot struct {
	bot      *bot.Bot
	handlers *Handlers
	cfg      *config.TelegramConfig
}

func New(cfg *config.TelegramConfig, procMgr *process.Manager, st *store.Store) (*Bot, error) {
	streamMgr := NewStreamManager()
	handlers := NewHandlers(procMgr, st, streamMgr)

	allowedUsers := make(map[int64]bool)
	for _, id := range cfg.AllowedUsers {
		allowedUsers[id] = true
	}

	opts := []bot.Option{
		bot.WithDefaultHandler(func(ctx context.Context, b *bot.Bot, update *models.Update) {
			if update.CallbackQuery != nil {
				if !allowedUsers[update.CallbackQuery.From.ID] {
					return
				}
				handlers.HandleCallback(ctx, b, update)
				return
			}

			if update.Message != nil && update.Message.Text != "" {
				if !allowedUsers[update.Message.From.ID] {
					return
				}
				slog.Info("default handler: treating as prompt", "text", update.Message.Text)
				handlers.HandlePrompt(ctx, b, update)
			}
		}),
	}

	b, err := bot.New(cfg.Token, opts...)
	if err != nil {
		return nil, err
	}

	authMiddleware := func(next bot.HandlerFunc) bot.HandlerFunc {
		return func(ctx context.Context, b *bot.Bot, update *models.Update) {
			if update.Message != nil && !allowedUsers[update.Message.From.ID] {
				slog.Warn("unauthorized user", "user_id", update.Message.From.ID)
				return
			}
			next(ctx, b, update)
		}
	}

	b.RegisterHandlerMatchFunc(matchCommand("/start"), authMiddleware(handlers.HandleStart))
	b.RegisterHandlerMatchFunc(matchCommand("/help"), authMiddleware(handlers.HandleHelp))
	b.RegisterHandlerMatchFunc(matchCommand("/newopencode"), authMiddleware(handlers.HandleNewOpenCode))
	b.RegisterHandlerMatchFunc(matchCommand("/new"), authMiddleware(handlers.HandleNew))
	b.RegisterHandlerMatchFunc(matchCommand("/list"), authMiddleware(handlers.HandleList))
	b.RegisterHandlerMatchFunc(matchCommand("/switch"), authMiddleware(handlers.HandleSwitch))
	b.RegisterHandlerMatchFunc(matchCommand("/stop"), authMiddleware(handlers.HandleStop))
	b.RegisterHandlerMatchFunc(matchCommand("/start_inst"), authMiddleware(handlers.HandleStartInst))
	b.RegisterHandlerMatchFunc(matchCommand("/status"), authMiddleware(handlers.HandleStatus))
	b.RegisterHandlerMatchFunc(matchCommand("/session"), authMiddleware(handlers.HandleSession))
	b.RegisterHandlerMatchFunc(matchCommand("/sessions"), authMiddleware(handlers.HandleSessions))
	b.RegisterHandlerMatchFunc(matchCommand("/abort"), authMiddleware(handlers.HandleAbort))

	return &Bot{
		bot:      b,
		handlers: handlers,
		cfg:      cfg,
	}, nil
}

func (b *Bot) Start(ctx context.Context) {
	slog.Info("telegram bot starting")
	b.bot.Start(ctx)
}

func (b *Bot) Stop() {
}

func (b *Bot) SendMessage(ctx context.Context, chatID int64, text string) {
	_, _ = b.bot.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:    chatID,
		Text:      text,
		ParseMode: models.ParseModeMarkdown,
	})
}

func (b *Bot) NotifyCrash(instName string, err error) {
	text := fmt.Sprintf("*Instance %s* crashed permanently: %s", escapeMarkdown(instName), err)
	for _, userID := range b.cfg.AllowedUsers {
		b.SendMessage(context.Background(), userID, text)
	}
}

func matchCommand(cmd string) func(update *models.Update) bool {
	return func(update *models.Update) bool {
		if update.Message == nil {
			return false
		}
		text := update.Message.Text
		if text == cmd {
			return true
		}
		if len(text) > len(cmd) && text[:len(cmd)] == cmd {
			next := text[len(cmd)]
			return next == ' ' || next == '@'
		}
		return false
	}
}
