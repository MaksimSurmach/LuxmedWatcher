package bot

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"strings"
	"testing"
	"time"

	"github.com/maksimsurmach/luxmed-watcher/internal/domain"
	"github.com/maksimsurmach/luxmed-watcher/internal/scheduler"
)

func TestNotifyLuxMedReconnectDeliveryOutcomes(t *testing.T) {
	for _, test := range []struct {
		name           string
		status         int
		response       string
		wantError      string
		retryAfter     time.Duration
		forbiddenLimit int
	}{
		{
			name: "accepted", status: 200,
			response: `{"ok":true,"result":{"message_id":42}}`,
		},
		{
			name: "rate limited", status: 429,
			response:   `{"ok":false,"error_code":429,"parameters":{"retry_after":123}}`,
			wantError:  "telegram temporarily rejected reconnect warning",
			retryAfter: 123 * time.Second,
		},
		{
			name: "blocked", status: 403,
			response:       `{"ok":false,"error_code":403,"description":"bot was blocked by the user"}`,
			wantError:      "telegram temporarily rejected reconnect warning",
			retryAfter:     6 * time.Hour,
			forbiddenLimit: 4,
		},
		{
			name: "bad request", status: 400,
			response:  `{"ok":false,"error_code":400}`,
			wantError: "telegram permanently rejected reconnect warning",
		},
		{
			name: "unknown", status: 502, response: "invalid body",
			wantError: "telegram reconnect delivery outcome unknown",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			b := testBotWithTelegram(t, func(w http.ResponseWriter, r *http.Request) {
				if err := r.ParseForm(); err != nil {
					t.Error(err)
				}
				if r.URL.Path != "/botfake/sendMessage" || r.Form.Get("chat_id") != "101" ||
					!strings.Contains(r.Form.Get("reply_markup"), `"callback_data":"account"`) {
					t.Errorf("unexpected recipient or reconnect action: %s %v", r.URL.Path, r.Form)
				}
				w.WriteHeader(test.status)
				fmt.Fprint(w, test.response)
			})
			user := createUser(t, b, 101, domain.UserStatusActive)
			err := b.NotifyLuxMedReconnect(t.Context(), user)
			if test.wantError == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil || err.Error() != test.wantError {
				t.Fatalf("delivery error = %v, want %s", err, test.wantError)
			}
			rejection, retryable := errors.AsType[*scheduler.ReconnectDeliveryRejected](err)
			if retryable != (test.retryAfter > 0) || retryable && (rejection.RetryAfter != test.retryAfter || rejection.ForbiddenLimit != test.forbiddenLimit) {
				t.Fatalf("incorrect retry classification: %v", rejection)
			}
		})
	}
}

type reconnectTransportFunc func(*http.Request) (*http.Response, error)

func (f reconnectTransportFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestNotifyLuxMedReconnectRetriesOnlyKnownUnsentRequests(t *testing.T) {
	for _, test := range []struct {
		name      string
		canceled  bool
		connected bool
		failure   error
		retry     bool
	}{
		{name: "canceled before send", canceled: true, retry: true},
		{name: "connection refused", failure: &net.OpError{Op: "dial", Err: errors.New("connection refused")}, retry: true},
		{name: "canceled before connection", failure: context.Canceled, retry: true},
		{name: "canceled after connection", connected: true, failure: context.Canceled},
		{name: "connection lost after send", connected: true, failure: &net.OpError{Op: "read", Err: errors.New("connection reset")}},
	} {
		t.Run(test.name, func(t *testing.T) {
			b := testBot(t)
			user := createUser(t, b, 101, domain.UserStatusActive)
			b.api.Client = &http.Client{Transport: reconnectTransportFunc(func(req *http.Request) (*http.Response, error) {
				if test.canceled {
					t.Error("attempted to send with an already canceled context")
				}
				if trace := httptrace.ContextClientTrace(req.Context()); trace != nil && test.connected {
					trace.GotConn(httptrace.GotConnInfo{})
				}
				return nil, test.failure
			})}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if test.canceled {
				cancel()
			}
			err := b.NotifyLuxMedReconnect(ctx, user)
			if _, retry := errors.AsType[*scheduler.ReconnectDeliveryRejected](err); retry != test.retry {
				t.Fatalf("retryable = %t, want %t (%v)", retry, test.retry, err)
			}
		})
	}
}

func TestNotifyLuxMedReconnectRetriesTLSHandshakeTimeout(t *testing.T) {
	b := testBot(t)
	user := createUser(t, b, 101, domain.UserStatusActive)
	hello := make(chan struct{}, 1)
	release := make(chan struct{})
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("sendMessage reached Telegram despite the handshake timeout")
	}))
	server.TLS = &tls.Config{
		GetConfigForClient: func(*tls.ClientHelloInfo) (*tls.Config, error) {
			hello <- struct{}{}
			<-release
			return nil, nil
		},
	}
	server.StartTLS()
	t.Cleanup(server.Close)
	t.Cleanup(func() { close(release) })

	transport := server.Client().Transport.(*http.Transport).Clone()
	transport.TLSHandshakeTimeout = 250 * time.Millisecond
	t.Cleanup(transport.CloseIdleConnections)
	b.api.Client = &http.Client{Transport: transport}
	b.api.SetAPIEndpoint(server.URL + "/bot%s/%s")
	err := b.NotifyLuxMedReconnect(t.Context(), user)
	select {
	case <-hello:
	default:
		t.Fatal("request did not reach the TLS handshake")
	}
	rejected, ok := errors.AsType[*scheduler.ReconnectDeliveryRejected](err)
	if !ok || !rejected.BeforeSend {
		t.Fatalf("TLS timeout must be retryable before send: %v", err)
	}
}
