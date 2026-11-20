package luxmed

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"time"
)

type Browser struct {
	api    string
	viewer url.URL
}

func NewBrowser(api, viewer string) (*Browser, error) {
	endpoint, apiErr := url.Parse(api)
	display, viewerErr := url.Parse(viewer)
	if apiErr != nil || viewerErr != nil || !validOrigin(endpoint) || !validOrigin(display) ||
		(display.Scheme != "https" && display.Hostname() != "localhost") {
		return nil, errors.New("invalid browser worker or viewer URL")
	}
	return &Browser{api: endpoint.Scheme + "://" + endpoint.Host, viewer: *display}, nil
}

func validOrigin(u *url.URL) bool {
	return u != nil && (u.Scheme == "http" || u.Scheme == "https") && u.Hostname() != "" &&
		u.User == nil && u.RawQuery == "" && u.Fragment == "" && (u.Path == "" || u.Path == "/")
}

func (b *Browser) Client() *HTTPClient {
	client := newHTTPClient(b.api, false)
	client.browser = true
	client.client.Timeout = 30 * time.Second
	return client
}

func (b *Browser) Check(ctx context.Context) error {
	_, err := b.Client().get(ctx, "/session")
	return err
}

func (b *Browser) ViewerLink(ctx context.Context) (string, error) {
	body, err := b.Client().request(ctx, http.MethodPost, "/viewer", nil)
	if err != nil {
		return "", err
	}
	var parsed struct {
		Ticket string `json:"ticket"`
	}
	if json.Unmarshal(body, &parsed) != nil || len(parsed.Ticket) != 64 {
		return "", ErrResponse
	}
	if _, err := hex.DecodeString(parsed.Ticket); err != nil {
		return "", ErrResponse
	}
	link := b.viewer
	link.Path = "/connect"
	link.Fragment = parsed.Ticket
	return link.String(), nil
}
