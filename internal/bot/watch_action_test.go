package bot

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"testing"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"github.com/maksimsurmach/luxmed-watcher/internal/domain"
	"github.com/maksimsurmach/luxmed-watcher/internal/i18n"
	"github.com/maksimsurmach/luxmed-watcher/internal/storage"
)

// A forged callback must not touch another user's watch. api is nil, so reaching
// any send would also panic.
func TestWatchActionRejectsOtherUsersWatch(t *testing.T) {
	ctx := context.Background()
	db, err := storage.Open(ctx, t.TempDir()+"/test.sqlite")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	owner, err := db.UpsertTelegramUser(ctx, domain.User{TelegramUserID: 1, TelegramChatID: 1, Locale: "en", Status: domain.UserStatusActive, Role: domain.UserRoleUser})
	if err != nil {
		t.Fatal(err)
	}
	attacker, err := db.UpsertTelegramUser(ctx, domain.User{TelegramUserID: 2, TelegramChatID: 2, Locale: "en", Status: domain.UserStatusActive, Role: domain.UserRoleUser})
	if err != nil {
		t.Fatal(err)
	}
	id, err := db.CreateWatch(ctx, domain.Watch{UserID: owner.ID, Name: "w", Status: domain.WatchStatusActive, CheckIntervalSeconds: 300})
	if err != nil {
		t.Fatal(err)
	}

	b := &Bot{db: db}
	for _, action := range []string{"pause", "delete", "deleteyes", "resume", "show"} {
		b.handleWatchAction(ctx, attacker, "watch:"+action+":"+strconv.FormatInt(id, 10))
	}

	watch, err := db.Watch(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if watch.Status != domain.WatchStatusActive {
		t.Fatalf("attacker changed status to %q", watch.Status)
	}
}

// A user who was never invited must not reach the account flow by forging a callback.
func TestCallbackIgnoredBeforeInvite(t *testing.T) {
	ctx := context.Background()
	db, err := storage.Open(ctx, t.TempDir()+"/test.sqlite")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	pending, err := db.UpsertTelegramUser(ctx, domain.User{TelegramUserID: 3, TelegramChatID: 3, Locale: "en", Status: domain.UserStatusPendingInvite, Role: domain.UserRoleUser})
	if err != nil {
		t.Fatal(err)
	}
	api := &tgbotapi.BotAPI{Client: http.DefaultClient}
	api.SetAPIEndpoint("http://127.0.0.1:1/bot%s/%s") // closed port: the callback ack fails fast, offline
	b := &Bot{db: db, api: api, flowState: map[int64]*flow{}}

	b.handleCallback(ctx, &tgbotapi.CallbackQuery{ID: "1", From: &tgbotapi.User{ID: 3}, Message: &tgbotapi.Message{}, Data: "account"})

	if state := b.getFlow(pending.ID); state != nil {
		t.Fatalf("pending user entered flow %q", state.Kind)
	}
}

// Delete asks first; only the confirmation button removes the watch.
func TestDeleteNeedsConfirmation(t *testing.T) {
	ctx := context.Background()
	db, err := storage.Open(ctx, t.TempDir()+"/test.sqlite")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	cat, err := i18n.Load("en")
	if err != nil {
		t.Fatal(err)
	}
	owner, err := db.UpsertTelegramUser(ctx, domain.User{TelegramUserID: 4, TelegramChatID: 4, Locale: "en", Status: domain.UserStatusActive, Role: domain.UserRoleUser})
	if err != nil {
		t.Fatal(err)
	}
	id, err := db.CreateWatch(ctx, domain.Watch{UserID: owner.ID, Name: "w", Status: domain.WatchStatusActive, CheckIntervalSeconds: 300})
	if err != nil {
		t.Fatal(err)
	}
	api := &tgbotapi.BotAPI{Client: http.DefaultClient}
	api.SetAPIEndpoint("http://127.0.0.1:1/bot%s/%s")
	b := &Bot{db: db, api: api, catalog: cat, logger: slog.New(slog.NewTextHandler(io.Discard, nil)), flowState: map[int64]*flow{}}
	status := func() domain.WatchStatus {
		w, err := db.Watch(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		return w.Status
	}

	b.handleWatchAction(ctx, owner, "watch:delete:"+strconv.FormatInt(id, 10))
	if got := status(); got != domain.WatchStatusActive {
		t.Fatalf("delete without confirmation changed status to %q", got)
	}
	b.handleWatchAction(ctx, owner, "watch:deleteyes:"+strconv.FormatInt(id, 10))
	if got := status(); got != domain.WatchStatusDeleted {
		t.Fatalf("confirmed delete left status %q", got)
	}
}
