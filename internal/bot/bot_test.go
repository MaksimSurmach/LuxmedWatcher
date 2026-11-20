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
	"slices"
	"testing"
	"time"

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

type watchCatalogClient struct{ authClient }

func (watchCatalogClient) GetCities(context.Context) ([]domain.City, error) {
	return []domain.City{{ID: 1, Name: "Warszawa"}}, nil
}

func (watchCatalogClient) GetServices(context.Context) ([]domain.Service, error) {
	return []domain.Service{{ID: 12, Name: "Test service"}}, nil
}

func (watchCatalogClient) GetDoctorsAndFacilities(context.Context, int, int) (domain.DoctorsAndFacilities, error) {
	return domain.DoctorsAndFacilities{
		Doctors:    []domain.Doctor{{ID: 21, FirstName: "Test", LastName: "Doctor"}},
		Facilities: []domain.Facility{{ID: 31, Name: "Test clinic"}},
	}, nil
}

func TestCreateWatchThroughTelegramCallbacks(t *testing.T) {
	ctx := context.Background()
	b := testBot(t)
	b.cfg.MinimumInterval = 2 * time.Minute
	b.cfg.DefaultInterval = 5 * time.Minute
	b.newClient = func(bool) luxmed.Client { return watchCatalogClient{} }
	pending := createUser(t, b, 10, domain.UserStatusPendingInvite)
	admin := createUser(t, b, 20, domain.UserStatusActive)
	if err := b.db.CreateInvite(ctx, "example", admin.ID, 1, nil); err != nil {
		t.Fatal(err)
	}
	from := &tgbotapi.User{ID: pending.TelegramUserID}
	chat := &tgbotapi.Chat{ID: pending.TelegramUserID, Type: "private"}
	callback := func(data string) {
		b.handleUpdate(ctx, tgbotapi.Update{CallbackQuery: &tgbotapi.CallbackQuery{
			ID: "test", Data: data, From: from, Message: &tgbotapi.Message{Chat: chat},
		}})
	}
	message := func(text string) {
		b.handleUpdate(ctx, tgbotapi.Update{Message: &tgbotapi.Message{MessageID: 1, Chat: chat, From: from, Text: text}})
	}

	callback("account") // Pending users may not connect an account.
	if b.getFlow(pending.ID) != nil {
		t.Fatal("pending user entered an authenticated flow")
	}
	message("example")
	callback("account")
	message("test-user")
	message("test-password")
	callback("new")
	callback("city:1")
	callback("proc:search")
	message("Test service")
	callback("proc:12")
	callback("doctor:exact")
	message("doctor")
	callback("doctor:21")
	callback("fac:all")
	callback("facsel:all:0:31")
	callback("facdone")
	callback("interval:default") // From an old step, this must have no effect.
	start := time.Now().In(luxmed.Location()).AddDate(0, 0, 1)
	end := start.AddDate(0, 0, 5)
	message(start.Format(time.DateOnly) + " " + end.Format(time.DateOnly))
	message("09:00-13:00")
	message("1,2,3,4,5,6,7")
	callback("interval:default")
	state := b.getFlow(pending.ID)
	if state == nil || state.Kind != flowWatchConfirm {
		t.Fatalf("did not reach review step: %+v", state)
	}
	token := state.Confirmation
	callback("confirm:stale")
	if watches, err := b.db.WatchesByUser(ctx, pending.ID); err != nil || len(watches) != 0 {
		t.Fatalf("stale confirmation changed watches: count=%d, err=%v", len(watches), err)
	}
	callback("confirm:" + token)
	callback("confirm:" + token) // Duplicate deliveries do not create another watch.

	watches, err := b.db.WatchesByUser(ctx, pending.ID)
	if err != nil || len(watches) != 1 {
		t.Fatalf("created watches: count=%d, err=%v", len(watches), err)
	}
	watch := watches[0]
	if watch.CityID != 1 || watch.ServiceID != 12 || watch.DoctorID == nil || *watch.DoctorID != 21 ||
		!slices.Equal(watch.FacilityIDs, []int{31}) || watch.CheckIntervalSeconds != 300 ||
		watch.DateFrom == nil || watch.DateTo == nil ||
		watch.DateFrom.In(luxmed.Location()).Format(time.DateOnly) != start.Format(time.DateOnly) ||
		watch.DateTo.In(luxmed.Location()).Format(time.DateOnly) != end.Format(time.DateOnly) ||
		len(watch.TimeWindows) != 7 || watch.TimeWindows[6] != (domain.TimeWindow{Weekday: 7, From: "09:00", To: "13:00"}) {
		t.Fatalf("saved filters differ from the selected filters: %+v", watch)
	}
}
