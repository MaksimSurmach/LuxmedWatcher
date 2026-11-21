package miniapp

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"net/http/httputil"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/maksimsurmach/luxmed-watcher/internal/luxmed"
)

type Config struct {
	Origin, ViewerOrigin, DashboardAPI string
	TicketKey                          string
	BotID                              int64
	Workers                            map[int64]luxmed.BrowserRoute
}

type ticket struct {
	token   string
	expires time.Time
}

func (t ticket) valid(token string) bool {
	return t.token != "" && time.Now().Before(t.expires) &&
		subtle.ConstantTimeCompare([]byte(t.token), []byte(token)) == 1
}

func newTicket(ttl time.Duration) ticket {
	var nonce [32]byte
	rand.Read(nonce[:])
	return ticket{token: hex.EncodeToString(nonce[:]), expires: time.Now().Add(ttl)}
}

type worker struct {
	api    string
	proxy  *httputil.ReverseProxy
	launch ticket
	view   ticket
	active bool
}

type app struct {
	config  Config
	key     ed25519.PublicKey
	client  *http.Client
	workers map[int64]*worker
	mu      sync.Mutex
	views   map[string]*worker
}

func New(cfg Config, web, vendor fs.FS) (public, private http.Handler, err error) {
	a, err := newApp(cfg)
	if err != nil {
		return nil, nil, err
	}
	local := http.NewServeMux()
	local.HandleFunc("POST /ticket/{telegramID}", a.issue)
	return a.public(web, vendor), local, nil
}

func newApp(cfg Config) (*app, error) {
	if !validURL(cfg.Origin, "https", "") || !validURL(cfg.ViewerOrigin, "http", "") ||
		!validURL(cfg.DashboardAPI, "http", "") || cfg.BotID <= 0 || len(cfg.Workers) == 0 ||
		((len(cfg.Workers) > 1 || cfg.TicketKey != "") && !luxmed.ValidTicketKey(cfg.TicketKey)) {
		return nil, errors.New("invalid Mini App settings")
	}
	a := &app{
		config: cfg, key: productionKey(), workers: make(map[int64]*worker, len(cfg.Workers)), views: make(map[string]*worker),
		client: &http.Client{Timeout: 18 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		}},
	}
	for id, route := range cfg.Workers {
		if id <= 0 || !validURL(route.API, "http", "") || !validURL(route.Viewer, "http", "/vnc") {
			return nil, errors.New("invalid Mini App browser worker")
		}
		upstream, _ := url.Parse(route.Viewer)
		origin := &url.URL{Scheme: upstream.Scheme, Host: upstream.Host}
		a.workers[id] = &worker{api: route.API, proxy: &httputil.ReverseProxy{
			Rewrite: func(request *httputil.ProxyRequest) {
				request.SetURL(origin)
				request.Out.URL.Path = upstream.Path
				request.Out.Header.Set("Origin", cfg.ViewerOrigin)
			},
			ErrorHandler: func(w http.ResponseWriter, _ *http.Request, _ error) {
				reply(w, http.StatusBadGateway, nil)
			},
		}}
	}
	return a, nil
}

func validURL(raw, scheme, path string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == scheme && u.Hostname() != "" && u.User == nil && u.Path == path &&
		u.Fragment == "" && u.RawQuery == "" && !u.ForceQuery
}

func (a *app) issue(w http.ResponseWriter, r *http.Request) {
	if a.config.TicketKey != "" {
		key, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if subtle.ConstantTimeCompare([]byte(a.config.TicketKey), []byte(key)) != 1 {
			reply(w, http.StatusUnauthorized, nil)
			return
		}
	}
	id, err := strconv.ParseInt(r.PathValue("telegramID"), 10, 64)
	browser := a.workers[id]
	if err != nil || browser == nil {
		reply(w, http.StatusNotFound, nil)
		return
	}
	a.mu.Lock()
	if browser.active {
		a.mu.Unlock()
		reply(w, http.StatusConflict, nil)
		return
	}
	delete(a.views, browser.view.token)
	browser.view = ticket{}
	browser.launch = newTicket(5 * time.Minute)
	token := browser.launch.token
	a.mu.Unlock()
	reply(w, http.StatusOK, map[string]string{"ticket": token})
}

