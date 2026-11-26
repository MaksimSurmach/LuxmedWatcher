package luxmed

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

type Browser struct {
	api    string
	key    string
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

func NewRoutedBrowser(id int64, route BrowserRoute, display, key string) (*Browser, error) {
	if !route.Shared {
		return NewBrowser(route.API, display)
	}
	scope := "/accounts/" + strconv.FormatInt(id, 10)
	endpoint := workerEndpoint(route.API, scope)
	if id <= 0 || endpoint == nil || !ValidTicketKey(key) {
		return nil, errors.New("invalid shared browser worker")
	}
	browser, err := NewBrowser(endpoint.Scheme+"://"+endpoint.Host, display)
	if err != nil {
		return nil, err
	}
	browser.api = route.API
	browser.key = key
	return browser, nil
}

func validOrigin(u *url.URL) bool {
	return u != nil && (u.Scheme == "http" || u.Scheme == "https") && u.Hostname() != "" &&
		u.User == nil && u.RawQuery == "" && u.Fragment == "" && (u.Path == "" || u.Path == "/")
}

func (b *Browser) Client() *HTTPClient {
	client := newHTTPClient(b.api, false)
	client.browser = true
	client.workerKey = b.key
	client.client.Timeout = 30 * time.Second
	return client
}

func (b *Browser) Check(ctx context.Context) error {
	_, err := b.Client().get(ctx, "/session")
	return err
}

func (b *Browser) SupportsBackgroundRenewal() bool {
	return b.key != ""
}

func (b *Browser) ViewerLink(ctx context.Context, locale string) (string, error) {
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
	link.RawQuery = url.Values{"lang": {locale}}.Encode()
	link.Fragment = parsed.Ticket
	return link.String(), nil
}
