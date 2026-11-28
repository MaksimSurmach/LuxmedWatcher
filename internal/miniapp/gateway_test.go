package miniapp

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"github.com/coder/websocket"
	"github.com/maksimsurmach/luxmed-watcher/internal/i18n"
	"github.com/maksimsurmach/luxmed-watcher/internal/luxmed"
)

func signTelegram(userID, botID int64, when time.Time, key ed25519.PrivateKey) string {
	user := fmt.Sprintf(`{"id":%d}`, userID)
	auth := strconv.FormatInt(when.Unix(), 10)
	message := fmt.Sprintf("%d:WebAppData\nauth_date=%s\nuser=%s", botID, auth, user)
	signature := base64.RawURLEncoding.EncodeToString(ed25519.Sign(key, []byte(message)))
	return url.Values{"auth_date": {auth}, "user": {user}, "signature": {signature}}.Encode()
}

func TestGatewayRoutesIndependentViewerStreams(t *testing.T) {
	const origin = "https://app.example"
	const workerOrigin = "http://localhost:18769"
	const botID = 123
	ticketKey := strings.Repeat("3", 64)
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	makeWorker := func(marker string) luxmed.BrowserRoute {
		token := strings.Repeat(marker, 64)
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/viewer":
				if r.Method != http.MethodPost {
					http.Error(w, "expected POST", http.StatusMethodNotAllowed)
					return
				}
				fmt.Fprintf(w, `{"ticket":%q}`, token)
			case "/vnc":
				if r.Header.Get("Origin") != workerOrigin ||
					r.Header.Get("Sec-WebSocket-Protocol") != "binary, ticket-"+token ||
					r.Header.Get("Cookie") != "" || r.Header.Get("Authorization") != "" ||
					r.Header.Get("X-Telegram-Init-Data") != "" {
					http.Error(w, "invalid worker upgrade", http.StatusForbidden)
					return
				}
				conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
					OriginPatterns: []string{"localhost:18769"}, Subprotocols: []string{"binary"},
				})
				if err != nil {
					return
				}
				defer conn.CloseNow()
				if err := conn.Write(r.Context(), websocket.MessageBinary, []byte(marker)); err != nil {
					return
				}
				kind, data, err := conn.Read(r.Context())
				if err == nil {
					conn.Write(r.Context(), kind, append([]byte(marker), data...))
				}
			default:
				http.NotFound(w, r)
			}
		}))
		t.Cleanup(server.Close)
		return luxmed.BrowserRoute{API: server.URL, Viewer: server.URL + "/vnc"}
	}
	var blocked atomic.Int64
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := verifiedTelegramID(r.Header.Get("X-Telegram-Init-Data"), botID, time.Now(), time.Minute, pub)
		if r.URL.Path != dashboardPrefix+"viewer-access" || r.Header.Get("Origin") != origin || !ok || blocked.Load() == id {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer core.Close()
	a, err := newApp(Config{
		Origin: origin, ViewerOrigin: workerOrigin, BotID: botID, DashboardAPI: core.URL, TicketKey: ticketKey,
		Workers: map[int64]luxmed.BrowserRoute{12: makeWorker("a"), 34: makeWorker("b")},
	})
	if err != nil {
		t.Fatal(err)
	}
	a.key = pub
	public := a.public(fstest.MapFS{}, fstest.MapFS{})
	issue := func(id int64, status int) string {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/ticket", nil)
		req.SetPathValue("telegramID", strconv.FormatInt(id, 10))
		if status != http.StatusUnauthorized {
			req.Header.Set("Authorization", "Bearer "+ticketKey)
		}
		res := httptest.NewRecorder()
		a.issue(res, req)
		if res.Code != status {
			t.Fatalf("issue status %d, want %d", res.Code, status)
		}
		var result struct{ Ticket string }
		if status == http.StatusOK && json.Unmarshal(res.Body.Bytes(), &result) != nil {
			t.Fatal("invalid ticket response")
		}
		return result.Ticket
	}
	redeem := func(id int64, ticket string, status int) string {
		t.Helper()
		body, _ := json.Marshal(map[string]string{"initData": signTelegram(id, botID, time.Now(), priv), "ticket": ticket})
		req := httptest.NewRequest(http.MethodPost, "/api/session", strings.NewReader(string(body)))
		req.Header.Set("Origin", origin)
		req.Header.Set("Content-Type", "application/json")
		res := httptest.NewRecorder()
		public.ServeHTTP(res, req)
		if res.Code != status {
			t.Fatalf("redeem status %d, want %d", res.Code, status)
		}
		var result struct{ ViewerToken string }
		if status == http.StatusOK && json.Unmarshal(res.Body.Bytes(), &result) != nil {
			t.Fatal("invalid viewer response")
		}
		return result.ViewerToken
	}
	issue(12, http.StatusUnauthorized)
	launchA := issue(12, http.StatusOK)
	launchB := issue(34, http.StatusOK)
	issue(99, http.StatusNotFound)
	redeem(34, launchA, http.StatusUnauthorized)
	blocked.Store(34)
	redeem(34, launchB, http.StatusForbidden)
	blocked.Store(0)
	viewA := redeem(12, launchA, http.StatusOK)
	viewB := redeem(34, launchB, http.StatusOK)
	redeem(12, launchA, http.StatusUnauthorized)

	server := httptest.NewServer(public)
	defer server.Close()
	endpoint := "ws" + strings.TrimPrefix(server.URL, "http") + "/vnc"
	dial := func(origin, token string) (*websocket.Conn, *http.Response, error) {
		return websocket.Dial(t.Context(), endpoint, &websocket.DialOptions{
			HTTPHeader: http.Header{
				"Origin": {origin}, "Cookie": {"private=1"}, "Authorization": {"private"}, "X-Telegram-Init-Data": {"private"},
			},
			Subprotocols: []string{"binary", "viewer-" + token},
		})
	}
	reject := func(origin, token string) {
		t.Helper()
		if conn, res, err := dial(origin, token); err == nil || res == nil || res.StatusCode != http.StatusForbidden {
			if conn != nil {
				conn.CloseNow()
			}
			t.Fatalf("unexpected viewer result: %v", err)
		}
	}
	reject("https://other.example", viewA)
	reject(origin, strings.Repeat("1", 64))
	aStream, _, err := dial(origin, viewA)
	if err != nil {
		t.Fatal(err)
	}
	defer aStream.CloseNow()
	bStream, _, err := dial(origin, viewB)
	if err != nil {
		t.Fatal(err)
	}
	defer bStream.CloseNow()
	issue(12, http.StatusConflict)
	issue(34, http.StatusConflict)
	reject(origin, viewB)
	for marker, stream := range map[string]*websocket.Conn{"a": aStream, "b": bStream} {
		kind, data, err := stream.Read(t.Context())
		if err != nil || kind != websocket.MessageBinary || string(data) != marker {
			t.Fatalf("wrong worker stream: %q %v", data, err)
		}
		if err := stream.Write(t.Context(), websocket.MessageBinary, []byte("test")); err != nil {
			t.Fatal(err)
		}
		_, echo, err := stream.Read(t.Context())
		if err != nil || string(echo) != marker+"test" {
			t.Fatalf("wrong worker echo: %q %v", echo, err)
		}
	}
}

