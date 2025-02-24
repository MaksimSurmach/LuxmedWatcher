package notifiers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
)

type WebhookNotifierConfig struct {
	URL     string            `yaml:"url"`
	Method  string            `yaml:"method"`
	Headers map[string]string `yaml:"headers"`
	Body    map[string]any    `yaml:"body"`
}

type WebhookNotifier struct {
	config WebhookNotifierConfig
	client *http.Client
}

func NewWebhookNotifier(cfg WebhookNotifierConfig) *WebhookNotifier {
	if cfg.Method == "" {
		cfg.Method = http.MethodPost // по умолчанию POST
	}
	return &WebhookNotifier{
		config: cfg,
		client: &http.Client{},
	}
}

// Send — отправляет уведомление по Webhook.
func (w *WebhookNotifier) Send(msg string) error {
	// Формируем тело (JSON) на основе config.Body + msg
	payload := make(map[string]any)
	for k, v := range w.config.Body {
		payload[k] = v
	}
	// Добавим/заменим ключ "message" или любой другой для msg
	payload["message"] = msg

	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal webhook payload: %w", err)
	}

	req, err := http.NewRequest(w.config.Method, w.config.URL, bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("failed to create webhook request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	// Дополнительные заголовки
	for k, v := range w.config.Headers {
		req.Header.Set(k, v)
	}

	resp, err := w.client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to send webhook request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// Можно прочесть resp.Body для более точной ошибки
		return fmt.Errorf("webhook responded with status %d", resp.StatusCode)
	}
	return nil
}
