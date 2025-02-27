package channels

import (
	"fmt"
)

type TelegramNotifier struct {
	BotToken string
	ChatID   string
}

func NewTelegramNotifier(botToken, chatID string) *TelegramNotifier {
	return &TelegramNotifier{BotToken: botToken, ChatID: chatID}
}

func (t *TelegramNotifier) Send(msg string) error {
	fmt.Printf("Sending Telegram message: %s\n", msg)
	return nil
}

func (t *TelegramNotifier) ChannelName() string {
	return "Telegram"
}
