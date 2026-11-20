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
	"time"
)

type MiniApp struct {
	agent  string
	public url.URL
	client http.Client
}

func NewMiniApp(agent, public string) (*MiniApp, error) {
	a, aErr := url.Parse(agent)
	p, pErr := url.Parse(public)
	if aErr != nil || pErr != nil || !validOrigin(a) || !validOrigin(p) || p.Scheme != "https" {
		return nil, errors.New("invalid Mini App URLs")
	}
	return &MiniApp{agent: a.Scheme + "://" + a.Host, public: *p, client: http.Client{Timeout: 4 * time.Second}}, nil
}

func (m *MiniApp) Link(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.agent+"/ticket", nil)
	if err != nil {
		return "", err
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