func (a *app) session(w http.ResponseWriter, r *http.Request) {
	mediaType, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if r.Header.Get("Origin") != a.config.Origin || mediaType != "application/json" {
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
	id, ok := verifiedTelegramID(input.InitData, a.config.BotID, time.Now(), 5*time.Minute, a.key)
	if !ok {
		reply(w, http.StatusUnauthorized, nil)
		return
	}
	a.exchange(w, r, id, input.Ticket, input.InitData)
}

func (a *app) exchange(w http.ResponseWriter, r *http.Request, id int64, launch, initData string) {
	browser := a.workers[id]
	a.mu.Lock()
	valid := browser != nil && !browser.active && browser.launch.valid(launch)
	a.mu.Unlock()
	if !valid {
		reply(w, http.StatusUnauthorized, nil)
		return
	}
	if status := a.access(r.Context(), initData); status != http.StatusNoContent {
		reply(w, status, nil)
		return
	}

	a.mu.Lock()
	if browser.active || !browser.launch.valid(launch) {
		a.mu.Unlock()
		reply(w, http.StatusUnauthorized, nil)
		return
	}
	browser.launch = ticket{}
	delete(a.views, browser.view.token)
	browser.view = newTicket(45 * time.Second)
	token := browser.view.token
	a.views[token] = browser
	a.mu.Unlock()
	reply(w, http.StatusOK, map[string]string{"viewerToken": token})
}

func (a *app) access(ctx context.Context, initData string) int {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.config.DashboardAPI+dashboardPrefix+"viewer-access", nil)
	if err != nil {
		return http.StatusServiceUnavailable
	}
	req.Header.Set("Origin", a.config.Origin)
	req.Header.Set("X-Telegram-Init-Data", initData)
	res, err := a.client.Do(req)
	if err != nil {
		return http.StatusServiceUnavailable
	}
	defer res.Body.Close()
	switch res.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusNoContent:
		return res.StatusCode
	default:
		return http.StatusServiceUnavailable
	}
}

func (a *app) viewer(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Origin") != a.config.Origin || r.URL.RawQuery != "" ||
		!strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		reply(w, http.StatusForbidden, nil)
		return
	}
	protocols := strings.Split(r.Header.Get("Sec-WebSocket-Protocol"), ",")
	var token string
	for i, protocol := range protocols {
		protocols[i] = strings.TrimSpace(protocol)
		if credential, ok := strings.CutPrefix(protocols[i], "viewer-"); ok {
			if token != "" || len(credential) != 64 {
				reply(w, http.StatusForbidden, nil)
				return
			}
			token = credential
		}
	}

	a.mu.Lock()
	browser := a.views[token]
	if browser == nil || browser.active || !browser.view.valid(token) || !slices.Contains(protocols, "binary") {
		a.mu.Unlock()
		reply(w, http.StatusForbidden, nil)
		return
	}
	delete(a.views, token)
	browser.view = ticket{}
	browser.active = true
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		browser.active = false
		a.mu.Unlock()
	}()

	workerToken, err := a.workerTicket(r.Context(), browser.api)
	if err != nil {
		reply(w, http.StatusServiceUnavailable, nil)
		return
	}
	r = r.Clone(r.Context())
	r.Header.Set("Sec-WebSocket-Protocol", "binary, ticket-"+workerToken)
	r.Header.Del("Cookie")
	r.Header.Del("Authorization")
	r.Header.Del("X-Telegram-Init-Data")
	browser.proxy.ServeHTTP(w, r)
}

func (a *app) workerTicket(ctx context.Context, api string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, api+"/viewer", nil)
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
