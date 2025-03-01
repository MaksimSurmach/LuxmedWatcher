package channels

import (
	"LuxmedWatcher/internal/core/notification"
	"context"
	"fmt"
	"net/http"
	"net/url"
)

func init() {
	notification.RegisterNotifier("telegram", NewTelegramNotifier)
}

type TelegramConfig struct {
	BotToken string `json:"bot_token" yaml:"bot_token"`
	ChatID   string `json:"chat_id" yaml:"chat_id"`
}

type TelegramNotifier struct {
	botToken string
	chatID   string
}

func NewTelegramNotifier(config interface{}) (notification.Notifier, error) {
	cfg, ok := config.(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("invalid config type for TelegramNotifier")
	}

	botToken, ok := cfg["bot_token"].(string)
	if !ok || botToken == "" {
		return nil, fmt.Errorf("bot_token is required for Telegram notifier")
	}

	chatID, ok := cfg["chat_id"].(string)
	if !ok || chatID == "" {
		return nil, fmt.Errorf("chat_id is required for Telegram notifier")
	}

	return &TelegramNotifier{
		botToken: botToken,
		chatID:   chatID,
	}, nil
}

// SendMessage отправляет сообщение в Telegram
func (t *TelegramNotifier) SendMessage(ctx context.Context, msg string) error {
	apiURL := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", t.botToken)

	params := url.Values{}
	params.Add("chat_id", t.chatID)
	params.Add("text", msg)

	req, err := http.NewRequestWithContext(ctx, "POST", apiURL, nil)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	req.URL.RawQuery = params.Encode()

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to send telegram message: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("telegram API returned error: %s", resp.Status)
	}

	return nil
}

func (t *TelegramNotifier) ChannelName() string {
	return "Telegram"
}