func TestSharedViewerAnnouncesCDPAndUsesPrivateWorkerScope(t *testing.T) {
	const origin = "https://app.example"
	const workerOrigin = "http://localhost:18769"
	key := strings.Repeat("d", 64)
	workerToken := strings.Repeat("a", 64)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+key {
			t.Error("worker authorization was omitted")
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/accounts/12/viewer":
			fmt.Fprintf(w, `{"ticket":%q}`, workerToken)
		case "/accounts/12/vnc":
			if r.Header.Get("X-Telegram-Init-Data") != "" || r.Header.Get("Sec-WebSocket-Protocol") != "binary, ticket-"+workerToken {
				t.Error("unexpected viewer proxy headers")
			}
			conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: []string{"localhost:18769"}, Subprotocols: []string{"binary"}})
			if err == nil {
				defer conn.CloseNow()
				_ = conn.Write(r.Context(), websocket.MessageBinary, []byte("frame"))
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer core.Close()
	a, err := newApp(Config{
		Origin: origin, ViewerOrigin: workerOrigin, BotID: 42, DashboardAPI: core.URL, WorkerKey: key,
		Workers: map[int64]luxmed.BrowserRoute{12: {
			API: upstream.URL + "/accounts/12", Viewer: upstream.URL + "/accounts/12/vnc", Shared: true,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	a.workers[12].launch = newTicket(time.Minute)
	res := httptest.NewRecorder()
	a.exchange(res, httptest.NewRequest(http.MethodPost, "/api/session", nil), 12, a.workers[12].launch.token, "")
	var body struct{ ViewerToken, Transport string }
	if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil || body.Transport != "cdp" {
		t.Fatalf("expected CDP session response: %s", res.Body)
	}
	server := httptest.NewServer(a.public(fstest.MapFS{}, fstest.MapFS{}))
	defer server.Close()
	conn, _, err := websocket.Dial(t.Context(), "ws"+strings.TrimPrefix(server.URL, "http")+"/vnc", &websocket.DialOptions{
		HTTPHeader:   http.Header{"Origin": {origin}, "Authorization": {"arbitrary"}, "X-Telegram-Init-Data": {"private"}},
		Subprotocols: []string{"binary", "viewer-" + body.ViewerToken},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	if _, frame, err := conn.Read(t.Context()); err != nil || string(frame) != "frame" {
		t.Fatalf("scoped websocket did not receive its frame: %q, %v", frame, err)
	}
}

func TestGatewayServesBundledFontsWithLicense(t *testing.T) {
	handler := (&app{}).public(os.DirFS("../../tools/miniapp/web"), fstest.MapFS{})
	for _, asset := range []struct{ name, contentType, signature string }{
		{"azeret-mono-latin.woff2", "font/woff2", "wOF2"},
		{"azeret-mono-latin-ext.woff2", "font/woff2", "wOF2"},
		{"OFL.txt", "text/plain", "Copyright 2021 The Azeret Project Authors"},
	} {
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/fonts/"+asset.name, nil))
		if res.Code != http.StatusOK || !strings.HasPrefix(res.Header().Get("Content-Type"), asset.contentType) ||
			!strings.HasPrefix(res.Body.String(), asset.signature) || !strings.Contains(res.Header().Get("Content-Security-Policy"), "font-src 'self'") {
			t.Fatalf("failed to serve %s with its expected type, contents and policy", asset.name)
		}
	}
}

func TestGatewayServesLocalizedPagesAndBundledDictionaries(t *testing.T) {
	t.Parallel()

	handler := (&app{}).public(os.DirFS("../../tools/miniapp/web"), fstest.MapFS{})
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/i18n.js", nil))
	const prefix = "export default "
	if res.Code != http.StatusOK || !strings.HasPrefix(res.Body.String(), prefix) ||
		!strings.HasPrefix(res.Header().Get("Content-Type"), "text/javascript") {
		t.Fatalf("dictionary module: status %d, type %s", res.Code, res.Header().Get("Content-Type"))
	}
	var dictionaries map[string]map[string]string
	if err := json.Unmarshal(res.Body.Bytes()[len(prefix):], &dictionaries); err != nil {
		t.Fatal(err)
	}
	boot := httptest.NewRecorder()
	handler.ServeHTTP(boot, httptest.NewRequest(http.MethodGet, "/boot.js", nil))
	if boot.Code != http.StatusOK || !strings.HasPrefix(boot.Header().Get("Content-Type"), "text/javascript") {
		t.Fatalf("boot module: status %d, type %s", boot.Code, boot.Header().Get("Content-Type"))
	}
	for _, locale := range i18n.SupportedLocales {
		t.Run(locale, func(t *testing.T) {
			for _, path := range []string{"/", "/app"} {
				req := httptest.NewRequest(http.MethodGet, path+"?lang="+locale, nil)
				req.Header.Set("Accept-Language", "en-US")
				res := httptest.NewRecorder()
				handler.ServeHTTP(res, req)
				labels := dictionaries[locale]
				body := res.Body.String()
				if res.Code != http.StatusOK ||
					!strings.Contains(body, `<html lang="`+locale+`">`) ||
					!strings.Contains(body, `src="/boot.js"`) {
					t.Errorf("%s: incorrect locale or missing boot script", path)
				}
				for _, key := range []string{"common.javascript_required", "dashboard.loading", "error.unavailable", "common.retry"} {
					want := template.HTMLEscapeString(labels[key])
					if want == "" || !strings.Contains(body, want) {
						t.Errorf("%s: missing localized bootstrap label %s", path, key)
					}
				}
			}
		})
	}
	req := httptest.NewRequest(http.MethodGet, "/app", nil)
	req.Header.Set("Accept-Language", "fr-FR, en-US;q=0.5, uk-UA;q=0.7, be;q=0")
	res = httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	if !strings.Contains(res.Body.String(), `<html lang="uk">`) {
		t.Error("expected the highest-ranked supported browser locale")
	}
}
