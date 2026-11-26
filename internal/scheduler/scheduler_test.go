package scheduler

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/maksimsurmach/luxmed-watcher/internal/domain"
	"github.com/maksimsurmach/luxmed-watcher/internal/luxmed"
	"github.com/maksimsurmach/luxmed-watcher/internal/security"
	"github.com/maksimsurmach/luxmed-watcher/internal/storage"
)

type fakeLuxMed struct {
	luxmed.Client
	search  func(context.Context) ([]domain.Appointment, error)
	logins  int
	authErr error
}

func (f *fakeLuxMed) Authenticate(context.Context, luxmed.Credentials) error {
	f.logins++
	return f.authErr
}

func (f *fakeLuxMed) SearchAppointments(ctx context.Context, _ domain.Watch) ([]domain.Appointment, error) {
	return f.search(ctx)
}

type notifyFunc func(context.Context, domain.User, domain.Watch, []domain.AppointmentHistory, int) (int, error)

func (n notifyFunc) NotifyAppointments(ctx context.Context, user domain.User, watch domain.Watch, items []domain.AppointmentHistory, count int) (int, error) {
	return n(ctx, user, watch, items, count)
}

func TestRunnerPersistsBeforeDeliveryAndDoesNotRepeatSuccess(t *testing.T) {
	ctx := context.Background()
	var delivered int
	var notifications int
	var db *storage.DB
	runner, database, watch := testRunner(t, notifyFunc(func(ctx context.Context, user domain.User, _ domain.Watch, items []domain.AppointmentHistory, total int) (int, error) {
		history, err := db.RecentHistory(ctx, user.ID, 50, 0)
		if err != nil || len(history) != 12 || len(items) != 10 || total != 12 {
			t.Fatalf("history=%d, preview=%d, total=%d, error=%v", len(history), len(items), total, err)
		}
		if got := []string{items[0].Fingerprint, items[1].Fingerprint, items[2].Fingerprint}; !slices.Equal(got, []string{"2", "1", "0"}) {
			t.Fatalf("order for slots at the same time = %v, want [2 1 0]", got)
		}
		notifications++
		if notifications == 1 {
			return 0, errors.New("temporary Telegram error")
		}
		delivered += total
		return 23, nil
	}))
	db = database
	var slots []domain.Appointment
	first := time.Now().Add(time.Hour)
	for i := range 12 {
		slots = append(slots, domain.Appointment{
			Fingerprint: fmt.Sprint(i), DateTime: first.Add(time.Duration(i) * time.Hour),
		})
	}
	slots[0].FacilityName, slots[0].DoctorName = "Clinic B", "Doctor A"
	slots[1].DateTime, slots[1].FacilityName, slots[1].DoctorName = first, "Clinic A", "Doctor B"
	slots[2].DateTime, slots[2].FacilityName, slots[2].DoctorName = first, "Clinic A", "Doctor A"
	client := &fakeLuxMed{search: func(context.Context) ([]domain.Appointment, error) {
		return append(append([]domain.Appointment(nil), slots...), slots[0]), nil
	}}
	runner.newClient = func(bool) luxmed.Client { return client }

	count, err := runner.ManualCheck(ctx, watch)
	if count != 12 || err == nil || delivered != 0 {
		t.Fatalf("first check returned count=%d error=%v delivered=%d", count, err, delivered)
	}
	if count, err := runner.ManualCheck(ctx, watch); err != nil || count != 12 {
		t.Fatalf("retry returned count=%d error=%v", count, err)
	}
	if err := runner.CheckWatch(ctx, watch); err != nil {
		t.Fatal(err)
	}
	if notifications != 2 || delivered != 12 || client.logins != 1 {
		t.Fatalf("notifications=%d delivered=%d logins=%d", notifications, delivered, client.logins)
	}
}

