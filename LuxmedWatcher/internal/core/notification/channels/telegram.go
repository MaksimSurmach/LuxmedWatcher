package channels

import (
	"fmt"
	"LuxmedWatcher/internal/core/notification"
	"context"
)

type TelegramNotifier struct {
	BotToken string
	ChatID   string
}

func (t *TelegramNotifier) Create(config interface{}) (notification.Notifier, error) {
	cfg, ok := config.(TelegramNotifier)
	if !ok {
		return nil, fmt.Errorf("invalid config type for TelegramNotifier")
	}

	if cfg.BotToken == "" {
		return nil, fmt.Errorf("BotToken is required")
	}
	if cfg.ChatID == "" {
		return nil, fmt.Errorf("ChatID is required")
	}

	return &TelegramNotifier{
		BotToken: cfg.BotToken,
		ChatID:   cfg.ChatID,
	}, nil
}

func (t *TelegramNotifier) SendMessage(ctx context.Context, msg string) error {
	fmt.Printf("Sending Telegram message: %s\n", msg)
	return nil
}

func (t *TelegramNotifier) ChannelName() string {
	return "Telegram"
}
