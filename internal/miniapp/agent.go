package miniapp

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"net/http/httputil"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"
)

type Config struct {
	Origin, WorkerAPI, WorkerViewer, ViewerOrigin string
	BotID, OwnerID                                int64
}

type ticket struct {
	token   string
	expires time.Time
}

func (t ticket) valid(token string) bool {
	return t.token != "" && t.token == token && time.Now().Before(t.expires)
}

func newTicket(ttl time.Duration) ticket {
	var nonce [32]byte
	rand.Read(nonce[:])
	return ticket{token: hex.EncodeToString(nonce[:]), expires: time.Now().Add(ttl)}
}

type app struct {
	config Config
	client *http.Client
	proxy  *httputil.ReverseProxy
	mu     sync.Mutex
	launch ticket
	view   ticket
	active bool
}

func New(cfg Config, web, vendor fs.FS) (public, private http.Handler, err error) {
	a, err := newApp(cfg)
	if err != nil {
		return nil, nil, err
	}
	local := http.NewServeMux()
	local.HandleFunc("POST /ticket", a.issue)
	return a.public(web, vendor), local, nil
}

func newApp(cfg Config) (*app, error) {
	if !validURL(cfg.Origin, "https", "") || !validURL(cfg.WorkerAPI, "http", "") ||
		!validURL(cfg.WorkerViewer, "http", "/vnc") || !validURL(cfg.ViewerOrigin, "http", "") ||
		cfg.BotID <= 0 || cfg.OwnerID <= 0 {
		return nil, errors.New("invalid Mini App settings")
	}
	upstream, err := url.Parse(cfg.WorkerViewer)
	if err != nil {
		return nil, err
	}
	upstreamOrigin := &url.URL{Scheme: upstream.Scheme, Host: upstream.Host}
	a := &app{config: cfg, client: &http.Client{Timeout: 18 * time.Second}}
	a.proxy = &httputil.ReverseProxy{
		Rewrite: func(request *httputil.ProxyRequest) {
			request.SetURL(upstreamOrigin)
			request.Out.URL.Path = upstream.Path
			request.Out.Header.Set("Origin", cfg.ViewerOrigin)
		},
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, _ error) {
			reply(w, http.StatusBadGateway, nil)
		},
	}
	return a, nil
}

func validURL(raw, scheme, path string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == scheme && u.Hostname() != "" && u.User == nil && u.Path == path &&
		u.Fragment == "" && u.RawQuery == "" && !u.ForceQuery
}

func (a *app) issue(w http.ResponseWriter, _ *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.active {
		reply(w, http.StatusConflict, nil)
		return
	}
	a.view = ticket{}
	a.launch = newTicket(5 * time.Minute)
	reply(w, http.StatusOK, map[string]string{"ticket": a.launch.token})
}

func (a *app) session(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Origin") != a.config.Origin || !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		reply(w, http.StatusForbidden, nil)
		return
	}
	var input struct {
		InitData string `json:"initData"`
		Ticket   string `json:"ticket"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&input); err != nil {
		reply(w, http.StatusBadRequest, nil)
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.active || !a.launch.valid(input.Ticket) ||
		!authorizedUser(input.InitData, a.config.BotID, a.config.OwnerID, time.Now(), productionKey()) {
		reply(w, http.StatusUnauthorized, nil)
		return
	}
	a.launch = ticket{}
	a.view = newTicket(45 * time.Second)
	reply(w, http.StatusOK, map[string]string{"viewerToken": a.view.token})
}

func (a *app) viewer(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Origin") != a.config.Origin || r.URL.RawQuery != "" ||
		!strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		reply(w, http.StatusForbidden, nil)
		return
	}
	protocols := strings.Split(r.Header.Get("Sec-WebSocket-Protocol"), ",")
	for i := range protocols {
		protocols[i] = strings.TrimSpace(protocols[i])
	}
	a.mu.Lock()
	if a.active || !a.view.valid(a.view.token) || !slices.Contains(protocols, "binary") ||
		!slices.Contains(protocols, "viewer-"+a.view.token) {
		a.mu.Unlock()
		reply(w, http.StatusForbidden, nil)
		return
	}
	a.view = ticket{}
	a.active = true
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		a.active = false
		a.mu.Unlock()
	}()

	workerTicket, err := a.workerTicket(r.Context())
	if err != nil {
		reply(w, http.StatusServiceUnavailable, nil)
		return
	}
	r = r.Clone(r.Context())
	r.Header.Set("Sec-WebSocket-Protocol", "binary, ticket-"+workerTicket)
	r.Header.Del("Cookie")
	r.Header.Del("Authorization")
	a.proxy.ServeHTTP(w, r)
}

func (a *app) workerTicket(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.config.WorkerAPI+"/viewer", nil)
	if err != nil {
		return "", err
	}
	res, err := a.client.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return "", errors.New("browser viewer is unavailable")
	}
	var t struct{ Ticket string }
	if err := json.NewDecoder(io.LimitReader(res.Body, 512)).Decode(&t); err != nil || len(t.Ticket) != 64 {
		return "", errors.New("invalid browser viewer ticket")
	}
	if _, err := hex.DecodeString(t.Ticket); err != nil {
		return "", errors.New("invalid browser viewer ticket")
	}
	return t.Ticket, nil
}

func reply(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if body != nil {
		json.NewEncoder(w).Encode(body)
	}
}
