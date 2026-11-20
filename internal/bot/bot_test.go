package bot

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/maksimsurmach/luxmed-watcher/internal/domain"
	"github.com/maksimsurmach/luxmed-watcher/internal/i18n"
	"github.com/maksimsurmach/luxmed-watcher/internal/luxmed"
	"github.com/maksimsurmach/luxmed-watcher/internal/security"
	"github.com/maksimsurmach/luxmed-watcher/internal/storage"
)

func testBot(t *testing.T) *Bot {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"ok":true,"result":{"id":99,"is_bot":true,"first_name":"Test Bot","message_id":99}}`)
	}))
	t.Cleanup(server.Close)
	api, err := tgbotapi.NewBotAPIWithClient("fake", server.URL+"/bot%s/%s", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	db, err := storage.Open(context.Background(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	catalog, err := i18n.Load("en")
	if err != nil {
		t.Fatal(err)
	}
	cryptor, err := security.NewCryptor(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	return New(api, db, catalog, cryptor, Config{DefaultLocale: "en"}, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func createUser(t *testing.T, b *Bot, id int64, status domain.UserStatus) domain.User {
	t.Helper()
	user, err := b.db.UpsertTelegramUser(context.Background(), domain.User{
		TelegramUserID: id, TelegramChatID: id, Locale: "en", Status: status, Role: domain.UserRoleUser,
	})
	if err != nil {
		t.Fatal(err)
	}
	return user
}

func TestWatchActionsRequireActiveOwnerInPrivateChat(t *testing.T) {
	ctx := context.Background()
	b := testBot(t)
	owner := createUser(t, b, 10, domain.UserStatusActive)
	other := createUser(t, b, 20, domain.UserStatusActive)
	id, err := b.db.CreateWatch(ctx, domain.Watch{UserID: owner.ID, Name: "Example", CityID: 1, ServiceID: 12, CheckIntervalSeconds: 300})
	if err != nil {
		t.Fatal(err)
	}
	callback := func(actor domain.User, chat *tgbotapi.Chat) {
		b.handleCallback(ctx, &tgbotapi.CallbackQuery{
			ID: "test", Data: fmt.Sprintf("watch:delete:%d", id),
			From: &tgbotapi.User{ID: actor.TelegramUserID}, Message: &tgbotapi.Message{Chat: chat},
		})
	}
	callback(other, &tgbotapi.Chat{ID: other.TelegramUserID, Type: "private"})
	callback(owner, &tgbotapi.Chat{ID: -55, Type: "group"})
	watch, err := b.db.Watch(ctx, id)
	if err != nil || watch.Status != domain.WatchStatusActive {
		t.Fatalf("unauthorized callback changed watch: status=%s, err=%v", watch.Status, err)
	}
	callback(owner, &tgbotapi.Chat{ID: owner.TelegramUserID, Type: "private"})
	watch, err = b.db.Watch(ctx, id)
	if err != nil || watch.Status != domain.WatchStatusDeleted {
		t.Fatalf("owner callback did not delete watch: status=%s, err=%v", watch.Status, err)
	}
}

type authClient struct {
	luxmed.Client
	err error
}

func (c authClient) Authenticate(context.Context, luxmed.Credentials) error { return c.err }

func TestCredentialsAreStoredOnlyAfterAuthentication(t *testing.T) {
	ctx := context.Background()
	b := testBot(t)
	user := createUser(t, b, 10, domain.UserStatusActive)
	b.setFlow(user.ID, &flow{Kind: flowAccountPass, Login: "example"})
	b.newClient = func(bool) luxmed.Client { return authClient{err: errors.New("login failed")} }
	b.handleText(ctx, user, "example password")
	if _, err := b.db.LuxMedAccount(ctx, user.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("invalid credentials saved: err=%v", err)
	}
	b.newClient = func(bool) luxmed.Client { return authClient{} }
	b.handleText(ctx, user, "example password")
	account, err := b.db.LuxMedAccount(ctx, user.ID)
	if err != nil || account.Status != domain.AccountStatusActive || account.EncryptedPassword == "example password" {
		t.Fatalf("authenticated credentials not safely saved: status=%s, err=%v", account.Status, err)
	}
}
