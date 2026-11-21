package luxmed

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

type MiniApp struct {
	agent     string
	public    url.URL
	client    http.Client
	ticketKey string
}

func NewMiniApp(agent, public, ticketKey string) (*MiniApp, error) {
	a, aErr := url.Parse(agent)
	p, pErr := url.Parse(public)
	if aErr != nil || pErr != nil || !validOrigin(a) || !validOrigin(p) || p.Scheme != "https" {
		return nil, errors.New("invalid Mini App URLs")
	}
	if ticketKey != "" && !ValidTicketKey(ticketKey) {
		return nil, errors.New("LUXMED_INTERNAL_KEY must be a 32-byte hex secret")
	}
	return &MiniApp{
		agent: a.Scheme + "://" + a.Host, public: *p, ticketKey: ticketKey,
		client: http.Client{Timeout: 4 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		}},
	}, nil
}

func ValidTicketKey(raw string) bool {
	key, err := hex.DecodeString(raw)
	return err == nil && len(key) == 32
}

func (m *MiniApp) Origin() string {
	return m.public.Scheme + "://" + m.public.Host
}

func (m *MiniApp) DashboardURL() string {
	return m.Origin() + "/app"
}

func (m *MiniApp) Link(ctx context.Context, telegramID int64) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.agent+"/ticket/"+strconv.FormatInt(telegramID, 10), nil)
	if err != nil {
		return "", err
	}
	if m.ticketKey != "" {
		req.Header.Set("Authorization", "Bearer "+m.ticketKey)
	}
	res, err := m.client.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return "", fmt.Errorf("Mini App agent status %d", res.StatusCode)
	}
	var parsed struct{ Ticket string }
	if err := json.NewDecoder(io.LimitReader(res.Body, 512)).Decode(&parsed); err != nil || len(parsed.Ticket) != 64 {
		return "", ErrResponse
	}
	if _, err := hex.DecodeString(parsed.Ticket); err != nil {
		return "", ErrResponse
	}
	link := m.public
	link.Path = "/"
	link.RawQuery = url.Values{"ticket": {parsed.Ticket}}.Encode()
	return link.String(), nil
}
