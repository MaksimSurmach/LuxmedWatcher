package channels

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"github.com/go-playground/validator/v10"
	"LuxmedWatcher/internal/core/notification"
	"context"
)

type WebhookNotifier struct {
	URL     string `validate:"required,url"`
	Method  string `mapstructure:"method"`
	Headers map[string]string `mapstructure:"headers"`
	BaseBody map[string]interface{} `mapstructure:"body"`
}

func (w *WebhookNotifier) Create(config interface{}) (notification.Notifier, error) {
	cfg, ok := config.(WebhookNotifier)
	if !ok {
		return nil, fmt.Errorf("invalid config type for WebhookNotifier")
	}
	val := validator.New()
	err := val.Struct(cfg)
	if err != nil {
		return nil, fmt.Errorf("invalid webhook notifier config: %w", err)
	}
	
	if cfg.Method == "" {
		cfg.Method = "POST"
	}
	if cfg.Headers == nil {
		cfg.Headers = make(map[string]string)
	}
	if cfg.BaseBody == nil {
		cfg.BaseBody = make(map[string]interface{})
	}

	return &WebhookNotifier{
		URL:     cfg.URL,
		Method:  cfg.Method,
		Headers: cfg.Headers,
		BaseBody: cfg.BaseBody,
	}, nil
}

func (w *WebhookNotifier) SendMessage(ctx context.Context, msg string) error {
	bodyMap := make(map[string]interface{}, len(w.BaseBody))
	for k, v := range w.BaseBody {
		bodyMap[k] = v
	}
	// Add message to the body
	bodyMap["message"] = msg

	data, err := json.Marshal(bodyMap)
	if err != nil {
		return err
	}

	req, err := http.NewRequest(w.Method, w.URL, bytes.NewBuffer(data))
	if err != nil {
		return err
	}

	for k, v := range w.Headers {
		req.Header.Set(k, v)
	}

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("webhook error: status %d", resp.StatusCode)
	}

	return nil
}

func (w *WebhookNotifier) ChannelName() string {
	return "Webhook"
}
