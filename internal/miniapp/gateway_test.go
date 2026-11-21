package miniapp

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/coder/websocket"
)

func TestViewerChecksOriginConsumesTicketAndProxiesBinary(t *testing.T) {
	const clientOrigin = "https://app.example"
	const workerOrigin = "http://localhost:18769"
	workerToken := strings.Repeat("b", 64)
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/viewer":
			if r.Method != http.MethodPost {
				http.Error(w, "expected POST", http.StatusMethodNotAllowed)
				return
			}
			fmt.Fprintf(w, `{"ticket":%q}`, workerToken)
		case "/vnc":
			if r.Header.Get("Origin") != workerOrigin ||
				r.Header.Get("Sec-WebSocket-Protocol") != "binary, ticket-"+workerToken ||
				r.Header.Get("Cookie") != "" || r.Header.Get("Authorization") != "" {
				http.Error(w, "invalid worker upgrade", http.StatusForbidden)
				return
			}
			socket, err := websocket.Accept(w, r, &websocket.AcceptOptions{
				OriginPatterns: []string{"localhost:18769"}, Subprotocols: []string{"binary"},
			})
			if err != nil {
				return
			}
			defer socket.CloseNow()
			if err := socket.Write(r.Context(), websocket.MessageBinary, []byte("RFB 003.008\n")); err == nil {
				socket.Read(r.Context())
			}

		default:
			http.NotFound(w, r)
		}
	}))
	defer worker.Close()

	a, err := newApp(Config{
		Origin: clientOrigin, WorkerAPI: worker.URL, WorkerViewer: worker.URL + "/vnc",
		ViewerOrigin: workerOrigin, BotID: 123, OwnerID: 321,
	})
	if err != nil {
		t.Fatal(err)
	}

	a.mu.Lock()
	a.view = newTicket(time.Minute)
	token := a.view.token
	a.mu.Unlock()
	server := httptest.NewServer(a.public(fstest.MapFS{}, fstest.MapFS{}))
	defer server.Close()
	endpoint := "ws" + strings.TrimPrefix(server.URL, "http") + "/vnc"
	dial := func(origin, token string) (*websocket.Conn, *http.Response, error) {
		t.Helper()
		return websocket.Dial(t.Context(), endpoint, &websocket.DialOptions{
			HTTPHeader:   http.Header{"Origin": {origin}, "Cookie": {"private=1"}, "Authorization": {"private"}},
			Subprotocols: []string{"binary", "viewer-" + token},
		})
	}
	if _, res, err := dial("https://other.example", token); err == nil || (res == nil || res.StatusCode != http.StatusForbidden) {
		t.Fatalf("unexpected cross-origin result: %v", err)
	}
	if _, res, err := dial(clientOrigin, strings.Repeat("1", 64)); err == nil || (res == nil || res.StatusCode != http.StatusForbidden) {
		t.Fatalf("unexpected invalid ticket result: %v", err)
	}
	client, _, err := dial(clientOrigin, token)
	if err != nil {
		t.Fatal(err)
	}
	defer client.CloseNow()
	kind, message, err := client.Read(t.Context())
	if err != nil || kind != websocket.MessageBinary || string(message) != "RFB 003.008\n" {
		t.Fatalf("unexpected RFB frame %q: %v", message, err)
	}
	client.CloseNow()
	if _, res, err := dial(clientOrigin, token); err == nil || (res == nil || res.StatusCode != http.StatusForbidden) {
		t.Fatalf("unexpected reused ticket result: %v", err)
	}
}