func TestRunnerSerializesChecksAndRejectsStaleWatch(t *testing.T) {
	ctx := context.Background()
	runner, db, watch := testRunner(t, nil)
	started := make(chan struct{})
	finish := make(chan struct{})
	runner.newClient = func(bool) luxmed.Client {
		return &fakeLuxMed{search: func(ctx context.Context) ([]domain.Appointment, error) {
			close(started)
			select {
			case <-finish:
				return nil, nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}}
	}

	done := make(chan error, 1)
	go func() { done <- runner.CheckWatch(ctx, watch) }()
	select {
	case <-started:
	case err := <-done:
		t.Fatalf("check didn't reach upstream: %v", err)
	case <-time.After(3 * time.Second):
		t.Fatal("check didn't start")
	}

	if _, err := runner.ManualCheck(ctx, watch); !errors.Is(err, ErrRateLimited) {
		t.Errorf("concurrent check: %v", err)
	}
	if err := db.SetWatchStatus(ctx, watch.ID, domain.WatchStatusPaused); err != nil {
		t.Error(err)
	}
	close(finish)
	if err := <-done; !errors.Is(err, ErrInactive) {
		t.Fatalf("in-flight check after pause: %v", err)
	}
	if err := runner.CheckWatch(ctx, watch); !errors.Is(err, ErrInactive) {
		t.Fatalf("stale active snapshot after pause: %v", err)
	}
}

func TestRejectedCredentialsAreNotRetried(t *testing.T) {
	ctx := context.Background()
	runner, db, watch := testRunner(t, nil)
	client := &fakeLuxMed{authErr: luxmed.ErrAuthentication}
	runner.newClient = func(bool) luxmed.Client { return client }

	owner, err := db.UserByID(ctx, watch.UserID)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.SaveCredentials(ctx, runner.cryptor, owner, luxmed.Credentials{Login: "saved", Password: "password"}); err != nil {
		t.Fatal(err)
	}
	runner.UseBrowsers(map[int64]*luxmed.Browser{42: {}})
	for range 2 {
		if _, err := runner.ManualCheck(ctx, watch); !errors.Is(err, luxmed.ErrAuthentication) {
			t.Fatalf("check error = %v", err)
		}
	}
	account, err := db.LuxMedAccount(ctx, watch.UserID)
	if err != nil || account.Status != domain.AccountStatusAuthFailed || client.logins != 1 {
		t.Fatalf("account status=%s, logins=%d, error=%v", account.Status, client.logins, err)
	}
}

func TestSundayFilterUsesWarsawTime(t *testing.T) {
	slot := domain.Appointment{DateTime: time.Date(2026, 6, 6, 22, 30, 0, 0, time.UTC)}
	window := []domain.TimeWindow{{Weekday: 7, From: "00:00", To: "01:00"}}
	if len(filterAppointmentsByTimeWindows([]domain.Appointment{slot}, window)) != 1 {
		t.Fatal("midnight Warsaw time was not recognized as Sunday")
	}
}

func TestSchedulerRenewsBrowserSessionBeforeHourlySearchIsDue(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	runner, db, watch := testRunner(t, nil)
	watch.CheckIntervalSeconds = 3600
	if err := db.UpdateWatch(ctx, watch); err != nil {
		t.Fatal(err)
	}
	if err := db.MarkWatchChecked(ctx, watch.ID, true, ""); err != nil {
		t.Fatal(err)
	}

	renewed := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/accounts/42/session" {
			t.Errorf("requested %s before the hourly search was due", r.URL.Path)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		renewed <- struct{}{}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	browser, err := luxmed.NewRoutedBrowser(42, luxmed.BrowserRoute{
		API: server.URL + "/accounts/42", Shared: true,
	}, "http://localhost:18769", strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	runner.UseBrowsers(map[int64]*luxmed.Browser{42: browser})
	scheduler := New(db, runner, time.Second, 0, slog.New(slog.NewTextHandler(io.Discard, nil)))
	var workers sync.WaitGroup
	workers.Go(func() { scheduler.Run(ctx) })
	defer func() {
		cancel()
		workers.Wait()
	}()

	select {
	case <-renewed:
	case <-ctx.Done():
		t.Fatal("hourly watch received no session check before its next search")
	}
}

func testRunner(t *testing.T, notifier Notifier) (*Runner, *storage.DB, domain.Watch) {
	t.Helper()
	ctx := context.Background()
	db, err := storage.Open(ctx, filepath.Join(t.TempDir(), "watcher.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	user, err := db.UpsertTelegramUser(ctx, domain.User{
		TelegramUserID: 42, TelegramChatID: 42, Status: domain.UserStatusActive, Role: domain.UserRoleUser,
	})
	if err != nil {
		t.Fatal(err)
	}
	cryptor, err := security.NewCryptor(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	// Exercise startup of an existing password-only installation.
	encrypted, err := cryptor.Encrypt("synthetic-password")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertLuxMedAccount(ctx, domain.LuxMedAccount{UserID: user.ID, Login: "test", EncryptedPassword: encrypted}); err != nil {
		t.Fatal(err)
	}
	if err := db.EncryptLegacyLogins(ctx, cryptor); err != nil {
		t.Fatal(err)
	}
	id, err := db.CreateWatch(ctx, domain.Watch{UserID: user.ID})
	if err != nil {
		t.Fatal(err)
	}
	watch, err := db.Watch(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	return NewRunner(db, cryptor, notifier, 0, time.Hour, false), db, watch
}

func TestBrowserChecksUseIndependentWorkers(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	runner, db, watchA := testRunner(t, nil)
	addWatch := func(telegramID int64, watchID int64) domain.Watch {
		t.Helper()
		user, err := db.UpsertTelegramUser(ctx, domain.User{
			TelegramUserID: telegramID, TelegramChatID: telegramID, Status: domain.UserStatusActive, Role: domain.UserRoleUser,
		})
		if err != nil {
			t.Fatal(err)
		}
		watch := domain.Watch{ID: watchID, UserID: user.ID, CityID: 1, ServiceID: int(telegramID), NextDays: 1}
		if watchID == 0 {
			watch.ID, err = db.CreateWatch(ctx, watch)
		} else {
			err = db.UpdateWatch(ctx, watch)
		}
		if err != nil {
			t.Fatal(err)
		}
		return watch
	}
	watchA = addWatch(42, watchA.ID)
	watchB := addWatch(84, 0)
	entered := make(chan int, 2)
	release := make(chan struct{})
	makeWorker := func(watch domain.Watch) *luxmed.Browser {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != luxmed.TermsURL || r.URL.Query().Get("serviceVariantId") != strconv.Itoa(watch.ServiceID) {
				t.Errorf("crossed worker requests: %s", r.URL.RequestURI())
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			entered <- watch.ServiceID
			select {
			case <-release:
				fmt.Fprint(w, `{"termsForService":{"termsForDays":[],"termsInfoForDays":[]}}`)
			case <-r.Context().Done():
			}
		}))
		t.Cleanup(server.Close)
		browser, err := luxmed.NewBrowser(server.URL, "http://localhost:18769")
		if err != nil {
			t.Fatal(err)
		}
		return browser
	}
	runner.UseBrowsers(map[int64]*luxmed.Browser{42: makeWorker(watchA), 84: makeWorker(watchB)})
	scheduler := New(db, runner, time.Second, 0, slog.New(slog.NewTextHandler(io.Discard, nil)))
	var checks sync.WaitGroup
	scheduler.tick(ctx, &checks, make(chan struct{}, cap(runner.capacity)))
	seen := make(map[int]bool)
	for range 2 {
		select {
		case service := <-entered:
			seen[service] = true
		case <-ctx.Done():
			t.Fatal("workers did not run concurrently")
		}
	}
	if !seen[42] || !seen[84] {
		t.Fatalf("workers reached: %v", seen)
	}
	if err := runner.CheckWatch(ctx, watchA); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("the same user ran two checks: %v", err)
	}
	close(release)
	checks.Wait()
	for _, id := range []int64{watchA.ID, watchB.ID} {
		watch, err := db.Watch(ctx, id)
		if err != nil || watch.LastSuccessAt == nil {
			t.Fatalf("scheduled watch %d did not finish: %+v, %v", id, watch, err)
		}
	}
}

func TestScheduledNotificationUsesLocaleChangedDuringSearch(t *testing.T) {
	t.Parallel()

	var deliveredFor string
	runner, db, watch := testRunner(t, notifyFunc(func(
		_ context.Context,
		user domain.User,
		_ domain.Watch,
		_ []domain.AppointmentHistory,
		_ int,
	) (int, error) {
		deliveredFor = user.Locale
		return 23, nil
	}))
	runner.newClient = func(bool) luxmed.Client {
		return &fakeLuxMed{search: func(ctx context.Context) ([]domain.Appointment, error) {
			if err := db.SetUserLocale(ctx, watch.UserID, "uk"); err != nil {
				return nil, err
			}
			return []domain.Appointment{{Fingerprint: "slot", DateTime: time.Now().Add(time.Hour)}}, nil
		}}
	}
	if err := runner.CheckWatch(t.Context(), watch); err != nil {
		t.Fatal(err)
	}
	if deliveredFor != "uk" {
		t.Errorf("notification used %q after switching to Ukrainian", deliveredFor)
	}
}
