package bot

import (
	"context"
	"crypto/tls"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/maksimsurmach/luxmed-watcher/internal/domain"
	"github.com/maksimsurmach/luxmed-watcher/internal/luxmed"
	"github.com/maksimsurmach/luxmed-watcher/internal/scheduler"
	"github.com/maksimsurmach/luxmed-watcher/internal/storage"
)

func TestTelegramUnblockResumesReconnectWarning(t *testing.T) {
	var sends atomic.Int32
	b := testBotWithTelegram(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/getUpdates") {
			fmt.Fprint(w, `{"ok":true,"result":[{"update_id":1,"my_chat_member":{"chat":{"id":42,"type":"private"},"from":{"id":42},"old_chat_member":{"status":"kicked"},"new_chat_member":{"status":"member","user":{"id":99}}}}]}`)
			return
		}
		if !strings.HasSuffix(r.URL.Path, "/sendMessage") {
			t.Errorf("unexpected Telegram call: %s", r.URL.Path)
		}
		sends.Add(1)
		fmt.Fprint(w, `{"ok":true,"result":{"message_id":42}}`)
	})
	ctx := t.Context()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(t.TempDir(), "unblock.sqlite")
	db, err := storage.Open(ctx, path)
	must(err)
	t.Cleanup(func() { db.Close() })
	b.db = db
	faults, err := sql.Open("sqlite", path)
	must(err)
	t.Cleanup(func() { faults.Close() })
	user := createUser(t, b, 42, domain.UserStatusActive)
	id, err := b.db.CreateWatch(ctx, domain.Watch{UserID: user.ID, CityID: 1, ServiceID: 1, NextDays: 1})
	must(err)
	watch, err := b.db.Watch(ctx, id)
	must(err)
	must(b.db.BeginLuxMedReconnect(ctx, user.ID))
	now := time.Now().Add(-30 * time.Hour)
	for i := range reconnectForbiddenLimit {
		attemptAt := now.Add(time.Duration(i) * 6 * time.Hour)
		claim, err := b.db.ClaimLuxMedReconnectDelivery(ctx, user.ID, attemptAt)
		must(err)
		if claim.Attempt != i+1 {
			t.Fatalf("attempt = %d, want %d", claim.Attempt, i+1)
		}
		must(b.db.RecordLuxMedReconnectRejection(ctx, user.ID, claim, attemptAt.Add(6*time.Hour), reconnectForbiddenLimit))
	}
	claim, err := b.db.ClaimLuxMedReconnectDelivery(ctx, user.ID, time.Now())
	must(err)
	if claim.Attempt != 0 {
		t.Fatal("403 limit did not suspend delivery")
	}

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(upstream.Close)
	browser, err := luxmed.NewBrowser(upstream.URL, "http://localhost:18769")
	must(err)
	runner := scheduler.NewRunner(b.db, b.cryptor, b, 0, time.Hour, false)
	runner.UseBrowsers(map[int64]*luxmed.Browser{user.TelegramUserID: browser})
	b.SetRunner(runner)
	updates, err := b.getUpdates(tgbotapi.NewUpdate(0))
	must(err)
	if len(updates) != 1 {
		t.Fatalf("received %d membership updates", len(updates))
	}
	updates = append(updates, telegramUpdate{Update: tgbotapi.Update{UpdateID: 2}})
	config := tgbotapi.NewUpdate(0)
	_, err = faults.ExecContext(ctx, `
		CREATE TRIGGER fail_rearm BEFORE UPDATE ON luxmed_reconnect_delivery
		BEGIN SELECT RAISE(ABORT, 'simulated write failure'); END
	`)
	must(err)
	must(b.processUpdates(ctx, &config, updates))
	b.processTelegramUnblocks(ctx)
	pending, err := b.db.PendingTelegramUnblocks()
	must(err)
	if config.Offset != 3 || len(pending) != 1 {
		t.Fatalf("failed rearm blocked polling or lost the event: offset=%d pending=%v", config.Offset, pending)
	}
	_, err = faults.ExecContext(ctx, `DROP TRIGGER fail_rearm`)
	must(err)
	// A fresh storage handle must recover the queued event without Telegram redelivery.
	reopened, err := storage.Open(ctx, path)
	must(err)
	t.Cleanup(func() { reopened.Close() })
	b.db = reopened
	runner = scheduler.NewRunner(reopened, b.cryptor, b, 0, time.Hour, false)
	other := createUser(t, b, 43, domain.UserStatusActive)
	otherID, err := reopened.CreateWatch(ctx, domain.Watch{UserID: other.ID, CityID: 1, ServiceID: 1, NextDays: 1})
	must(err)
	otherWatch, err := reopened.Watch(ctx, otherID)
	must(err)
	runner.UseBrowsers(map[int64]*luxmed.Browser{user.TelegramUserID: browser, other.TelegramUserID: browser})
	b.SetRunner(runner)
	must(os.WriteFile(filepath.Join(path+".reconnect", fmt.Sprintf("%d.json", user.ID)), []byte("corrupt"), 0o600))
	b.processTelegramUnblocks(ctx)
	pending, err = b.db.PendingTelegramUnblocks()
	must(err)
	if len(pending) != 0 {
		t.Fatalf("unreadable refusal journal prevented the queued unblock: %v", pending)
	}
	for range 2 {
		config.Offset = 0
		must(b.processUpdates(ctx, &config, updates))
		b.processTelegramUnblocks(ctx)
		if err := runner.CheckWatch(ctx, watch); !errors.Is(err, luxmed.ErrAuthentication) {
			t.Fatalf("check watch: %v", err)
		}
	}
	pending, err = b.db.PendingTelegramUnblocks()
	must(err)
	if len(pending) != 0 {
		t.Fatalf("successful retry left pending events: %v", pending)
	}
	if sends := sends.Load(); sends != 1 {
		t.Fatalf("delivered %d warnings, want one", sends)
	}
	if err := runner.CheckWatch(ctx, otherWatch); !errors.Is(err, luxmed.ErrAuthentication) ||
		errors.Is(err, storage.ErrReconnectJournalUnreadable) || sends.Load() != 2 {
		t.Fatalf("corrupt journal affected another user: sends=%d err=%v", sends.Load(), err)
	}
}

func TestNotifyLuxMedReconnectDeliveryOutcomes(t *testing.T) {
	for _, test := range []struct {
		name           string
		status         int
		response       string
		wantError      string
		retryAfter     time.Duration
		truncated      bool
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
			name: "rate limit with truncated body", status: 429, truncated: true,
			response: `{"ok":false,`, wantError: "telegram temporarily rejected reconnect warning",
			retryAfter: 84 * time.Second,
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
			name: "blocked with truncated body", status: 403, truncated: true,
			response: `{"ok":false,`, wantError: "telegram temporarily rejected reconnect warning",
			retryAfter: 6 * time.Hour, forbiddenLimit: 4,
		},
		{
			name: "bad request with truncated body", status: 400, truncated: true,
			response: `{"ok":false,`, wantError: "telegram permanently rejected reconnect warning",
		},
		{
			name: "unauthorized with truncated body", status: 401, truncated: true,
			response: `{"ok":false,`, wantError: "telegram permanently rejected reconnect warning",
		},
		{
			name: "explicit server rejection", status: 502,
			response:   `{"ok":false,"error_code":502,"parameters":{"retry_after":42}}`,
			wantError:  "telegram temporarily rejected reconnect warning",
			retryAfter: 42 * time.Second,
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
				if test.truncated {
					w.Header().Set("Content-Length", "512")
					w.Header().Set("Retry-After", "84")
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
