package app

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/maksimsurmach/luxmed-watcher/internal/storage"
)

func TestPostgresReconnectJournalDirectoryIsIndependentOfSQLitePath(t *testing.T) {
	root := t.TempDir()
	state := filepath.Join(root, "state")
	t.Setenv("XDG_STATE_HOME", state)
	blocked := filepath.Join(root, "not-a-directory")
	if err := os.WriteFile(blocked, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	app := New(Config{
		DatabaseURL:  "postgres://localhost/watcher",
		DatabasePath: filepath.Join(blocked, "obsolete.sqlite"),
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := app.configureReconnectJournal(new(storage.DB)); err != nil {
		t.Fatalf("PostgreSQL startup depends on the SQLite path: %v", err)
	}
	dir, err := postgresReconnectJournal(app.cfg.DatabaseURL)
	if err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		t.Fatalf("independent default journal was not created: %v", err)
	}
	app.cfg.ReconnectJournalDir = filepath.Join(root, "persistent", "reconnect")
	if err := app.configureReconnectJournal(new(storage.DB)); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(app.cfg.ReconnectJournalDir); err != nil || !info.IsDir() {
		t.Fatalf("explicit journal directory was not created: %v", err)
	}
	app.cfg.ReconnectJournalDir = filepath.Join(blocked, "unavailable")
	if err := app.configureReconnectJournal(new(storage.DB)); err == nil {
		t.Fatal("unavailable explicit journal directory was accepted")
	}
}

func TestTelegramHTTPPreservesRequestTrace(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)

	var connected atomic.Bool
	ctx := httptrace.WithClientTrace(t.Context(), &httptrace.ClientTrace{
		GotConn: func(httptrace.GotConnInfo) { connected.Store(true) },
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	client := telegramHTTP{ctx: t.Context(), client: server.Client()}
	res, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if err := res.Body.Close(); err != nil {
		t.Fatal(err)
	}
	if !connected.Load() {
		t.Fatal("Telegram request lost its connection trace")
	}
}
