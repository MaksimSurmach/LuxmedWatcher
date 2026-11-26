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
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/maksimsurmach/luxmed-watcher/internal/domain"
	"github.com/maksimsurmach/luxmed-watcher/internal/i18n"
	"github.com/maksimsurmach/luxmed-watcher/internal/luxmed"
	"github.com/maksimsurmach/luxmed-watcher/internal/scheduler"
	"github.com/maksimsurmach/luxmed-watcher/internal/security"
	"github.com/maksimsurmach/luxmed-watcher/internal/storage"
)

func testBot(t *testing.T) *Bot {
	t.Helper()
	return testBotWithTelegram(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"ok":true,"result":{"message_id":99}}`)
	})
}

func testBotWithTelegram(t *testing.T, telegram http.HandlerFunc) *Bot {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/getMe") {
			fmt.Fprint(w, `{"ok":true,"result":{"id":99,"is_bot":true,"first_name":"Test Bot"}}`)
			return
		}
		telegram(w, r)
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
	err        error
	historyErr error
}

func (c authClient) Authenticate(context.Context, luxmed.Credentials) error { return c.err }

func (c authClient) GetRecentProcedures(context.Context) ([]domain.Procedure, error) {
	return nil, c.historyErr
}

func TestFirstPasswordLoginSavesCredentialsWhenHistoryIsUnavailable(t *testing.T) {
	ctx := t.Context()
	var deleted atomic.Bool
	b := testBotWithTelegram(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/deleteMessage") {
			deleted.Store(true)
		}
		fmt.Fprint(w, `{"ok":true,"result":{"message_id":99}}`)
	})
	user := createUser(t, b, 10, domain.UserStatusActive)
	b.newClient = func(bool) luxmed.Client { return authClient{historyErr: luxmed.ErrUnavailable} }
	b.setFlow(user.TelegramUserID, &flow{Kind: flowAccountPass, Login: "example"})
	const command = "/logout_luxmed"
	const password = command + " fixture"
	b.handleUpdate(ctx, tgbotapi.Update{Message: &tgbotapi.Message{
		MessageID: 1, From: &tgbotapi.User{ID: user.TelegramUserID},
		Chat: &tgbotapi.Chat{ID: user.TelegramChatID, Type: "private"},
		Text: password, Entities: []tgbotapi.MessageEntity{{Type: "bot_command", Length: len(command)}},
	}})
	b.workers.Wait()
	account, err := b.db.LuxMedAccount(ctx, user.ID)
	if err != nil || account.EncryptedPassword == "" || account.EncryptedPassword == password {
		t.Fatalf("first login did not save encrypted credentials: %v", err)
	}
	creds, err := b.cryptor.OpenCredentials(user.ID, account.EncryptedPassword)
	if err != nil || creds.Password != password || !deleted.Load() {
		t.Fatalf("command-shaped password was not scrubbed and saved: deleted=%v err=%v", deleted.Load(), err)
	}
	if required, err := b.db.LuxMedReconnectRequired(ctx, user.ID); err != nil || required {
		t.Fatalf("first login unexpectedly started an episode: required=%v err=%v", required, err)
	}
}

func TestPasswordLoginDuringCooldownSavesCredentialsAndQueuesOnlyActiveSearches(t *testing.T) {
	ctx := context.Background()
	b := testBot(t)
	user := createUser(t, b, 10, domain.UserStatusActive)
	const minimum = 5 * time.Minute
	runner := scheduler.NewRunner(b.db, b.cryptor, b, minimum, time.Hour, false)
	b.cfg.MinimumInterval = minimum
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	search := domain.Watch{UserID: user.ID, CityID: 1, ServiceID: 12, CheckIntervalSeconds: 3600}
	activeID, err := b.db.CreateWatch(ctx, search)
	must(err)
	pausedID, err := b.db.CreateWatch(ctx, search)
	must(err)
	must(b.db.MarkWatchChecked(ctx, activeID, false, "luxmed_authentication"))
	must(b.db.MarkWatchChecked(ctx, pausedID, false, "luxmed_authentication"))
	must(b.db.SetWatchStatus(ctx, pausedID, domain.WatchStatusPaused))
	must(b.db.BeginLuxMedReconnect(ctx, user.ID))
	claim, err := b.db.ClaimLuxMedReconnectDelivery(ctx, user.ID, time.Now())
	if err != nil || claim.Attempt != 1 {
		t.Fatalf("claim reconnect warning: attempt=%d, err=%v", claim.Attempt, err)
	}
	must(b.db.MarkLuxMedReconnectNotified(ctx, user.ID))
	started := time.Now().UTC()
	if reserved, err := b.db.ReserveLuxMedSearch(ctx, user.ID, started, minimum); err != nil || !reserved {
		t.Fatalf("reserve search before login: reserved=%v, err=%v", reserved, err)
	}

	b.setFlow(user.TelegramUserID, &flow{Kind: flowAccountPass, Login: "example"})
	b.newClient = func(bool) luxmed.Client { return authClient{err: errors.New("login failed")} }
	b.handleText(ctx, user, "example password")
	b.workers.Wait()
	if _, err := b.db.LuxMedAccount(ctx, user.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("invalid credentials saved: err=%v", err)
	}
	if required, err := b.db.LuxMedReconnectRequired(ctx, user.ID); err != nil || !required {
		t.Fatalf("failed login closed reconnect episode: required=%v, err=%v", required, err)
	}
	b.newClient = func(bool) luxmed.Client { return authClient{} }
	b.handleText(ctx, user, "example password")
	b.workers.Wait()
	account, err := b.db.LuxMedAccount(ctx, user.ID)
	if err != nil || account.Status != domain.AccountStatusActive || account.EncryptedPassword == "example password" {
		t.Fatalf("authenticated credentials not safely saved: status=%s, err=%v", account.Status, err)
	}

	if required, err := b.db.LuxMedReconnectRequired(ctx, user.ID); err != nil || !required {
		t.Fatalf("issued token closed the episode before verification: required=%v, err=%v", required, err)
	}
	if claim, err := b.db.ClaimLuxMedReconnectDelivery(ctx, user.ID, time.Now().Add(time.Hour)); err != nil || claim.Attempt != 0 {
		t.Fatalf("queued reconnect warning survived successful login: attempt=%d, err=%v", claim.Attempt, err)
	}
	due, err := b.db.DueWatches(ctx, 0)
	if err != nil || len(due) != 1 || due[0].ID != activeID || due[0].CityID != search.CityID ||
		due[0].ServiceID != search.ServiceID || due[0].CheckIntervalSeconds != search.CheckIntervalSeconds {
		t.Fatalf("active search did not resume with saved filters: due=%+v, err=%v", due, err)
	}
	if err := runner.CheckWatch(ctx, due[0]); !errors.Is(err, scheduler.ErrRateLimited) {
		t.Fatalf("successful login bypassed the minimum search interval: %v", err)
	}
	paused, err := b.db.Watch(ctx, pausedID)
	if err != nil || paused.Status != domain.WatchStatusPaused || paused.LastCheckedAt == nil {
		t.Fatalf("successful login changed a paused search: %+v, err=%v", paused, err)
	}
}

func TestFailedCredentialMessageDeletionStopsSignIn(t *testing.T) {
	var explained bool
	b := testBotWithTelegram(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/deleteMessage") {
			http.Error(w, `{"ok":false,"description":"denied"}`, http.StatusForbidden)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/sendMessage") {
			explained = strings.Contains(r.FormValue("text"), "Delete it in the chat")
		}
		fmt.Fprint(w, `{"ok":true,"result":{"message_id":99}}`)
	})
	user := createUser(t, b, 42, domain.UserStatusActive)
	b.setFlow(user.TelegramUserID, &flow{Kind: flowAccountPass, Login: "fixture"})
	b.newClient = func(bool) luxmed.Client { t.Fatal("unexpected authentication"); return nil }
	if err := b.db.Close(); err != nil {
		t.Fatal(err)
	}
	b.handleUpdate(t.Context(), tgbotapi.Update{Message: &tgbotapi.Message{MessageID: 1, Text: "test password", From: &tgbotapi.User{ID: 42}, Chat: &tgbotapi.Chat{ID: 42, Type: "private"}}})
	if !explained || b.getFlow(user.TelegramUserID) != nil {
		t.Fatal("failed deletion did not stop and explain sign-in")
	}
}

func TestCredentialMessagesAreDeletedWhenUserStoreIsUnavailable(t *testing.T) {
	var deletions atomic.Int32
	b := testBotWithTelegram(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/deleteMessage") && r.FormValue("chat_id") == "42" {
			deletions.Add(1)
		}
		fmt.Fprint(w, `{"ok":true,"result":true}`)
	})
	user := createUser(t, b, 42, domain.UserStatusActive)
	if err := b.db.Close(); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []flowKind{flowAccountLogin, flowAccountPass} {
		b.setFlow(user.TelegramUserID, &flow{Kind: kind})
		b.handleUpdate(t.Context(), tgbotapi.Update{Message: &tgbotapi.Message{
			MessageID: 1, Text: "fixture", From: &tgbotapi.User{ID: 42},
			Chat: &tgbotapi.Chat{ID: 42, Type: "private"},
		}})
	}
	if got := deletions.Load(); got != 2 {
		t.Fatalf("deleted %d sign-in messages, want 2", got)
	}
}

func TestWatchConfirmationAndResumeUseLocalAccountWithoutChrome(t *testing.T) {
	ctx := t.Context()
	b := testBot(t)
	user := createUser(t, b, 42, domain.UserStatusActive)
	b.SetRunner(scheduler.NewRunner(b.db, b.cryptor, nil, time.Minute, time.Hour, false))
	if err := b.db.UpsertLuxMedAccount(ctx, domain.LuxMedAccount{
		UserID: user.ID, EncryptedPassword: "unreadable portal fixture", Status: domain.AccountStatusActive,
	}); err != nil {
		t.Fatal(err)
	}
	b.setFlow(user.TelegramUserID, &flow{
		Kind: flowWatchConfirm, Confirmation: "confirm",
		Watch: domain.Watch{UserID: user.ID, CityID: 1, ServiceID: 2},
	})
	b.finishWatch(ctx, user, "confirm")
	watches, err := b.db.WatchesByUser(ctx, user.ID)
	if err != nil || len(watches) != 1 {
		t.Fatalf("confirmation failed with an existing local account: %v", err)
	}
	b.handleWatchAction(ctx, user, fmt.Sprintf("watch:pause:%d", watches[0].ID))
	b.handleWatchAction(ctx, user, fmt.Sprintf("watch:resume:%d", watches[0].ID))
	watch, err := b.db.Watch(ctx, watches[0].ID)
	if err != nil || watch.Status != domain.WatchStatusActive {
		t.Fatalf("search did not resume: status=%s err=%v", watch.Status, err)
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
	if b.getFlow(pending.TelegramUserID) != nil {
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
	callback("days:weekend")
	callback("back:watch_days") // Stale back button must not skip a step.
	if b.getFlow(pending.TelegramUserID).Kind != flowWatchInterval {
		t.Fatal("stale back button changed the current step")
	}
	callback("back:watch_interval")
	message("1,2,3,4,5,6,7")
	callback("interval:default")
	state := b.getFlow(pending.TelegramUserID)
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

func TestOnlyOwnerCanHideHistory(t *testing.T) {
	ctx := context.Background()
	b := testBot(t)
	owner := createUser(t, b, 10, domain.UserStatusActive)
	other := createUser(t, b, 20, domain.UserStatusActive)
	watchID, err := b.db.CreateWatch(ctx, domain.Watch{UserID: owner.ID, Name: "Example", CityID: 1, ServiceID: 12, CheckIntervalSeconds: 300})
	if err != nil {
		t.Fatal(err)
	}
	item, _, err := b.db.UpsertAppointmentHistory(ctx, watchID, domain.Appointment{Fingerprint: "example", DateTime: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	data := fmt.Sprintf("hide:%d:%d:0", item.ID, watchID)
	b.hideHistory(ctx, other, data)
	items, err := b.db.WatchHistory(ctx, owner.ID, watchID, 10, 0)
	if err != nil || len(items) != 1 {
		t.Fatalf("unauthorized hide changed history: count=%d, err=%v", len(items), err)
	}
	b.hideHistory(ctx, owner, data)
	items, err = b.db.WatchHistory(ctx, owner.ID, watchID, 10, 0)
	if err != nil || len(items) != 0 {
		t.Fatalf("owner hide did not change history: count=%d, err=%v", len(items), err)
	}
}

func TestHistoryExplainsUnavailableTimes(t *testing.T) {
	for _, tc := range []struct {
		name    string
		offset  time.Duration
		missing bool
		message string
	}{
		{
			name:    "appointment time passed",
			offset:  -24 * time.Hour,
			message: "This appointment time has passed.",
		},
		{
			name:    "future time missing from results",
			offset:  24 * time.Hour,
			missing: true,
			message: "This time no longer appears in search results. Check other times.",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			var sends atomic.Int32
			b := testBotWithTelegram(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/botfake/sendMessage" {
					t.Errorf("unexpected Telegram method: %s", r.URL.Path)
					return
				}
				sends.Add(1)
				if err := r.ParseForm(); err != nil {
					t.Error(err)
					return
				}
				if text := r.Form.Get("text"); !strings.Contains(text, tc.message) {
					t.Errorf("message %q does not contain %q", text, tc.message)
				}
				if markup := r.Form.Get("reply_markup"); strings.Contains(markup, luxmed.ReservationPageURL) {
					t.Errorf("unavailable time offered a portal action: %s", markup)
				}
				fmt.Fprint(w, `{"ok":true,"result":{"message_id":99}}`)
			})
			user := createUser(t, b, 101, domain.UserStatusActive)
			id, err := b.db.CreateWatch(ctx, domain.Watch{
				UserID: user.ID, Name: "Example", CityID: 1, ServiceID: 12, CheckIntervalSeconds: 300,
			})
			if err != nil {
				t.Fatal(err)
			}
			item, _, err := b.db.UpsertAppointmentHistory(ctx, id, domain.Appointment{
				Fingerprint: "test-slot",
				DateTime:    time.Now().Add(tc.offset),
				BookingURL:  luxmed.ReservationPageURL,
			})
			if err != nil {
				t.Fatal(err)
			}
			if tc.missing {
				nextCheck := item.LastSeenAt.Add(time.Minute)
				if err := b.db.MarkMissingAppointments(ctx, id, nextCheck, item.LastSeenAt); err != nil {
					t.Fatal(err)
				}
			}

			b.showHistory(ctx, user)
			if got := sends.Load(); got != 1 {
				t.Errorf("sent %d messages, want 1", got)
			}
		})
	}
}

func TestAppointmentNotificationEscapesHTMLAndShowsRemainingSlots(t *testing.T) {
	b := testBot(t)
	text, _ := b.appointmentNotification("en", domain.Watch{Name: "A < B & C"}, []domain.AppointmentHistory{
		{Appointment: domain.Appointment{DateTime: time.Unix(0, 0), DoctorName: "<b>Example</b>", FacilityName: "A & B", Address: "Street 12"}},
	}, 3, 10)
	for _, value := range []string{
		"<b>Appointment times found · 3</b>", "A &lt; B &amp; C", "&lt;b&gt;Example&lt;/b&gt;", "A &amp; B",
		"Street 12", "<i>More options: 2</i>", "Check the time and book in the LUX MED portal.",
	} {
		if !strings.Contains(text, value) {
			t.Errorf("notification missing %q in %q", value, text)
		}
	}
}

func TestWatchWithExpiredSessionOffersSignIn(t *testing.T) {
	ctx := t.Context()
	var sends atomic.Int32
	b := testBotWithTelegram(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/botfake/sendMessage" {
			t.Errorf("unexpected Telegram method: %s", r.URL.Path)
			return
		}
		sends.Add(1)
		if err := r.ParseForm(); err != nil {
			t.Error(err)
			return
		}
		text := r.Form.Get("text")
		if !strings.Contains(text, "Sign-in required.") || strings.Contains(text, "luxmed_authentication") {
			t.Errorf("unexpected message: %q", text)
		}
		markup := r.Form.Get("reply_markup")
		if !strings.Contains(markup, "Sign in to LUX MED") || !strings.Contains(markup, "account") {
			t.Errorf("missing sign-in action: %s", markup)
		}
		fmt.Fprint(w, `{"ok":true,"result":{"message_id":99}}`)
	})
	user := createUser(t, b, 101, domain.UserStatusActive)
	id, err := b.db.CreateWatch(ctx, domain.Watch{
		UserID: user.ID, Name: "Example", CityID: 1, ServiceID: 12, CheckIntervalSeconds: 300,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := b.db.MarkWatchChecked(ctx, id, false, "luxmed_authentication"); err != nil {
		t.Fatal(err)
	}
	b.showWatches(ctx, user)
	if got := sends.Load(); got != 1 {
		t.Errorf("sent %d messages, want 1", got)
	}
}

func TestBrowserBotAdmitsOnlyInvitedUsersWithAnAssignedWorker(t *testing.T) {
	ctx := t.Context()
	b := testBot(t)
	const ownerID, assignedID, unassignedID = 101, 202, 303
	b.cfg.AdminTelegramIDs = map[int64]bool{ownerID: true}
	b.cfg.Browsers = map[int64]*luxmed.Browser{ownerID: {}, assignedID: {}}
	owner := createUser(t, b, ownerID, domain.UserStatusActive)
	if err := b.db.CreateInvite(ctx, "invite-example", owner.ID, 1, nil); err != nil {
		t.Fatal(err)
	}
	start := func(id int64, code string) {
		b.handleUpdate(ctx, tgbotapi.Update{Message: &tgbotapi.Message{
			From:     &tgbotapi.User{ID: id},
			Chat:     &tgbotapi.Chat{ID: id, Type: "private"},
			Text:     strings.TrimSpace("/start " + code),
			Entities: []tgbotapi.MessageEntity{{Type: "bot_command", Length: len("/start")}},
		}})
	}
	start(assignedID, "")
	pending, err := b.db.UserByTelegramID(ctx, assignedID)
	if err != nil || pending.IsActive() {
		t.Fatalf("assignment did not start in pending state: %+v, %v", pending, err)
	}
	start(unassignedID, "invite-example")
	if _, err := b.db.UserByTelegramID(ctx, unassignedID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("unassigned user was admitted: %v", err)
	}
	start(assignedID, "invite-example")
	active, err := b.db.UserByTelegramID(ctx, assignedID)
	if err != nil || !active.IsActive() || active.IsAdmin() {
		t.Fatalf("invited user was not admitted as regular user: %+v, %v", active, err)
	}
}

func TestStandaloneConnectLinkUsesSavedLanguage(t *testing.T) {
	t.Parallel()

	ticket := strings.Repeat("a", 64)
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/viewer" {
			t.Errorf("unexpected browser worker request: %s %s", r.Method, r.URL.Path)
		}
		fmt.Fprintf(w, `{"ticket":%q}`, ticket)
	}))
	t.Cleanup(worker.Close)

	for _, tc := range []struct{ name, origin, field string }{
		{name: "local link", origin: "http://localhost:18769", field: "text"},
		{name: "inline button", origin: "https://browser.example", field: "reply_markup"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sent := make(chan string, 1)
			b := testBotWithTelegram(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/botfake/sendMessage" {
					t.Errorf("unexpected Telegram API request: %s", r.URL.Path)
				}
				if err := r.ParseForm(); err != nil {
					t.Error(err)
				}
				sent <- r.Form.Get(tc.field)
				fmt.Fprint(w, `{"ok":true,"result":{"message_id":99}}`)
			})
			user := createUser(t, b, 101, domain.UserStatusActive)
			if err := b.db.SetUserPreferences(t.Context(), user.ID, "be", domain.City{}); err != nil {
				t.Fatal(err)
			}
			browser, err := luxmed.NewBrowser(worker.URL, tc.origin)
			if err != nil {
				t.Fatal(err)
			}
			b.cfg.Browsers = map[int64]*luxmed.Browser{user.TelegramUserID: browser}
			action := credentialAction("chrome", user.CredentialRevisions())
			b.handleAction(t.Context(), user.TelegramUserID, action, b.selectChrome(user.TelegramUserID, action))
			select {
			case got := <-sent:
				want := tc.origin + "/connect?lang=be#" + ticket
				if !strings.Contains(got, want) {
					t.Errorf("connect link = %q, want URL %q", got, want)
				}
			default:
				t.Fatal("viewer link was not sent")
			}
		})
	}
}

func TestTelegramChromeOpeningKeepsMigratedCredentialsUntilExplicitRemoval(t *testing.T) {
	ctx := t.Context()
	var opened atomic.Int32
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/viewer" {
			t.Errorf("unexpected worker request: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		fmt.Fprintf(w, `{"ticket":%q}`, strings.Repeat("a", 64))
	}))
	t.Cleanup(worker.Close)
	b := testBotWithTelegram(t, func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		if strings.Contains(r.Form.Get("text"), "/connect?") {
			opened.Add(1)
		}
		fmt.Fprint(w, `{"ok":true,"result":{"message_id":99}}`)
	})
	user := createUser(t, b, 101, domain.UserStatusActive)
	browser, err := luxmed.NewBrowser(worker.URL, "http://localhost:18769")
	if err != nil {
		t.Fatal(err)
	}
	b.cfg.Browsers = map[int64]*luxmed.Browser{user.TelegramUserID: browser}
	sealed, err := b.cryptor.SealCredentials(user.ID, luxmed.Credentials{Login: "legacy", Password: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	if err := b.db.UpsertLuxMedAccount(ctx, domain.LuxMedAccount{
		UserID: user.ID, EncryptedPassword: sealed, Status: domain.AccountStatusActive,
	}); err != nil {
		t.Fatal(err)
	}
	choice := credentialAction("chrome", user.CredentialRevisions())
	b.handleAction(ctx, user.TelegramUserID, choice, b.selectChrome(user.TelegramUserID, choice))
	account, err := b.db.LuxMedAccount(ctx, user.ID)
	if err != nil || account.EncryptedPassword != sealed || opened.Load() != 1 {
		t.Fatalf("Chrome did not open with the legacy record intact: opened=%d error=%v", opened.Load(), err)
	}
	current, err := b.db.UserByID(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	forgetAction := credentialAction("forget", current.CredentialRevisions())
	b.handleAction(ctx, user.TelegramUserID, forgetAction, nil)
	if _, err := b.db.LuxMedAccount(ctx, user.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("explicit removal did not delete the record: %v", err)
	}
	current, err = b.db.UserByID(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.db.SaveCredentials(ctx, b.cryptor, current, luxmed.Credentials{Login: "opt-in", Password: "fixture"}); err != nil {
		t.Fatal(err)
	}
	b.handleAction(ctx, user.TelegramUserID, "connect:open", nil)
	b.handleAction(ctx, user.TelegramUserID, "connect:forget", nil)
	b.handleAction(ctx, user.TelegramUserID, forgetAction, nil)
	if _, err := b.db.LoginAccount(ctx, user.ID, true); err != nil || opened.Load() != 1 {
		t.Fatalf("old account actions switched away from saved login: opened=%d error=%v", opened.Load(), err)
	}
	revisions, _, _, err := b.db.BrowserLoginMode(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	switchAction := credentialAction("switch", revisions)
	b.handleAction(ctx, user.TelegramUserID, switchAction, b.selectChrome(user.TelegramUserID, switchAction))
	if _, err := b.db.LuxMedAccount(ctx, user.ID); !errors.Is(err, sql.ErrNoRows) || opened.Load() != 2 {
		t.Fatalf("fresh confirmation failed to select Chrome and delete credentials: opened=%d err=%v", opened.Load(), err)
	}
	revisions, _, _, err = b.db.BrowserLoginMode(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	choice = credentialAction("chrome", revisions)
	b.handleAction(ctx, user.TelegramUserID, choice, b.selectChrome(user.TelegramUserID, choice))
	if _, err := b.db.LuxMedAccount(ctx, user.ID); !errors.Is(err, sql.ErrNoRows) || opened.Load() != 3 {
		t.Fatalf("Chrome did not open after explicit opt-out: opened=%d error=%v", opened.Load(), err)
	}
}

func TestTelegramBrowserAccountCheckLeavesSearchVerificationPending(t *testing.T) {
	ctx := t.Context()
	b := testBot(t)
	user := createUser(t, b, 10, domain.UserStatusActive)
	if _, err := b.db.CreateWatch(ctx, domain.Watch{UserID: user.ID}); err != nil {
		t.Fatal(err)
	}
	if err := b.db.BeginLuxMedReconnect(ctx, user.ID); err != nil {
		t.Fatal(err)
	}

	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path != "/session" {
			t.Errorf("unexpected browser request: %s", req.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(worker.Close)
	browser, err := luxmed.NewBrowser(worker.URL, "http://localhost:18769")
	if err != nil {
		t.Fatal(err)
	}
	b.cfg.Browsers = map[int64]*luxmed.Browser{user.TelegramUserID: browser}
	runner := scheduler.NewRunner(b.db, b.cryptor, nil, time.Minute, time.Hour, false)
	runner.UseBrowsers(b.cfg.Browsers)
	b.SetRunner(runner)

	if err := b.checkWatchAccount(ctx, user); err != nil {
		t.Fatal(err)
	}
	_, access, err := b.db.WatchesWithReconnectState(ctx, user.ID)
	if err != nil || access.EpisodeID == "" || access.Status != storage.LuxMedAccessPending {
		t.Fatalf("session check did not leave protected verification pending: %+v, %v", access, err)
	}
}

type blockedLoginClient struct {
	luxmed.Client
	started chan<- struct{}
	release <-chan struct{}
}

func (c blockedLoginClient) Authenticate(context.Context, luxmed.Credentials) error {
	close(c.started)
	<-c.release
	return nil
}

func TestTelegramChromeCancelsInFlightPasswordVerification(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	started, release := make(chan struct{}), make(chan struct{})
	releaseLogin := sync.OnceFunc(func() { close(release) })
	var b *Bot
	b = testBotWithTelegram(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/answerCallbackQuery") {
			// Let authentication return while Telegram is acknowledging the Chrome click.
			releaseLogin()
			b.workers.Wait()
		}
		fmt.Fprint(w, `{"ok":true,"result":{"message_id":99}}`)
	})
	user := createUser(t, b, 42, domain.UserStatusActive)
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/viewer" {
			t.Errorf("unexpected browser request: %s", r.URL.Path)
		}
		fmt.Fprintf(w, `{"ticket":%q}`, strings.Repeat("a", 64))
	}))
	t.Cleanup(worker.Close)
	browser, err := luxmed.NewBrowser(worker.URL, "http://localhost:18769")
	if err != nil {
		t.Fatal(err)
	}
	b.cfg.Browsers = map[int64]*luxmed.Browser{user.TelegramUserID: browser}
	defer func() {
		releaseLogin()
		b.workers.Wait()
	}()
	b.newClient = func(bool) luxmed.Client { return blockedLoginClient{started: started, release: release} }
	b.setFlow(user.TelegramUserID, &flow{Kind: flowAccountPass, Login: "fixture"})
	from := &tgbotapi.User{ID: user.TelegramUserID}
	chat := &tgbotapi.Chat{ID: user.TelegramChatID, Type: "private"}
	done := make(chan error, 1)
	go func() {
		config := tgbotapi.NewUpdate(0)
		done <- b.processUpdates(ctx, &config, []telegramUpdate{{Update: tgbotapi.Update{
			UpdateID: 1,
			Message:  &tgbotapi.Message{MessageID: 20, From: from, Chat: chat, Text: "fixture password"},
		}}})
	}()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("password verification did not start")
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("password verification blocked the Telegram update loop")
	}

	config := tgbotapi.NewUpdate(2)
	if err := b.processUpdates(ctx, &config, []telegramUpdate{{Update: tgbotapi.Update{
		UpdateID: 2,
		CallbackQuery: &tgbotapi.CallbackQuery{
			ID: "chrome", From: from, Message: &tgbotapi.Message{MessageID: 21, Chat: chat}, Data: credentialAction("chrome", user.CredentialRevisions()),
		},
	}}}); err != nil {
		t.Fatal(err)
	}
	releaseLogin()
	b.workers.Wait()
	fresh, err := b.db.UserByID(ctx, user.ID)
	if err != nil || fresh.CredentialSaveRevision != user.CredentialSaveRevision+1 ||
		fresh.CredentialRevision != user.CredentialRevision || b.getFlow(user.TelegramUserID) != nil {
		t.Fatalf("Chrome did not cancel the verification: user=%+v, err=%v", fresh, err)
	}
	if _, err := b.db.LuxMedAccount(ctx, user.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("late verification persisted the password: %v", err)
	}
}

func TestTelegramLogoutCancelsPasswordVerificationAndPausesSearches(t *testing.T) {
	ctx := t.Context()
	var deleted atomic.Int32
	b := testBotWithTelegram(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/deleteMessage") {
			deleted.Add(1)
		}
		fmt.Fprint(w, `{"ok":true,"result":{"message_id":99}}`)
	})
	user := createUser(t, b, 42, domain.UserStatusActive)
	watchID, err := b.db.CreateWatch(ctx, domain.Watch{UserID: user.ID})
	if err != nil {
		t.Fatal(err)
	}
	started, release := make(chan struct{}), make(chan struct{})
	releaseLogin := sync.OnceFunc(func() { close(release) })
	defer func() {
		releaseLogin()
		b.workers.Wait()
	}()
	b.newClient = func(bool) luxmed.Client { return blockedLoginClient{started: started, release: release} }
	b.setFlow(user.TelegramUserID, &flow{Kind: flowAccountPass, Login: "fixture"})
	b.handleText(ctx, user, "fixture")
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("login did not start")
	}
	const command = "/logout_luxmed"
	send := func(id int, text string) {
		b.handleUpdate(ctx, tgbotapi.Update{Message: &tgbotapi.Message{
			MessageID: id,
			From:      &tgbotapi.User{ID: user.TelegramUserID},
			Chat:      &tgbotapi.Chat{ID: user.TelegramChatID, Type: "private"},
			Text:      text, Entities: []tgbotapi.MessageEntity{{Type: "bot_command", Length: len(command)}},
		}})
	}
	send(20, command+" fixture")
	beforeLogout, err := b.db.UserByID(ctx, user.ID)
	if err != nil || beforeLogout.CredentialRevisions() != user.CredentialRevisions() || deleted.Load() != 1 {
		t.Fatalf("command prefix affected the login state: revisions=%+v deleted=%d err=%v",
			beforeLogout.CredentialRevisions(), deleted.Load(), err)
	}
	send(21, command)
	releaseLogin()
	b.workers.Wait()
	if deleted.Load() != 1 {
		t.Fatal("logout was treated as a password message")
	}
	if _, err := b.db.LuxMedAccount(ctx, user.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("late verification stored credentials after logout: %v", err)
	}
	fresh, err := b.db.UserByID(ctx, user.ID)
	if err != nil || fresh.CredentialSaveRevision <= user.CredentialSaveRevision {
		t.Fatalf("logout did not advance the save revision: %v", err)
	}
	watch, err := b.db.Watch(ctx, watchID)
	if err != nil || watch.Status != domain.WatchStatusPaused {
		t.Fatalf("logout did not pause the watch: status=%s, err=%v", watch.Status, err)
	}
}

func TestTelegramChromeRollsBackMiniAppSaveDuringAcknowledgment(t *testing.T) {
	ctx := t.Context()
	var commit func() error
	b := testBotWithTelegram(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/answerCallbackQuery") {
			if err := commit(); err != nil {
				t.Error(err)
			}
		}
		fmt.Fprint(w, `{"ok":true,"result":{"message_id":99}}`)
	})
	user := createUser(t, b, 42, domain.UserStatusActive)
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, `{"ticket":%q}`, strings.Repeat("a", 64))
	}))
	t.Cleanup(worker.Close)
	browser, err := luxmed.NewBrowser(worker.URL, "http://localhost:18769")
	if err != nil {
		t.Fatal(err)
	}
	b.cfg.Browsers = map[int64]*luxmed.Browser{user.TelegramUserID: browser}
	attempt, err := b.db.StartCredentialAttempt(ctx, user, "mini-app-fixture")
	if err != nil {
		t.Fatal(err)
	}
	defer attempt.Finish()
	creds := luxmed.Credentials{Login: "fixture", Password: "synthetic"}
	if err := b.db.PrepareCredentialAttempt(attempt.Context(), b.cryptor, user, creds, attempt); err != nil {
		t.Fatal(err)
	}
	commit = func() error {
		if !errors.Is(attempt.Context().Err(), context.Canceled) {
			t.Error("Telegram acknowledged the click before canceling Mini App")
		}
		// Model a transaction that passed its cancellation check before the click.
		_, err := b.db.SaveCredentialAttempt(ctx, attempt)
		attempt.Finish()
		return err
	}
	b.handleCallback(ctx, &tgbotapi.CallbackQuery{
		ID: "chrome", Data: credentialAction("chrome", user.CredentialRevisions()), From: &tgbotapi.User{ID: user.TelegramUserID},
		Message: &tgbotapi.Message{MessageID: 1, Chat: &tgbotapi.Chat{ID: user.TelegramUserID, Type: "private"}},
	})
	if _, err := b.db.LuxMedAccount(ctx, user.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("Mini App password survived the Chrome choice: %v", err)
	}

	owner, err := b.db.UserByID(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	committed, err := b.db.StartCredentialAttempt(ctx, owner, "mini-app-completed")
	if err != nil {
		t.Fatal(err)
	}
	if err := b.db.PrepareCredentialAttempt(committed.Context(), b.cryptor, owner, creds, committed); err != nil {
		t.Fatal(err)
	}
	if _, err := b.db.SaveCredentialAttempt(committed.Context(), committed); err != nil {
		t.Fatal(err)
	}
	commit = func() error { return nil }
	for _, phase := range []string{"before Finish", "after Finish"} {
		b.handleCallback(ctx, &tgbotapi.CallbackQuery{
			ID: "chrome-finished", Data: credentialAction("chrome", owner.CredentialRevisions()),
			From:    &tgbotapi.User{ID: user.TelegramUserID},
			Message: &tgbotapi.Message{MessageID: 2, Chat: &tgbotapi.Chat{ID: user.TelegramUserID, Type: "private"}},
		})
		if _, err := b.db.LoginAccount(ctx, user.ID, true); err != nil {
			t.Fatalf("stale Telegram button deleted committed credentials %s: %v", phase, err)
		}
		committed.Finish()
	}
}

func TestTelegramRoutesCommandsDuringPasswordVerification(t *testing.T) {
	ctx := t.Context()
	var deleted atomic.Int32
	var menu, watches, help atomic.Bool
	var b *Bot
	b = testBotWithTelegram(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/deleteMessage") {
			deleted.Add(1)
		}
		switch r.FormValue("text") {
		case b.catalog.T("en", "errors.no_watches"):
			watches.Store(true)
		case b.catalog.T("en", "help.text"):
			help.Store(true)
		}
		if strings.Contains(r.FormValue("reply_markup"), "connect:chrome:") {
			menu.Store(true)
		}
		fmt.Fprint(w, `{"ok":true,"result":{"message_id":99}}`)
	})
	user := createUser(t, b, 42, domain.UserStatusActive)
	b.cfg.Browsers = map[int64]*luxmed.Browser{user.TelegramUserID: {}}
	started, release := make(chan struct{}), make(chan struct{})
	releaseLogin := sync.OnceFunc(func() { close(release) })
	defer func() {
		releaseLogin()
		b.workers.Wait()
	}()
	b.newClient = func(bool) luxmed.Client { return blockedLoginClient{started: started, release: release} }
	b.setFlow(user.TelegramUserID, &flow{Kind: flowAccountPass, Login: "fixture"})
	b.handleText(ctx, user, "fixture")
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("login did not start")
	}
	update := func(text string, id int) {
		b.handleUpdate(ctx, tgbotapi.Update{Message: &tgbotapi.Message{
			MessageID: id, From: &tgbotapi.User{ID: user.TelegramUserID},
			Chat: &tgbotapi.Chat{ID: user.TelegramChatID, Type: "private"},
			Text: text, Entities: []tgbotapi.MessageEntity{{Type: "bot_command", Length: len(strings.SplitN(text, " ", 2)[0])}},
		}})
	}
	update("/watches", 20)
	update("/help", 21)
	update("/watches fixture", 22)
	update("/synthetic_password", 23)
	if !watches.Load() || !help.Load() || deleted.Load() != 2 {
		t.Fatalf("verification did not route known commands and scrub input: watches=%v help=%v deleted=%d",
			watches.Load(), help.Load(), deleted.Load())
	}
	update("/connect", 24)
	releaseLogin()
	b.workers.Wait()
	if deleted.Load() != 2 || !menu.Load() {
		t.Fatalf("connect command did not show the mode menu: deleted=%d menu=%v", deleted.Load(), menu.Load())
	}
	if _, err := b.db.LuxMedAccount(ctx, user.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("late verification saved a password after /connect: %v", err)
	}
}

func TestChromeWidgetUsesBothRevisionsAndAppliesChoiceWhenAcknowledgmentFails(t *testing.T) {
	ctx := t.Context()
	var acknowledgments atomic.Int32
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, `{"ticket":%q}`, strings.Repeat("a", 64))
	}))
	t.Cleanup(worker.Close)
	b := testBotWithTelegram(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/answerWidgetQuery") {
			acknowledgments.Add(1)
			http.Error(w, `{"ok":false,"description":"expired"}`, http.StatusBadRequest)
			return
		}
		fmt.Fprint(w, `{"ok":true,"result":{"message_id":99}}`)
	})
	user := createUser(t, b, 42, domain.UserStatusActive)
	browser, err := luxmed.NewBrowser(worker.URL, "http://localhost:18769")
	if err != nil {
		t.Fatal(err)
	}
	b.cfg.Browsers = map[int64]*luxmed.Browser{user.TelegramUserID: browser}
	if err := b.db.ConfirmBrowserLogin(ctx, user); err != nil {
		t.Fatal(err)
	}
	current, err := b.db.UserByID(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	attempt, err := b.db.StartCredentialAttempt(ctx, current, "")
	if err != nil {
		t.Fatal(err)
	}
	defer attempt.Finish()
	if err := b.db.PrepareCredentialAttempt(attempt.Context(), b.cryptor, current,
		luxmed.Credentials{Login: "fixture", Password: "synthetic"}, attempt); err != nil {
		t.Fatal(err)
	}
	click := func(action string) {
		query := &widgetQuery{
			ID: "test-click", MessageID: 1, Data: action,
			From: &tgbotapi.User{ID: user.TelegramUserID},
			Chat: &tgbotapi.Chat{ID: user.TelegramChatID, Type: "private"},
		}
		query.Type.Type = "data"
		b.handleWidgetQuery(ctx, query)
	}
	click(credentialAction("chrome", user.CredentialRevisions()))
	if err := attempt.Context().Err(); err != nil {
		t.Fatalf("stale widget canceled a newer save: %v", err)
	}
	click(credentialAction("chrome", current.CredentialRevisions()))
	if !errors.Is(attempt.Context().Err(), context.Canceled) {
		t.Fatal("current widget did not cancel the attached save")
	}
	fresh, err := b.db.UserByID(ctx, user.ID)
	if err != nil || fresh.CredentialSaveRevision != current.CredentialSaveRevision+1 || acknowledgments.Load() != 2 {
		t.Fatalf("widget did not apply Chrome after the failed acknowledgment: revisions=%+v acknowledgments=%d err=%v",
			fresh.CredentialRevisions(), acknowledgments.Load(), err)
	}
}

func TestTelegramChromeLinkFailureShowsCurrentChoice(t *testing.T) {
	ctx := t.Context()
	var viewerCalls atomic.Int32
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if viewerCalls.Add(1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		fmt.Fprintf(w, `{"ticket":%q}`, strings.Repeat("a", 64))
	}))
	t.Cleanup(worker.Close)
	var sentMenu atomic.Value
	b := testBotWithTelegram(t, func(w http.ResponseWriter, r *http.Request) {
		if markup := r.FormValue("reply_markup"); strings.Contains(markup, "connect:chrome:") {
			sentMenu.Store(markup)
		}
		fmt.Fprint(w, `{"ok":true,"result":{"message_id":99}}`)
	})
	user := createUser(t, b, 42, domain.UserStatusActive)
	browser, err := luxmed.NewBrowser(worker.URL, "http://localhost:18769")
	if err != nil {
		t.Fatal(err)
	}
	b.cfg.Browsers = map[int64]*luxmed.Browser{user.TelegramUserID: browser}

	click := func(action string) {
		b.handleAction(ctx, user.TelegramUserID, action, b.selectChrome(user.TelegramUserID, action))
	}
	click(credentialAction("chrome", user.CredentialRevisions()))
	fresh, err := b.db.UserByID(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	retry := credentialAction("chrome", fresh.CredentialRevisions())
	if menu, _ := sentMenu.Load().(string); !strings.Contains(menu, retry) ||
		fresh.CredentialSaveRevision != user.CredentialSaveRevision+1 {
		t.Fatalf("failed viewer link did not show a fresh choice: menu=%q revisions=%+v", menu, fresh.CredentialRevisions())
	}
	click(retry)
	if viewerCalls.Load() != 2 {
		t.Fatalf("fresh click did not retry viewer link: calls=%d", viewerCalls.Load())
	}
}

func TestResumeWatchUsesSavedLoginWithChromeConfigured(t *testing.T) {
	ctx := t.Context()
	b := testBot(t)
	user := createUser(t, b, 42, domain.UserStatusActive)
	b.cfg.Browsers = map[int64]*luxmed.Browser{user.TelegramUserID: {}}
	runner := scheduler.NewRunner(b.db, b.cryptor, nil, time.Minute, time.Hour, false)
	runner.UseBrowsers(b.cfg.Browsers)
	b.SetRunner(runner)
	if err := b.db.UpsertLuxMedAccount(ctx, domain.LuxMedAccount{
		UserID: user.ID, UseSavedLogin: true, EncryptedPassword: "unreadable portal fixture",
	}); err != nil {
		t.Fatal(err)
	}
	id, err := b.db.CreateWatch(ctx, domain.Watch{UserID: user.ID, Status: domain.WatchStatusPaused})
	if err != nil {
		t.Fatal(err)
	}
	b.handleWatchAction(ctx, user, fmt.Sprintf("watch:resume:%d", id))
	watch, err := b.db.Watch(ctx, id)
	if err != nil || watch.Status != domain.WatchStatusActive {
		t.Fatalf("saved-login search did not resume locally: status=%s err=%v", watch.Status, err)
	}
	watch.CityID, watch.ServiceID, watch.NextDays = 1, 2, 7
	b.setFlow(user.TelegramUserID, &flow{Kind: flowWatchConfirm, Watch: watch, Confirmation: "confirm"})
	b.finishWatch(ctx, user, "confirm")
	updated, err := b.db.Watch(ctx, id)
	if err != nil || updated.NextDays != 7 {
		t.Fatalf("saved-login search confirmation required an upstream probe: days=%d err=%v", updated.NextDays, err)
	}
}

type cancelableLoginClient struct {
	luxmed.Client
	started chan<- context.Context
}

func (c cancelableLoginClient) Authenticate(ctx context.Context, _ luxmed.Credentials) error {
	c.started <- ctx
	<-ctx.Done()
	return ctx.Err()
}

func TestTelegramCancelButtonStopsLoginBeforeAcknowledgmentAndDatabase(t *testing.T) {
	var notified atomic.Value
	started := make(chan context.Context, 1)
	var attempt context.Context
	b := testBotWithTelegram(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/answerCallbackQuery") {
			if !errors.Is(attempt.Err(), context.Canceled) {
				t.Error("cancel must reach the attempt before the callback acknowledgment")
			}
		} else {
			notified.Store(r.FormValue("text"))
		}
		fmt.Fprint(w, `{"ok":true,"result":{"message_id":99}}`)
	})
	ctx, stop := context.WithCancel(t.Context())
	defer stop()
	user := createUser(t, b, 42, domain.UserStatusActive)
	b.newClient = func(bool) luxmed.Client { return cancelableLoginClient{started: started} }
	b.submitLogin(ctx, user, luxmed.Credentials{Login: "fixture", Password: "synthetic"})
	select {
	case attempt = <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("authentication did not start")
	}
	if err := b.db.Close(); err != nil {
		t.Fatal(err)
	}
	b.handleCallback(ctx, &tgbotapi.CallbackQuery{
		ID: "cancel", Data: "cancel", From: &tgbotapi.User{ID: user.TelegramUserID},
		Message: &tgbotapi.Message{MessageID: 1, Chat: &tgbotapi.Chat{ID: user.TelegramChatID, Type: "private"}},
	})
	b.workers.Wait()
	if !errors.Is(attempt.Err(), context.Canceled) {
		t.Fatalf("verification did not receive cancellation: %v", attempt.Err())
	}
	if notified.Load() != b.catalog.T(user.Locale, "flow.account.canceled") ||
		b.getFlow(user.TelegramUserID) != nil {
		t.Fatalf("cancellation did not report the outcome and clear its flow: %v", notified.Load())
	}
}

func TestTelegramLoginKeepsScrubbingUntilResultDelivery(t *testing.T) {
	for _, outcome := range []string{
		"saved", "cancel after commit", "authentication notice failure", "saved notice failure",
		"cancellation notice failure", "restart after failed notice", "new flow", "shutdown",
	} {
		t.Run(outcome, func(t *testing.T) {
			ctx, stop := context.WithCancel(t.Context())
			defer stop()
			sending := make(chan struct{})
			release := make(chan struct{})
			unblock := sync.OnceFunc(func() { close(release) })
			var deleted atomic.Int32
			var notified atomic.Value
			var cancelNoticeFailed atomic.Bool
			b := testBotWithTelegram(t, func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/deleteMessage") {
					deleted.Add(1)
				} else if strings.HasSuffix(r.URL.Path, "/sendMessage") {
					if notified.Load() == nil {
						notified.Store(r.FormValue("text"))
						close(sending)
						<-release
						if outcome == "authentication notice failure" || outcome == "saved notice failure" || outcome == "restart after failed notice" {
							w.WriteHeader(http.StatusServiceUnavailable)
							return
						}
					} else {
						notified.Store(r.FormValue("text"))
						if outcome == "cancellation notice failure" && cancelNoticeFailed.CompareAndSwap(false, true) {
							w.WriteHeader(http.StatusServiceUnavailable)
							return
						}
					}
				}
				fmt.Fprint(w, `{"ok":true,"result":{"message_id":99}}`)
			})
			defer func() {
				unblock()
				b.workers.Wait()
			}()
			user := createUser(t, b, 42, domain.UserStatusActive)
			if outcome == "shutdown" {
				started := make(chan context.Context, 1)
				b.newClient = func(bool) luxmed.Client { return cancelableLoginClient{started: started} }
				b.submitLogin(ctx, user, luxmed.Credentials{Login: "fixture", Password: "synthetic"})
				stop()
				b.workers.Wait()
				if b.getFlow(user.TelegramUserID) != nil || notified.Load() != nil {
					t.Fatal("shutdown left a flow or sent a result")
				}
				return
			}
			b.newClient = func(bool) luxmed.Client {
				if outcome == "authentication notice failure" {
					return authClient{err: luxmed.ErrAuthentication}
				}
				return authClient{}
			}
			b.submitLogin(ctx, user, luxmed.Credentials{Login: "fixture", Password: "synthetic"})
			select {
			case <-sending:
			case <-time.After(5 * time.Second):
				t.Fatal("result delivery did not start")
			}
			loginMessage(b, ctx, user, "repeat fixture", 1)
			if deleted.Load() != 1 {
				t.Fatal("repeated password was not removed while Telegram was delivering the result")
			}
			var wantFlow *flow
			switch outcome {
			case "cancel after commit", "cancellation notice failure":
				loginMessage(b, ctx, user, "/cancel", 2)
			case "new flow":
				wantFlow = &flow{Kind: flowWatchCity}
				b.setFlow(user.TelegramUserID, wantFlow)
			}
			unblock()
			b.workers.Wait()
			switch outcome {
			case "authentication notice failure", "saved notice failure", "cancellation notice failure":
				pending := b.getFlow(user.TelegramUserID)
				if pending == nil || pending.Kind != flowAccountCheck || pending.notice == nil {
					t.Fatal("failed delivery did not keep sensitive input protected")
				}
				loginMessage(b, ctx, user, "second fixture", 3)
				if deleted.Load() != 2 || b.getFlow(user.TelegramUserID) != pending.notice.retry {
					t.Fatal("repeated password was not scrubbed or notice retry did not restore the right flow")
				}
				if outcome != "authentication notice failure" {
					loginMessage(b, ctx, user, "ordinary message", 4)
					if deleted.Load() != 2 {
						t.Fatal("delivered notice kept intercepting ordinary messages")
					}
				}
			case "restart after failed notice":
				loginMessage(b, ctx, user, "/connect", 3)
				if flow := b.getFlow(user.TelegramUserID); flow == nil || flow.Kind != flowAccountLogin {
					t.Fatal("explicit restart did not replace the undelivered notice")
				}
				if deleted.Load() != 1 {
					t.Fatal("explicit restart was treated as credential text")
				}
			default:
				if b.getFlow(user.TelegramUserID) != wantFlow {
					t.Fatal("completion left its old flow or replaced a newer flow")
				}
			}
			if outcome == "cancel after commit" {
				if _, err := b.db.LoginAccount(ctx, user.ID, true); err != nil ||
					notified.Load() != b.catalog.T(user.Locale, "flow.account.already_saved") {
					t.Fatalf("cancel did not report the committed save: message=%v err=%v", notified.Load(), err)
				}
			}
		})
	}
}

func loginMessage(b *Bot, ctx context.Context, user domain.User, text string, id int) {
	msg := &tgbotapi.Message{
		MessageID: id, Text: text, From: &tgbotapi.User{ID: user.TelegramUserID},
		Chat: &tgbotapi.Chat{ID: user.TelegramChatID, Type: "private"},
	}
	if strings.HasPrefix(text, "/") {
		msg.Entities = []tgbotapi.MessageEntity{{Type: "bot_command", Length: len(text)}}
	}
	b.handleUpdate(ctx, tgbotapi.Update{Message: msg})
}

func TestChromeReconnectPreservesPendingWatchConfirmation(t *testing.T) {
	ctx := t.Context()
	var signedIn atomic.Bool
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/session":
			if !signedIn.Load() {
				w.WriteHeader(http.StatusUnauthorized)
			}
		case "/ticket/42":
			signedIn.Store(true)
			fmt.Fprintf(w, `{"ticket":%q}`, strings.Repeat("a", 64))
		default:
			t.Errorf("unexpected browser request: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(worker.Close)
	b := testBot(t)
	user := createUser(t, b, 42, domain.UserStatusActive)
	browser, err := luxmed.NewBrowser(worker.URL, "http://localhost:18769")
	if err != nil {
		t.Fatal(err)
	}
	b.cfg.Browsers = map[int64]*luxmed.Browser{user.TelegramUserID: browser}
	b.cfg.MiniApp, err = luxmed.NewMiniApp(worker.URL, "https://app.example", "")
	if err != nil {
		t.Fatal(err)
	}
	runner := scheduler.NewRunner(b.db, b.cryptor, nil, time.Minute, time.Hour, false)
	runner.UseBrowsers(b.cfg.Browsers)
	b.SetRunner(runner)
	watch := domain.Watch{UserID: user.ID, CityID: 5, ServiceID: 6, NextDays: 7}
	b.setFlow(user.TelegramUserID, &flow{Kind: flowWatchConfirm, Watch: watch, Confirmation: "draft"})
	b.handleAction(ctx, user.TelegramUserID, "confirm:draft", nil)
	if saved, err := b.db.WatchesByUser(ctx, user.ID); err != nil || len(saved) != 0 {
		t.Fatalf("watch saved before reconnect: count=%d err=%v", len(saved), err)
	}

	b.handleAction(ctx, user.TelegramUserID, "account", nil)
	loginMessage(b, ctx, user, "/connect", 1)
	b.handleAction(ctx, user.TelegramUserID, "connect:password", nil)
	if err := b.db.UpsertLuxMedAccount(ctx, domain.LuxMedAccount{
		UserID: user.ID, EncryptedPassword: "inactive migrated fixture",
	}); err != nil {
		t.Fatal(err)
	}
	b.handleAction(ctx, user.TelegramUserID, credentialAction("forget", user.CredentialRevisions()), nil)
	if _, err := b.db.LuxMedAccount(ctx, user.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("forget did not remove the migrated credentials: %v", err)
	}
	current, err := b.db.UserByID(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	action := credentialAction("chrome", current.CredentialRevisions())
	b.handleAction(ctx, user.TelegramUserID, action, b.selectChrome(user.TelegramUserID, action))
	b.handleAction(ctx, user.TelegramUserID, "confirm:draft", nil)
	saved, err := b.db.WatchesByUser(ctx, user.ID)
	if err != nil || len(saved) != 1 || saved[0].CityID != watch.CityID ||
		saved[0].ServiceID != watch.ServiceID || saved[0].NextDays != watch.NextDays {
		t.Fatalf("reconnect lost the watch draft or confirmation: count=%d err=%v", len(saved), err)
	}
}

func TestNewWatchUsesSavedLoginAndLocalCities(t *testing.T) {
	for _, preferred := range []bool{true, false} {
		name := "cached city picker"
		if preferred {
			name = "preferred city"
		}
		t.Run(name, func(t *testing.T) {
			ctx := t.Context()
			b := testBot(t)
			user := createUser(t, b, 42, domain.UserStatusActive)
			b.cfg.Browsers = map[int64]*luxmed.Browser{user.TelegramUserID: {}}
			runner := scheduler.NewRunner(b.db, b.cryptor, nil, time.Minute, time.Hour, false)
			runner.UseBrowsers(b.cfg.Browsers)
			b.SetRunner(runner)
			if err := b.db.UpsertLuxMedAccount(ctx, domain.LuxMedAccount{
				UserID: user.ID, UseSavedLogin: true, EncryptedPassword: "offline fixture",
			}); err != nil {
				t.Fatal(err)
			}
			city := domain.City{ID: 5, Name: "Fixture"}
			if err := b.db.UpsertCities(ctx, []domain.City{city}); err != nil {
				t.Fatal(err)
			}
			if preferred {
				if err := b.db.SetUserPreferences(ctx, user.ID, user.Locale, city); err != nil {
					t.Fatal(err)
				}
				loginMessage(b, ctx, user, "/new", 1)
			} else {
				b.handleAction(ctx, user.TelegramUserID, "new", nil)
			}
			flow := b.getFlow(user.TelegramUserID)
			if preferred && (flow == nil || flow.Kind != flowWatchService || flow.Watch.CityID != city.ID) ||
				!preferred && (flow == nil || flow.Kind != flowWatchCity) {
				t.Fatal("new watch did not start with saved login and local city data")
			}
		})
	}
}

func TestCredentialDeleteFailureKeepsInputProtectedUntilWarningArrives(t *testing.T) {
	ctx := t.Context()
	var deletes, warnings atomic.Int32
	var probe context.Context
	b := testBotWithTelegram(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/deleteMessage"):
			if deletes.Add(1) == 1 {
				w.WriteHeader(http.StatusForbidden)
				return
			}
		case strings.Contains(r.FormValue("text"), "Delete it in the chat"):
			if !errors.Is(probe.Err(), context.Canceled) {
				t.Error("verification was still running while sending the warning")
			}
			if warnings.Add(1) == 1 {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
		}
		fmt.Fprint(w, `{"ok":true,"result":{"message_id":99}}`)
	})
	user := createUser(t, b, 42, domain.UserStatusActive)
	started := make(chan context.Context, 1)
	b.newClient = func(bool) luxmed.Client { return cancelableLoginClient{started: started} }
	b.submitLogin(ctx, user, luxmed.Credentials{Login: "fixture", Password: "synthetic"})
	select {
	case probe = <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("verification did not start")
	}
	loginMessage(b, ctx, user, "first repeat", 1)
	b.workers.Wait()
	state := b.getFlow(user.TelegramUserID)
	if state == nil || state.Kind != flowAccountCheck || state.notice == nil ||
		state.notice.key != "flow.account.delete_failed" {
		t.Fatal("double failure cleared password protection")
	}
	loginMessage(b, ctx, user, "second repeat", 2)
	loginMessage(b, ctx, user, "ordinary text", 3)
	if deletes.Load() != 2 || warnings.Load() != 2 || b.getFlow(user.TelegramUserID) != nil {
		t.Fatalf("warning did not end password protection: deletions=%d warnings=%d", deletes.Load(), warnings.Load())
	}
	if _, err := b.db.LuxMedAccount(ctx, user.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("verification saved a password after deletion failed: %v", err)
	}
}

func TestConnectMenuDoesNotCancelMiniAppCredentialAttempt(t *testing.T) {
	ctx := t.Context()
	b := testBot(t)
	user := createUser(t, b, 42, domain.UserStatusActive)
	b.cfg.Browsers = map[int64]*luxmed.Browser{user.TelegramUserID: {}}
	attempt, err := b.db.StartCredentialAttempt(ctx, user, "mini-app")
	if err != nil {
		t.Fatal(err)
	}
	defer attempt.Finish()
	if err := b.db.PrepareCredentialAttempt(attempt.Context(), b.cryptor, user,
		luxmed.Credentials{Login: "fixture", Password: "synthetic"}, attempt); err != nil {
		t.Fatal(err)
	}
	loginMessage(b, ctx, user, "/connect", 1)
	b.handleAction(ctx, user.TelegramUserID, "account", nil)
	if err := attempt.Context().Err(); err != nil {
		t.Fatalf("showing the login menu canceled Mini App verification: %v", err)
	}
	if _, err := b.db.SaveCredentialAttempt(attempt.Context(), attempt); err != nil {
		t.Fatalf("showing the login menu prevented the Mini App save: %v", err)
	}
}
