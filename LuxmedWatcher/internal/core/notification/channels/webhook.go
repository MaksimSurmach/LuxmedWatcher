package channels

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
)

type WebhookNotifier struct {
	URL     string
	Method  string
	Headers map[string]string
	BaseBody map[string]interface{}
}

func NewWebhookNotifier(url, method string, headers map[string]string, baseBody map[string]interface{}) *WebhookNotifier {
	return &WebhookNotifier{
		URL:      url,
		Method:   method,
		Headers:  headers,
		BaseBody: baseBody,
	}
}

func (w *WebhookNotifier) Send(msg string) error {
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
