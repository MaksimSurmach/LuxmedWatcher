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
	err       error
	searchErr error
}

func (c authClient) Authenticate(context.Context, luxmed.Credentials) error { return c.err }

func (c authClient) GetRecentProcedures(context.Context) ([]domain.Procedure, error) {
	return nil, c.searchErr
}

func (c authClient) SearchAppointments(context.Context, domain.Watch) (luxmed.SearchResult, error) {
	return luxmed.SearchResult{Checked: c.searchErr == nil}, c.searchErr
}

func TestPasswordLoginResumesOnlyActiveSearchesAfterAuthentication(t *testing.T) {
	ctx := context.Background()
	b := testBot(t)
	user := createUser(t, b, 10, domain.UserStatusActive)
	const minimum = 5 * time.Minute
	runner := scheduler.NewRunner(b.db, b.cryptor, b, minimum, time.Hour, false)
	b.SetRunner(runner)
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
	must(b.db.RecordLuxMedReconnectRejection(ctx, user.ID, claim, time.Now().Add(time.Minute), 0))
	started := time.Now().UTC()
	if reserved, err := b.db.ReserveLuxMedSearch(ctx, user.ID, started, minimum); err != nil || !reserved {
		t.Fatalf("reserve search before login: reserved=%v, err=%v", reserved, err)
	}

	b.setFlow(user.ID, &flow{Kind: flowAccountPass, Login: "example"})
	b.newClient = func(bool) luxmed.Client { return authClient{err: errors.New("login failed")} }
	b.handleText(ctx, user, "example password")
	if _, err := b.db.LuxMedAccount(ctx, user.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("invalid credentials saved: err=%v", err)
	}
	if required, err := b.db.LuxMedReconnectRequired(ctx, user.ID); err != nil || !required {
		t.Fatalf("failed login closed reconnect episode: required=%v, err=%v", required, err)
	}
	b.newClient = func(bool) luxmed.Client { return authClient{} }
	b.handleText(ctx, user, "example password")
	if _, err := b.db.LuxMedAccount(ctx, user.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("login during search cooldown saved unverified credentials: %v", err)
	}
	must(b.db.ReleaseLuxMedSearch(ctx, user.ID, started))
	b.handleText(ctx, user, "example password")
	account, err := b.db.LuxMedAccount(ctx, user.ID)
	if err != nil || account.Status != domain.AccountStatusActive || account.EncryptedPassword == "example password" {
		t.Fatalf("authenticated credentials not safely saved: status=%s, err=%v", account.Status, err)
	}

	if required, err := b.db.LuxMedReconnectRequired(ctx, user.ID); err != nil || required {
		t.Fatalf("successful login left reconnect episode open: required=%v, err=%v", required, err)
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

func TestPasswordLoginKeepsReconnectOpenWhenSearchRejectsToken(t *testing.T) {
	ctx := t.Context()
	b := testBot(t)
	user := createUser(t, b, 10, domain.UserStatusActive)
	if _, err := b.db.CreateWatch(ctx, domain.Watch{UserID: user.ID, CityID: 1, ServiceID: 12}); err != nil {
		t.Fatal(err)
	}
	if err := b.db.BeginLuxMedReconnect(ctx, user.ID); err != nil {
		t.Fatal(err)
	}
	if err := b.db.MarkLuxMedReconnectNotified(ctx, user.ID); err != nil {
		t.Fatal(err)
	}
	b.setFlow(user.ID, &flow{Kind: flowAccountPass, Login: "example"})
	b.newClient = func(bool) luxmed.Client { return authClient{searchErr: luxmed.ErrAuthentication} }
	b.handleText(ctx, user, "example password")
	if _, err := b.db.LuxMedAccount(ctx, user.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("password saved after protected search returned 401: %v", err)
	}
	if required, err := b.db.LuxMedReconnectRequired(ctx, user.ID); err != nil || !required {
		t.Fatalf("search 401 closed the episode: required=%v err=%v", required, err)
	}
	if claim, err := b.db.ClaimLuxMedReconnectDelivery(ctx, user.ID, time.Now()); err != nil || claim.Attempt != 0 {
		t.Fatalf("search 401 reopened delivery for a notified episode: %+v %v", claim, err)
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
	callback("days:weekend")
	callback("back:watch_days") // Stale back button must not skip a step.
	if b.getFlow(pending.ID).Kind != flowWatchInterval {
		t.Fatal("stale back button changed the current step")
	}
	callback("back:watch_interval")
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
		if !strings.Contains(markup, "Sign in to LUX MED") || !strings.Contains(markup, "connect:open") {
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
			b.handleAction(t.Context(), user.TelegramUserID, "connect:open")
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

func TestTelegramBrowserAccountCheckResolvesReconnect(t *testing.T) {
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

	if err := b.checkAccount(ctx, user); err != nil {
		t.Fatal(err)
	}
	if required, err := b.db.LuxMedReconnectRequired(ctx, user.ID); err != nil || required {
		t.Fatalf("confirmed browser session left reconnect episode open: required=%v, err=%v", required, err)
	}
}
