package scheduler

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
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
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

func (f *fakeLuxMed) SearchAppointments(ctx context.Context, _ domain.Watch) (luxmed.SearchResult, error) {
	apps, err := f.search(ctx)
	return luxmed.SearchResult{Appointments: apps, Checked: err == nil}, err
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
	return testRunnerAt(t, notifier, filepath.Join(t.TempDir(), "watcher.sqlite"))
}

func testRunnerAt(t *testing.T, notifier Notifier, path string) (*Runner, *storage.DB, domain.Watch) {
	t.Helper()
	ctx := context.Background()
	db, err := storage.Open(ctx, path)
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
	encrypted, err := cryptor.Encrypt("synthetic-password")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.SaveVerifiedLuxMedAccount(ctx, domain.LuxMedAccount{UserID: user.ID, Login: "test", EncryptedPassword: encrypted}); err != nil {
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

type reconnectRecorder struct {
	Notifier
	attempts  int
	delivered int
}

func (n *reconnectRecorder) NotifyLuxMedReconnect(_ context.Context, _ domain.User) error {
	n.attempts++
	if n.attempts == 1 {
		return &ReconnectDeliveryRejected{RetryAfter: 2 * time.Minute}
	}
	n.delivered++
	return nil
}

func TestBrowserReconnectRetriesDeliveryAndResumesSavedSearches(t *testing.T) {
	ctx := t.Context()
	notifier := &reconnectRecorder{}
	runner, db, watch := testRunner(t, notifier)
	watch.CityID, watch.ServiceID, watch.CheckIntervalSeconds = 1, 2, 3600
	watch.NextDays = 1
	if err := db.UpdateWatch(ctx, watch); err != nil {
		t.Fatal(err)
	}
	other := watch
	other.ServiceID = 3
	other.ID = 0
	otherID, err := db.CreateWatch(ctx, other)
	if err != nil {
		t.Fatal(err)
	}
	pausedID, err := db.CreateWatch(ctx, other)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.SetWatchStatus(ctx, pausedID, domain.WatchStatusPaused); err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{otherID, pausedID} {
		if err := db.MarkWatchChecked(ctx, id, true, ""); err != nil {
			t.Fatal(err)
		}
	}
	other.ID = otherID

	var status, requests atomic.Int32
	var sessionReady atomic.Bool
	status.Store(http.StatusTooManyRequests)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.URL.Path == "/session" && sessionReady.Load() {
			return
		}
		code := int(status.Load())
		if code != http.StatusOK {
			w.WriteHeader(code)
			return
		}
		if r.URL.Path == "/session" {
			return
		}
		if r.URL.Path != luxmed.TermsURL || r.URL.Query().Get("searchPlace.id") != "1" ||
			!slices.Contains([]string{"2", "3"}, r.URL.Query().Get("serviceVariantId")) {
			t.Errorf("unexpected search filters: %s", r.URL.RequestURI())
		}
		fmt.Fprint(w, `{"termsForService":{"termsForDays":[],"termsInfoForDays":[]}}`)
	}))
	t.Cleanup(server.Close)
	browser, err := luxmed.NewBrowser(server.URL, "http://localhost:18769")
	if err != nil {
		t.Fatal(err)
	}
	browsers := map[int64]*luxmed.Browser{42: browser}
	runner.UseBrowsers(browsers)

	assertReconnect := func(want bool) {
		t.Helper()
		got, err := db.LuxMedReconnectRequired(ctx, watch.UserID)
		if err != nil || got != want {
			t.Fatalf("reconnect required = %v, want %v, error = %v", got, want, err)
		}
	}
	assertAccess := func(want storage.LuxMedAccessStatus) string {
		t.Helper()
		_, access, err := db.WatchesWithReconnectState(ctx, watch.UserID)
		if err != nil || access.Status != want {
			t.Fatalf("access snapshot = %+v, want %s, error = %v", access, want, err)
		}
		return access.EpisodeID
	}
	if err := runner.renewBrowserSession(ctx, 42, browser); !errors.Is(err, luxmed.ErrUnavailable) {
		t.Fatalf("renewal during throttling: %v", err)
	}
	assertReconnect(false)

	if err := db.MarkWatchChecked(ctx, watch.ID, false, "luxmed_authentication"); err != nil {
		t.Fatal(err)
	}
	status.Store(http.StatusUnauthorized)
	if err := runner.renewBrowserSession(ctx, 42, browser); err == nil {
		t.Fatal("renewal did not report rejected Telegram delivery")
	}
	assertReconnect(true)
	episodeID := assertAccess(storage.LuxMedAccessPaused)
	// Even if the legacy card changes, the episode opened by /session must retain the terms requirement.
	if err := db.MarkWatchChecked(ctx, watch.ID, false, ""); err != nil {
		t.Fatal(err)
	}
	if _, required, err := db.LuxMedReconnectVerification(ctx, watch.UserID); err != nil || !required {
		t.Fatalf("session rejection did not carry forward the protected access requirement: required=%v err=%v", required, err)
	}
	if notifier.attempts != 1 || notifier.delivered != 0 {
		t.Fatalf("rejected delivery had unexpected result: %+v", notifier)
	}

	restart := func() *Runner {
		started := NewRunner(db, nil, notifier, 0, time.Hour, false)
		started.UseBrowsers(browsers)
		return started
	}
	runner = restart()
	if _, err := runner.ManualCheck(ctx, watch); !errors.Is(err, luxmed.ErrAuthentication) {
		t.Fatalf("expired session: %v", err)
	}
	user, err := db.UserByID(ctx, watch.UserID)
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.deliverReconnect(ctx, user, time.Now().Add(90*time.Second)); err != nil || notifier.attempts != 1 {
		t.Fatalf("delivery before Telegram's retry time: attempts=%d, err=%v", notifier.attempts, err)
	}
	if err := runner.deliverReconnect(ctx, user, time.Now().Add(3*time.Minute)); err != nil {
		t.Fatalf("delivery after Telegram's retry time: %v", err)
	}
	runner = restart()

	expired := other
	past := time.Now().AddDate(0, 0, -2)
	expired.DateTo = &past
	if err := db.UpdateWatch(ctx, expired); err != nil {
		t.Fatal(err)
	}
	requestsBefore := requests.Load()
	if count, err := runner.ManualCheck(ctx, expired); count != 0 || err != nil || requests.Load() != requestsBefore {
		t.Fatalf("past date range should make no requests: count=%d, error=%v", count, err)
	}
	if saved, err := db.Watch(ctx, expired.ID); err != nil || saved.LastCheckedAt != nil {
		t.Fatalf("skipped search acquired a check timestamp: %+v, %v", saved, err)
	}
	assertReconnect(true)
	if err := db.UpdateWatch(ctx, other); err != nil {
		t.Fatal(err)
	}

	if _, err := runner.ManualCheck(ctx, other); !errors.Is(err, luxmed.ErrAuthentication) {
		t.Fatalf("second search with expired session: %v", err)
	}
	if notifier.attempts != 2 || notifier.delivered != 1 {
		t.Fatalf("warning should be accepted once across searches and restarts: %+v", notifier)
	}

	// Reopen the account runner while the form works but the protected terms request still fails.
	runner = restart()
	sessionReady.Store(true)
	requestsBefore = requests.Load()
	if required, err := runner.CheckBrowserSession(ctx, user); !required.VerificationPending || err != nil || requests.Load()-requestsBefore != 1 {
		t.Fatalf("manual probe did more than check the form: required=%v err=%v requests=%d", required, err, requests.Load()-requestsBefore)
	}
	runner = restart()
	if got := assertAccess(storage.LuxMedAccessPending); got != episodeID {
		t.Fatalf("manual verification changed episode: %q, want %q", got, episodeID)
	}
	status.Store(http.StatusServiceUnavailable)
	if err := runner.renewBrowserSession(ctx, 42, browser); !errors.Is(err, luxmed.ErrUnavailable) {
		t.Fatalf("transient terms failure: %v", err)
	}
	assertAccess(storage.LuxMedAccessPending)
	runner = restart()
	status.Store(http.StatusUnauthorized)
	if err := runner.renewBrowserSession(ctx, 42, browser); !errors.Is(err, luxmed.ErrAuthentication) {
		t.Fatalf("background check accepted the failed terms request: %v", err)
	}
	if got := assertAccess(storage.LuxMedAccessPaused); got != episodeID {
		t.Fatalf("background 401 changed episode: %q, want %q", got, episodeID)
	}
	assertReconnect(true)
	if notifier.attempts != 2 || notifier.delivered != 1 {
		t.Fatalf("ready form opened a duplicate episode: %+v", notifier)
	}
	status.Store(http.StatusOK)
	if err := runner.renewBrowserSession(ctx, 42, browser); err != nil {
		t.Fatalf("protected search did not confirm reconnect: %v", err)
	}
	assertReconnect(false)
	assertAccess(storage.LuxMedAccessConnected)
	due, err := db.DueWatches(ctx, 0)
	if err != nil || len(due) != 1 || due[0].ID != watch.ID || due[0].ServiceID != watch.ServiceID ||
		due[0].CityID != watch.CityID || due[0].CheckIntervalSeconds != watch.CheckIntervalSeconds {
		t.Fatalf("expected the other active search to resume with saved filters, got %+v, %v", due, err)
	}
	if err := runner.CheckWatch(ctx, due[0]); err != nil {
		t.Fatalf("resumed scheduled search: %v", err)
	}

	status.Store(http.StatusUnauthorized)
	sessionReady.Store(false)
	if err := runner.renewBrowserSession(ctx, 42, browser); err != nil {
		t.Fatalf("second expiry: %v", err)
	}
	assertReconnect(true)
	if got := assertAccess(storage.LuxMedAccessPaused); got == episodeID {
		t.Fatalf("new session loss reused episode %q", got)
	}
	if notifier.attempts != 3 || notifier.delivered != 2 {
		t.Fatalf("expected a warning for the new episode: %+v", notifier)
	}
}

type reconnectNotifierFunc struct {
	Notifier
	send func() error
}

func (n reconnectNotifierFunc) NotifyLuxMedReconnect(context.Context, domain.User) error {
	return n.send()
}

func TestReconnectDeliveryStopsAfterUncertainAcknowledgment(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "watcher.sqlite")
	sends := 0
	var db *storage.DB
	notifier := reconnectNotifierFunc{send: func() error {
		sends++
		// Telegram accepted the send, but the database is unavailable before acknowledgment.
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
		return nil
	}}
	runner, original, watch := testRunnerAt(t, notifier, path)
	db = original
	user, err := db.UserByID(ctx, watch.UserID)
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.requireReconnect(ctx, user); err == nil ||
		!strings.Contains(err.Error(), "record LuxMed reconnect warning delivery") {
		t.Fatalf("expected acknowledgment failure, got %v", err)
	}

	db, err = storage.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	restarted := NewRunner(db, nil, notifier, 0, time.Hour, false)
	if err := restarted.deliverReconnect(ctx, user, time.Now().Add(24*time.Hour)); err != nil || sends != 1 {
		t.Fatalf("uncertain attempt was retried: sends=%d, err=%v", sends, err)
	}
	if required, err := db.LuxMedReconnectRequired(ctx, user.ID); err != nil || !required {
		t.Fatalf("monitoring should still require sign-in: required=%t, err=%v", required, err)
	}
}

func TestReconnectDeliveryStopsAfterPermanentRejection(t *testing.T) {
	ctx := t.Context()
	sends := 0
	notifier := reconnectNotifierFunc{send: func() error {
		sends++
		return errors.New("telegram permanently rejected reconnect warning")
	}}
	runner, db, watch := testRunner(t, notifier)
	user, err := db.UserByID(ctx, watch.UserID)
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.requireReconnect(ctx, user); err == nil {
		t.Fatal("expected permanent delivery failure")
	}
	restarted := NewRunner(db, nil, notifier, 0, time.Hour, false)
	if err := restarted.deliverReconnect(ctx, user, time.Now().Add(24*time.Hour)); err != nil || sends != 1 {
		t.Fatalf("permanent rejection was retried: sends=%d, err=%v", sends, err)
	}
}

func TestSessionProbeResumesWatchesWithoutBypassingSearchCooldown(t *testing.T) {
	ctx := t.Context()
	const minimum = 5 * time.Minute
	_, db, watch := testRunner(t, nil)
	watch.CityID, watch.ServiceID = 1, 2
	watch.CheckIntervalSeconds = 3600
	if err := db.UpdateWatch(ctx, watch); err != nil {
		t.Fatal(err)
	}
	var searches, probes atomic.Int32
	var connected atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/session" {
			probes.Add(1)
		} else {
			searches.Add(1)
		}
		if !connected.Load() {
			w.WriteHeader(http.StatusUnauthorized)
		}
	}))
	t.Cleanup(server.Close)
	browser, err := luxmed.NewBrowser(server.URL, "http://localhost:18769")
	if err != nil {
		t.Fatal(err)
	}
	start := func() *Runner {
		runner := NewRunner(db, nil, nil, minimum, time.Hour, false)
		runner.UseBrowsers(map[int64]*luxmed.Browser{42: browser})
		return runner
	}

	if err := start().CheckWatch(ctx, watch); !errors.Is(err, luxmed.ErrAuthentication) {
		t.Fatalf("expired search: %v", err)
	}
	due, err := db.DueWatches(ctx, 0)
	if err != nil || len(due) != 0 {
		t.Fatalf("expired session accelerated a full search: %v, %v", due, err)
	}
	connected.Store(true)
	runner := start()
	scheduler := New(db, runner, time.Second, 0, slog.New(slog.NewTextHandler(io.Discard, nil)))
	scheduler.maintainReconnectSessions(ctx)
	due, err = db.DueWatches(ctx, 0)
	if err != nil || len(due) != 0 || probes.Load() != 1 {
		t.Fatalf("session probe prematurely restored the watch: probes=%d, due=%v, err=%v", probes.Load(), due, err)
	}
	if required, err := db.LuxMedReconnectRequired(ctx, watch.UserID); err != nil || !required {
		t.Fatalf("session probe closed the episode during cooldown: required=%v, err=%v", required, err)
	}
	if _, err := start().ManualCheck(ctx, watch); !errors.Is(err, ErrRateLimited) || searches.Load() != 1 {
		t.Fatalf("search bypassed the persisted cooldown: searches=%d, err=%v", searches.Load(), err)
	}
}

func TestBrowserBusyReleasesOnlyUnusedSearchReservation(t *testing.T) {
	for _, test := range []struct {
		name          string
		busyOnRequest int32
		rateLimited   bool
	}{
		{name: "busy before any search", busyOnRequest: 1},
		{name: "busy after a successful search window", busyOnRequest: 2},
		{name: "busy on retry after upstream 429", busyOnRequest: 2, rateLimited: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := t.Context()
			runner, db, watch := testRunner(t, nil)
			runner.minimumInterval = 5 * time.Minute
			watch.CityID, watch.ServiceID, watch.NextDays = 1, 2, 20
			if err := db.UpdateWatch(ctx, watch); err != nil {
				t.Fatal(err)
			}
			other := watch
			other.ID = 0
			otherID, err := db.CreateWatch(ctx, other)
			if err != nil {
				t.Fatal(err)
			}
			other.ID = otherID
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				request := requests.Add(1)
				if request == 1 && test.rateLimited {
					w.WriteHeader(http.StatusTooManyRequests)
					return
				}
				if request == test.busyOnRequest {
					w.WriteHeader(http.StatusConflict)
					return
				}
				fmt.Fprint(w, `{"termsForService":{"termsForDays":[],"termsInfoForDays":[]}}`)
			}))
			t.Cleanup(server.Close)
			browser, err := luxmed.NewBrowser(server.URL, "http://localhost:18769")
			if err != nil {
				t.Fatal(err)
			}
			runner.UseBrowsers(map[int64]*luxmed.Browser{42: browser})
			if err := runner.CheckWatch(ctx, watch); !errors.Is(err, luxmed.ErrBrowserBusy) {
				t.Fatalf("first check: %v", err)
			}

			restarted := NewRunner(db, nil, nil, runner.minimumInterval, time.Hour, false)
			restarted.UseBrowsers(runner.browsers)
			err = restarted.CheckWatch(ctx, other)
			if test.busyOnRequest == 1 && (err != nil || requests.Load() < 2) {
				t.Fatalf("unused reservation blocked retry: requests=%d err=%v", requests.Load(), err)
			}
			if test.busyOnRequest == 2 && (!errors.Is(err, ErrRateLimited) || requests.Load() != 2) {
				t.Fatalf("partial search lost its cooldown: requests=%d err=%v", requests.Load(), err)
			}
		})
	}
}

func TestReconnectDeliveryAfterTemporaryFailuresAndUnblocking(t *testing.T) {
	ctx := t.Context()
	attempts, delivered := 0, 0
	blocked := true
	notifier := reconnectNotifierFunc{send: func() error {
		attempts++
		if attempts <= 3 {
			return &ReconnectDeliveryRejected{RetryAfter: time.Minute}
		}
		if blocked {
			return &ReconnectDeliveryRejected{RetryAfter: 6 * time.Hour, ForbiddenLimit: 4}
		}
		delivered++
		return nil
	}}
	runner, db, watch := testRunner(t, notifier)
	user, err := db.UserByID(ctx, watch.UserID)
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.requireReconnect(ctx, user); err == nil {
		t.Fatal("expected first temporary rejection")
	}
	for attempt := 2; attempt <= 3; attempt++ {
		if err := runner.deliverReconnect(ctx, user, time.Now().Add(time.Hour)); err == nil {
			t.Fatalf("expected temporary rejection on attempt %d", attempt)
		}
	}
	if err := runner.deliverReconnect(ctx, user, time.Now().Add(time.Hour)); err == nil {
		t.Fatal("expected the first blocked rejection after temporary failures")
	}

	restarted := NewRunner(db, nil, notifier, 0, time.Hour, false)
	blocked = false
	now := time.Now()
	if err := restarted.deliverReconnect(ctx, user, now.Add(time.Hour)); err != nil || attempts != 4 {
		t.Fatalf("retry before the cooldown: attempts=%d err=%v", attempts, err)
	}
	if err := restarted.deliverReconnect(ctx, user, now.Add(7*time.Hour)); err != nil || delivered != 1 {
		t.Fatalf("retry after unblocking: delivered=%d err=%v", delivered, err)
	}
	if err := restarted.deliverReconnect(ctx, user, now.Add(48*time.Hour)); err != nil || attempts != 5 || delivered != 1 {
		t.Fatalf("duplicate after acceptance: attempts=%d delivered=%d err=%v", attempts, delivered, err)
	}
}

func TestReconnectDeliverySlowsAfterBlockedRetryLimit(t *testing.T) {
	ctx := t.Context()
	const limit = 4
	attempts := 0
	notifier := reconnectNotifierFunc{send: func() error {
		attempts++
		return &ReconnectDeliveryRejected{RetryAfter: 6 * time.Hour, ForbiddenLimit: limit}
	}}
	runner, db, watch := testRunner(t, notifier)
	user, err := db.UserByID(ctx, watch.UserID)
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.requireReconnect(ctx, user); err == nil {
		t.Fatal("expected rejection while blocked")
	}
	for attempt := 2; attempt <= limit; attempt++ {
		if err := runner.deliverReconnect(ctx, user, time.Now().Add(7*time.Hour)); err == nil {
			t.Fatalf("expected rejection on attempt %d", attempt)
		}
	}
	if err := runner.deliverReconnect(ctx, user, time.Now().Add(24*time.Hour)); err != nil || attempts != limit {
		t.Fatalf("weekly backoff not respected: attempts=%d err=%v", attempts, err)
	}
	if err := runner.deliverReconnect(ctx, user, time.Now().Add(8*24*time.Hour)); err == nil || attempts != limit+1 {
		t.Fatalf("weekly retry was lost without an unblock event: attempts=%d err=%v", attempts, err)
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

func TestPasswordReconnectSerializesWithQueuedWarning(t *testing.T) {
	ctx := t.Context()
	sending := make(chan struct{})
	unblock := make(chan struct{})
	releaseSend := sync.OnceFunc(func() { close(unblock) })
	t.Cleanup(releaseSend)
	attempts := 0
	notifier := reconnectNotifierFunc{send: func() error {
		attempts++
		if attempts == 1 {
			close(sending)
			<-unblock
		}
		return &ReconnectDeliveryRejected{}
	}}
	runner, db, watch := testRunner(t, notifier)
	runner.newClient = func(bool) luxmed.Client { return &fakeLuxMed{authErr: luxmed.ErrAuthentication} }
	account, err := db.LuxMedAccount(ctx, watch.UserID)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.BeginLuxMedReconnect(ctx, watch.UserID); err != nil {
		t.Fatal(err)
	}
	claim, err := db.ClaimLuxMedReconnectDelivery(ctx, watch.UserID, time.Now())
	if err != nil || claim.Attempt != 1 {
		t.Fatalf("prepare scheduled retry: attempt=%d, err=%v", claim.Attempt, err)
	}
	if err := db.RecordLuxMedReconnectRejection(ctx, watch.UserID, claim, time.Now().Add(-time.Minute), 0); err != nil {
		t.Fatal(err)
	}

	retryDone := make(chan error, 1)
	go func() { retryDone <- runner.renewBrowserSession(ctx, 42, nil) }()
	select {
	case <-sending:
	case err := <-retryDone:
		t.Fatalf("retry missed the notifier: %v", err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}

	account.Login = "verified login"
	waitCtx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	if err := runner.SaveVerifiedLuxMedAccount(waitCtx, account); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("password sign-in bypassed the in-flight retry: %v", err)
	}
	releaseSend()
	if err := <-retryDone; err == nil {
		t.Fatal("expected temporary rejection from retry")
	}

	if err := runner.SaveVerifiedLuxMedAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	user, err := db.UserByID(ctx, watch.UserID)
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.deliverReconnect(ctx, user, time.Now().Add(time.Hour)); err != nil || attempts != 1 {
		t.Fatalf("retry after verified login: attempts=%d, err=%v", attempts, err)
	}
}

type fixedSearchResultClient struct {
	fakeLuxMed
	result luxmed.SearchResult
	err    error
}

func (c *fixedSearchResultClient) SearchAppointments(context.Context, domain.Watch) (luxmed.SearchResult, error) {
	return c.result, c.err
}

func TestAuthenticationFailureDoesNotRecordOnChangedWatch(t *testing.T) {
	for _, scenario := range []struct {
		name                string
		transitions         []domain.WatchStatus
		anotherActive       bool
		pauseDuringDelivery bool
		wantWarning         bool
	}{
		{name: "still active", wantWarning: true},
		{name: "paused", transitions: []domain.WatchStatus{domain.WatchStatusPaused}},
		{name: "deleted", transitions: []domain.WatchStatus{domain.WatchStatusDeleted}},
		{name: "reactivated", transitions: []domain.WatchStatus{domain.WatchStatusPaused, domain.WatchStatusActive}, wantWarning: true},
		{name: "another active", transitions: []domain.WatchStatus{domain.WatchStatusPaused}, anotherActive: true, wantWarning: true},
		{name: "paused after 401 before result write", pauseDuringDelivery: true, wantWarning: true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			sends := 0
			runner, db, watch := testRunner(t, nil)
			var pauseErr error
			runner.notifier = reconnectNotifierFunc{send: func() error {
				sends++
				if scenario.pauseDuringDelivery {
					pauseErr = db.SetUserWatchStatus(ctx, watch.UserID, watch.ID, domain.WatchStatusPaused)
				}
				return nil
			}}
			if scenario.anotherActive {
				if _, err := db.CreateWatch(ctx, domain.Watch{UserID: watch.UserID}); err != nil {
					t.Fatal(err)
				}
			}
			searching, respond := make(chan struct{}), make(chan struct{})
			release := sync.OnceFunc(func() { close(respond) })
			defer release()
			runner.newClient = func(bool) luxmed.Client {
				return &fakeLuxMed{search: func(ctx context.Context) ([]domain.Appointment, error) {
					close(searching)
					select {
					case <-respond:
						return nil, luxmed.ErrAuthentication
					case <-ctx.Done():
						return nil, ctx.Err()
					}
				}}
			}
			done := make(chan error, 1)
			go func() { done <- runner.CheckWatch(ctx, watch) }()
			select {
			case <-searching:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			wantStatus := domain.WatchStatusActive
			for _, status := range scenario.transitions {
				if err := db.SetUserWatchStatus(ctx, watch.UserID, watch.ID, status); err != nil {
					t.Fatal(err)
				}
				wantStatus = status
			}
			release()
			if err := <-done; !errors.Is(err, luxmed.ErrAuthentication) {
				t.Fatalf("check error = %v", err)
			}
			if pauseErr != nil {
				t.Fatal(pauseErr)
			}
			if scenario.pauseDuringDelivery {
				wantStatus = domain.WatchStatusPaused
			}
			saved, err := db.Watch(ctx, watch.ID)
			wantError := ""
			if len(scenario.transitions) == 0 && !scenario.pauseDuringDelivery {
				wantError = "luxmed_authentication"
			}
			if err != nil || saved.Status != wantStatus || saved.LastError != wantError ||
				(saved.LastCheckedAt != nil) != (wantError != "") {
				t.Fatalf("recorded stale result: %+v, %v", saved, err)
			}
			required, err := db.LuxMedReconnectRequired(ctx, watch.UserID)
			if err != nil || required != scenario.wantWarning || (sends == 1) != scenario.wantWarning || sends > 1 {
				t.Fatalf("reconnect required = %v, sends = %d, err = %v", required, sends, err)
			}
		})
	}
}

func TestConfirmedPartialSearchResolvesReconnect(t *testing.T) {
	for _, test := range []struct {
		name      string
		checked   bool
		slots     int
		searchErr error
		wantSlots int
		wantError string
		reconnect bool
	}{
		{
			name: "confirmed partial search without slots", checked: true,
			searchErr: luxmed.ErrPartialResults, wantError: "luxmed_partial",
		},
		{
			name: "confirmed partial search with slots", checked: true, slots: 2, wantSlots: 2,
			searchErr: luxmed.ErrPartialResults, wantError: "luxmed_partial",
		},
		{
			name: "later request rejected authentication", checked: true, slots: 2,
			searchErr: errors.Join(luxmed.ErrPartialResults, luxmed.ErrAuthentication), reconnect: true,
			wantError: "luxmed_authentication",
		},
		{
			name: "partial result without confirmation", searchErr: luxmed.ErrPartialResults, reconnect: true,
			wantError: "luxmed_partial",
		},
		{
			name: "locally skipped search", reconnect: true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := t.Context()
			delivered := 0
			runner, db, watch := testRunner(t, notifyFunc(func(
				_ context.Context, _ domain.User, _ domain.Watch, _ []domain.AppointmentHistory, count int,
			) (int, error) {
				delivered += count
				return 23, nil
			}))
			appointments := make([]domain.Appointment, 0, test.slots)
			for i := range test.slots {
				appointments = append(appointments, domain.Appointment{
					Fingerprint: strconv.Itoa(i), DateTime: time.Now().Add(time.Duration(i+1) * time.Hour),
				})
			}
			account, err := db.LuxMedAccount(ctx, watch.UserID)
			if err != nil {
				t.Fatal(err)
			}
			runner.accounts[watch.UserID] = &accountCheck{
				login: account.Login, encryptedPassword: account.EncryptedPassword, authenticatedAt: time.Now(),
				client: &fixedSearchResultClient{
					result: luxmed.SearchResult{Checked: test.checked, Appointments: appointments},
					err:    test.searchErr,
				},
			}
			if err := db.BeginLuxMedReconnect(ctx, watch.UserID); err != nil {
				t.Fatal(err)
			}
			now := time.Now()
			claim, err := db.ClaimLuxMedReconnectDelivery(ctx, watch.UserID, now)
			if err != nil || claim.Attempt != 1 {
				t.Fatalf("seed warning delivery: attempt=%d err=%v", claim.Attempt, err)
			}
			if err := db.RecordLuxMedReconnectRejection(ctx, watch.UserID, claim, now.Add(time.Minute), 0); err != nil {
				t.Fatal(err)
			}

			count, err := runner.ManualCheck(ctx, watch)
			if !errors.Is(err, test.searchErr) || count != test.wantSlots || delivered != test.wantSlots {
				t.Fatalf("search: slots=%d delivered=%d err=%v", count, delivered, err)
			}
			saved, err := db.Watch(ctx, watch.ID)
			if err != nil || saved.LastError != test.wantError {
				t.Fatalf("watch error = %q, want %q, err=%v", saved.LastError, test.wantError, err)
			}
			required, err := db.LuxMedReconnectRequired(ctx, watch.UserID)
			if err != nil || required != test.reconnect {
				t.Fatalf("reconnect required = %t, want %t, err=%v", required, test.reconnect, err)
			}
			claim, err = db.ClaimLuxMedReconnectDelivery(ctx, watch.UserID, now.Add(time.Hour))
			if err != nil || (claim.Attempt != 0) != test.reconnect {
				t.Fatalf("warning retry: attempt=%d reconnect=%t err=%v", claim.Attempt, test.reconnect, err)
			}
		})
	}
}

type timedOutSearchClient struct{ fakeLuxMed }

func (c *timedOutSearchClient) SearchAppointments(ctx context.Context, _ domain.Watch) (luxmed.SearchResult, error) {
	// A successful preview is followed by a detail request that exceeds the search deadline.
	<-ctx.Done()
	return luxmed.SearchResult{Checked: true}, errors.Join(luxmed.ErrPartialResults, ctx.Err())
}

func TestConfirmedSessionResolvesReconnectAfterSearchDeadline(t *testing.T) {
	ctx := t.Context()
	runner, db, watch := testRunner(t, nil)
	runner.newClient = func(bool) luxmed.Client { return &timedOutSearchClient{} }
	if err := db.BeginLuxMedReconnect(ctx, watch.UserID); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	claim, err := db.ClaimLuxMedReconnectDelivery(ctx, watch.UserID, now)
	if err != nil || claim.Attempt != 1 {
		t.Fatalf("seed warning delivery: attempt=%d err=%v", claim.Attempt, err)
	}
	if err := db.RecordLuxMedReconnectRejection(ctx, watch.UserID, claim, now.Add(time.Minute), 0); err != nil {
		t.Fatal(err)
	}

	searchCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	count, err := runner.ManualCheck(searchCtx, watch)
	if count != 0 || !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, luxmed.ErrPartialResults) {
		t.Fatalf("search: count=%d err=%v", count, err)
	}
	required, err := db.LuxMedReconnectRequired(ctx, watch.UserID)
	if err != nil || required {
		t.Fatalf("reconnect required after confirmed session = %t, err=%v", required, err)
	}
	if claim, err = db.ClaimLuxMedReconnectDelivery(ctx, watch.UserID, now.Add(time.Hour)); err != nil || claim.Attempt != 0 {
		t.Fatalf("warning survived search deadline: attempt=%d err=%v", claim.Attempt, err)
	}
}

func TestReconnectRejectionSurvivesRestartAfterDatabaseWriteFailure(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "watcher.sqlite")
	sends := 0
	notifier := reconnectNotifierFunc{send: func() error {
		sends++
		if sends == 1 {
			return &ReconnectDeliveryRejected{ForbiddenLimit: 4, RetryAfter: 6 * time.Hour}
		}
		return nil
	}}
	runner, db, watch := testRunnerAt(t, notifier, path)
	fixture, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { fixture.Close() })
	if _, err := fixture.ExecContext(ctx, `
		CREATE TRIGGER reject_delivery_write BEFORE UPDATE OF retry_at ON luxmed_reconnect_delivery
		WHEN NEW.retry_at IS NOT NULL BEGIN SELECT RAISE(FAIL, 'simulated database write failure'); END
	`); err != nil {
		t.Fatal(err)
	}
	user, err := db.UserByID(ctx, watch.UserID)
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.requireReconnect(ctx, user); err == nil || !strings.Contains(err.Error(), "record LuxMed reconnect rejection") {
		t.Fatalf("expected the initial rejection write to fail: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := storage.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { reopened.Close() })
	runner = NewRunner(reopened, nil, notifier, 0, time.Hour, false)

	now := time.Now()
	if err := runner.deliverReconnect(ctx, user, now.Add(time.Hour)); err == nil || sends != 1 {
		t.Fatalf("database still unavailable: sends=%d err=%v", sends, err)
	}
	if _, err := fixture.ExecContext(ctx, `DROP TRIGGER reject_delivery_write`); err != nil {
		t.Fatal(err)
	}
	if err := runner.deliverReconnect(ctx, user, now.Add(time.Hour)); err != nil || sends != 1 {
		t.Fatalf("persist rejection without bypassing cooldown: sends=%d err=%v", sends, err)
	}
	if err := runner.deliverReconnect(ctx, user, now.Add(7*time.Hour)); err != nil || sends != 2 {
		t.Fatalf("send after cooldown: sends=%d err=%v", sends, err)
	}
	if err := runner.deliverReconnect(ctx, user, now.Add(8*time.Hour)); err != nil || sends != 2 {
		t.Fatalf("delivered warning was sent twice: sends=%d err=%v", sends, err)
	}
}

func TestReconnectClaimErrorAfterWriteRecordsKnownUnsentDelivery(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "watcher.sqlite")
	sends := 0
	runner, db, watch := testRunnerAt(t, reconnectNotifierFunc{send: func() error {
		sends++
		return nil
	}}, path)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	user, err := db.UserByID(ctx, watch.UserID)
	must(err)
	must(db.BeginLuxMedReconnect(ctx, user.ID))
	faults, err := sql.Open("sqlite", path)
	must(err)
	t.Cleanup(func() { faults.Close() })
	// FAIL reports a statement error while retaining the insert performed before it.
	_, err = faults.ExecContext(ctx, `
		CREATE TRIGGER lose_claim_reply AFTER INSERT ON luxmed_reconnect_delivery
		BEGIN SELECT RAISE(FAIL, 'simulated lost reservation response'); END
	`)
	must(err)
	now := time.Now()
	err = runner.deliverReconnect(ctx, user, now)
	if err == nil || !strings.Contains(err.Error(), "lost reservation response") || sends != 0 {
		t.Fatalf("claim error was not recognized as unsent: sends=%d err=%v", sends, err)
	}
	var attempts int
	var hasRetry bool
	must(faults.QueryRowContext(ctx, `
		SELECT attempts, retry_at IS NOT NULL FROM luxmed_reconnect_delivery WHERE user_id=$1
	`, user.ID).Scan(&attempts, &hasRetry))
	if attempts != 1 || !hasRetry {
		t.Fatalf("persisted claim was not made retryable: attempts=%d retry=%v", attempts, hasRetry)
	}
	_, err = faults.ExecContext(ctx, `DROP TRIGGER lose_claim_reply`)
	must(err)
	must(runner.deliverReconnect(ctx, user, now.Add(2*time.Minute)))
	must(runner.deliverReconnect(ctx, user, now.Add(3*time.Minute)))
	if sends != 1 {
		t.Fatalf("sends after recovery = %d, want one", sends)
	}
}

func TestReconnectEpisodeWriteFailures(t *testing.T) {
	for _, test := range []struct {
		name      string
		operation string
		searchErr error
	}{
		{name: "opening stays due", operation: "INSERT", searchErr: luxmed.ErrAuthentication},
		{name: "closing retries before warning delivery", operation: "DELETE", searchErr: luxmed.ErrUnavailable},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := t.Context()
			path := filepath.Join(t.TempDir(), "watcher.sqlite")
			sends := 0
			runner, db, watch := testRunnerAt(t, reconnectNotifierFunc{send: func() error {
				sends++
				return nil
			}}, path)
			must := func(err error) {
				t.Helper()
				if err != nil {
					t.Fatal(err)
				}
			}
			closing := test.operation == "DELETE"
			if closing {
				must(db.BeginLuxMedReconnect(ctx, watch.UserID))
			}
			searchErr := test.searchErr
			if closing {
				searchErr = nil
			}
			client := &fakeLuxMed{search: func(context.Context) ([]domain.Appointment, error) {
				return nil, searchErr
			}}
			runner.newClient = func(bool) luxmed.Client { return client }
			faults, err := sql.Open("sqlite", path)
			must(err)
			t.Cleanup(func() { faults.Close() })
			_, err = faults.ExecContext(ctx, `CREATE TRIGGER fail_episode BEFORE `+test.operation+` ON luxmed_reconnect
				BEGIN SELECT RAISE(ABORT, 'simulated episode write failure'); END`)
			must(err)
			if err := runner.CheckWatch(ctx, watch); err == nil || (!closing && !errors.Is(err, test.searchErr)) ||
				!strings.Contains(err.Error(), "simulated episode write failure") {
				t.Fatalf("search during write failure: %v", err)
			}
			if closing {
				if err := runner.renewBrowserSession(ctx, 42, nil); err == nil || sends != 0 {
					t.Fatalf("background retry during outage: err=%v sends=%d", err, sends)
				}
			} else {
				saved, err := db.Watch(ctx, watch.ID)
				must(err)
				if saved.LastCheckedAt != nil || saved.LastError != "" {
					t.Fatalf("failed episode took the search out of the queue: %+v", saved)
				}
			}
			_, err = faults.ExecContext(ctx, `DROP TRIGGER fail_episode`)
			must(err)
			if closing {
				reopened, err := storage.Open(ctx, path)
				must(err)
				t.Cleanup(func() { reopened.Close() })
				restarted := NewRunner(reopened, runner.cryptor, runner.notifier, 0, time.Hour, false)
				restarted.newClient = runner.newClient
				must(restarted.renewBrowserSession(ctx, 42, nil))
				must(restarted.renewBrowserSession(ctx, 42, nil))
				if sends != 0 || client.logins != 2 {
					t.Fatalf("restart must recheck before delivery: sends=%d logins=%d", sends, client.logins)
				}
			} else if err := runner.CheckWatch(ctx, watch); !errors.Is(err, luxmed.ErrAuthentication) || sends != 1 {
				t.Fatalf("retry did not deliver the warning: err=%v sends=%d", err, sends)
			}
			required, err := db.LuxMedReconnectRequired(ctx, watch.UserID)
			if err != nil || required == closing {
				t.Fatalf("episode after retry: required=%v err=%v", required, err)
			}
		})
	}
}

func TestPasswordAuthenticationAloneDoesNotResolveReconnect(t *testing.T) {
	for _, test := range []struct {
		name      string
		status    int
		wantErr   error
		reconnect bool
	}{
		{name: "search unavailable after login", status: http.StatusServiceUnavailable, wantErr: luxmed.ErrUnavailable, reconnect: true},
		{
			name: "search rejects fresh login", status: http.StatusUnauthorized,
			wantErr: luxmed.ErrAuthentication, reconnect: true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := t.Context()
			runner, db, watch := testRunner(t, nil)
			client := &fakeLuxMed{search: func(context.Context) ([]domain.Appointment, error) {
				if test.status == http.StatusUnauthorized {
					return nil, luxmed.ErrAuthentication
				}
				return nil, &luxmed.HTTPError{StatusCode: test.status}
			}}
			runner.newClient = func(bool) luxmed.Client { return client }
			if err := db.BeginLuxMedReconnect(ctx, watch.UserID); err != nil {
				t.Fatal(err)
			}
			if err := runner.renewBrowserSession(ctx, 42, nil); !errors.Is(err, test.wantErr) || client.logins != 1 {
				t.Fatalf("search after login: logins=%d err=%v", client.logins, err)
			}
			required, err := db.LuxMedReconnectRequired(ctx, watch.UserID)
			if err != nil || required != test.reconnect {
				t.Fatalf("reconnect = %v, want %v, err=%v", required, test.reconnect, err)
			}
		})
	}
}

func TestDeferredPasswordVerificationSurvivesRestartAndRespectsSearchCooldown(t *testing.T) {
	for _, scenario := range []struct {
		name      string
		searchErr error
	}{
		{name: "protected search confirms the login"},
		{name: "protected search still rejects the token", searchErr: luxmed.ErrAuthentication},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			ctx := t.Context()
			path := filepath.Join(t.TempDir(), "watcher.sqlite")
			sends := 0
			notifier := reconnectNotifierFunc{send: func() error {
				sends++
				return nil
			}}
			oldRunner, db, watch := testRunnerAt(t, notifier, path)
			must := func(err error) {
				t.Helper()
				if err != nil {
					t.Fatal(err)
				}
			}
			watch.CityID, watch.ServiceID, watch.CheckIntervalSeconds = 1, 12, 3600
			must(db.UpdateWatch(ctx, watch))
			must(db.MarkWatchChecked(ctx, watch.ID, false, "luxmed_authentication"))
			must(db.BeginLuxMedReconnect(ctx, watch.UserID))
			must(db.MarkLuxMedReconnectNotified(ctx, watch.UserID))
			const cooldown = 5 * time.Minute
			started := time.Now().UTC()
			reserved, err := db.ReserveLuxMedSearch(ctx, watch.UserID, started, cooldown)
			if err != nil || !reserved {
				t.Fatalf("reserve original search: %v, %v", reserved, err)
			}
			account, err := db.LuxMedAccount(ctx, watch.UserID)
			must(err)
			account.Login = "replacement credentials"
			must(oldRunner.SavePendingLuxMedAccount(ctx, account))

			reopened, err := storage.Open(ctx, path)
			must(err)
			t.Cleanup(func() { reopened.Close() })
			runner := NewRunner(reopened, oldRunner.cryptor, notifier, cooldown, time.Hour, false)
			client := &fakeLuxMed{search: func(context.Context) ([]domain.Appointment, error) {
				return nil, scenario.searchErr
			}}
			runner.newClient = func(bool) luxmed.Client { return client }
			due, err := reopened.DueWatches(ctx, 0)
			must(err)
			if len(due) != 1 || due[0].ID != watch.ID || due[0].CityID != watch.CityID ||
				due[0].ServiceID != watch.ServiceID || due[0].CheckIntervalSeconds != watch.CheckIntervalSeconds {
				t.Fatalf("unexpected queued search after restart: %+v", due)
			}
			if err := runner.CheckWatch(ctx, due[0]); !errors.Is(err, ErrRateLimited) || client.logins != 0 {
				t.Fatalf("verification bypassed cooldown: logins=%d err=%v", client.logins, err)
			}

			must(reopened.ReleaseLuxMedSearch(ctx, watch.UserID, started))
			runner.accounts[watch.UserID].next = time.Time{}
			if err := runner.CheckWatch(ctx, due[0]); !errors.Is(err, scenario.searchErr) || client.logins != 1 {
				t.Fatalf("protected verification: logins=%d err=%v", client.logins, err)
			}
			required, err := reopened.LuxMedReconnectRequired(ctx, watch.UserID)
			if err != nil || required != (scenario.searchErr != nil) || sends != 0 {
				t.Fatalf("episode after verification: open=%v duplicate warnings=%d err=%v", required, sends, err)
			}
		})
	}
}

func TestBrowserRecoveryChecksAccessWithoutRestartingPausedOrExpiredSearch(t *testing.T) {
	for _, paused := range []bool{true, false} {
		name := "expired"
		if paused {
			name = "paused"
		}
		t.Run(name, func(t *testing.T) {
			ctx := t.Context()
			runner, db, watch := testRunner(t, nil)
			must := func(err error) {
				t.Helper()
				if err != nil {
					t.Fatal(err)
				}
			}
			watch.CityID, watch.ServiceID, watch.CheckIntervalSeconds = 1, 12, 3600
			must(db.BeginLuxMedReconnect(ctx, watch.UserID))
			if !paused {
				past := time.Now().AddDate(0, 0, -2)
				watch.DateTo = &past
			}
			must(db.UpdateWatch(ctx, watch))
			must(db.MarkWatchChecked(ctx, watch.ID, false, "luxmed_authentication"))
			must(db.MarkLuxMedReconnectNotified(ctx, watch.UserID))
			if paused {
				must(db.SetWatchStatus(ctx, watch.ID, domain.WatchStatusPaused))
			}
			before, err := db.Watch(ctx, watch.ID)
			must(err)
			var termsCalls atomic.Int32
			var authorized atomic.Bool
			worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				if req.URL.Path == "/session" {
					return
				}
				termsCalls.Add(1)
				q := req.URL.Query()
				if req.URL.Path != luxmed.TermsURL || q.Get("serviceVariantId") != "12" ||
					q.Get("searchDateFrom") == "" || q.Get("searchDateFrom") != q.Get("searchDateTo") {
					t.Errorf("unexpected protected access probe: %s", req.URL.RequestURI())
				}
				if !authorized.Load() {
					w.WriteHeader(http.StatusUnauthorized)
					return
				}
				fmt.Fprint(w, `{"termsForService":{"termsForDays":[]}}`)
			}))
			t.Cleanup(worker.Close)
			browser, err := luxmed.NewBrowser(worker.URL, "http://localhost:18769")
			must(err)
			runner.UseBrowsers(map[int64]*luxmed.Browser{42: browser})
			user, err := db.UserByID(ctx, watch.UserID)
			must(err)
			if required, err := runner.CheckBrowserSession(ctx, user); err != nil || !required.VerificationPending || termsCalls.Load() != 0 {
				t.Fatalf("manual session check attempted a protected search: required=%v err=%v", required, err)
			}
			scheduler := New(db, runner, time.Second, 0, slog.New(slog.NewTextHandler(io.Discard, nil)))
			scheduler.maintainReconnectSessions(ctx)
			required, err := db.LuxMedReconnectRequired(ctx, watch.UserID)
			if err != nil || !required || termsCalls.Load() != 1 {
				t.Fatalf("protected 401 lost the episode: required=%v calls=%d err=%v", required, termsCalls.Load(), err)
			}
			authorized.Store(true)
			scheduler.maintainReconnectSessions(ctx)
			required, err = db.LuxMedReconnectRequired(ctx, watch.UserID)
			if err != nil || required || termsCalls.Load() != 2 {
				t.Fatalf("authorized probe did not clear the episode: required=%v calls=%d err=%v", required, termsCalls.Load(), err)
			}
			after, err := db.Watch(ctx, watch.ID)
			must(err)
			if after.Status != before.Status || after.LastCheckedAt == nil ||
				!after.LastCheckedAt.Equal(*before.LastCheckedAt) || after.LastError != "" {
				t.Fatalf("access probe changed the saved schedule: before=%+v after=%+v", before, after)
			}
		})
	}
}

type termsProbeClient struct {
	fakeLuxMed
	probe func(context.Context, domain.Watch) error
}

func (c *termsProbeClient) CheckTermsAccess(ctx context.Context, watch domain.Watch) error {
	return c.probe(ctx, watch)
}

func TestActiveLegacyRecoveryStaysQueuedAfterTerms503(t *testing.T) {
	ctx := t.Context()
	_, db, watch := testRunner(t, nil)
	watch.CityID, watch.ServiceID, watch.CheckIntervalSeconds = 1, 12, 3600
	if err := db.UpdateWatch(ctx, watch); err != nil {
		t.Fatal(err)
	}
	if err := db.MarkWatchChecked(ctx, watch.ID, false, "luxmed_authentication"); err != nil {
		t.Fatal(err)
	}
	var status atomic.Int32
	status.Store(http.StatusServiceUnavailable)
	var termsCalls atomic.Int32
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path == "/session" {
			return
		}
		termsCalls.Add(1)
		if status.Load() == http.StatusOK {
			fmt.Fprint(w, `{"termsForService":{"termsForDays":[]}}`)
			return
		}
		w.Header().Set("Retry-After", "60")
		w.WriteHeader(int(status.Load()))
	}))
	t.Cleanup(worker.Close)
	browser, err := luxmed.NewBrowser(worker.URL, "http://localhost:18769")
	if err != nil {
		t.Fatal(err)
	}
	start := func() *Scheduler {
		runner := NewRunner(db, nil, nil, 0, time.Hour, false)
		runner.UseBrowsers(map[int64]*luxmed.Browser{42: browser})
		return New(db, runner, time.Second, 0, slog.New(slog.NewTextHandler(io.Discard, nil)))
	}
	start().maintainReconnectSessions(ctx)
	pending, err := db.Watch(ctx, watch.ID)
	if err != nil || pending.LastError != "luxmed_authentication" || termsCalls.Load() != 1 {
		t.Fatalf("terms 503 lost pending verification: watch=%+v calls=%d err=%v", pending, termsCalls.Load(), err)
	}
	if required, err := db.LuxMedReconnectRequired(ctx, watch.UserID); err != nil || required {
		t.Fatalf("terms 503 opened an episode: required=%v err=%v", required, err)
	}

	status.Store(http.StatusOK)
	if ids := start().maintainReconnectSessions(ctx); !slices.Equal(ids, []int64{42}) {
		t.Fatalf("pending verification was lost on restart: %v", ids)
	}
	verified, err := db.Watch(ctx, watch.ID)
	if err != nil || verified.LastError != "" || verified.LastSuccessAt == nil || termsCalls.Load() != 2 {
		t.Fatalf("terms success did not clear pending verification: watch=%+v calls=%d err=%v", verified, termsCalls.Load(), err)
	}
}

func TestAssignedBrowserVerifiesPausedLegacyErrorWithoutPasswordOrEpisode(t *testing.T) {
	ctx := t.Context()
	runner, db, watch := testRunner(t, nil)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	watch.CityID, watch.ServiceID = 1, 12
	must(db.UpdateWatch(ctx, watch))
	must(db.DeleteLuxMedAccount(ctx, watch.UserID))
	must(db.MarkWatchChecked(ctx, watch.ID, false, "luxmed_authentication"))
	if _, err := db.LuxMedAccount(ctx, watch.UserID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("browser fixture unexpectedly has credentials: %v", err)
	}
	other, err := db.UpsertTelegramUser(ctx, domain.User{
		TelegramUserID: 43, Status: domain.UserStatusActive, Role: domain.UserRoleUser,
	})
	must(err)
	otherWatchID, err := db.CreateWatch(ctx, domain.Watch{UserID: other.ID, CityID: 1, ServiceID: 34})
	must(err)
	must(db.SetWatchStatus(ctx, otherWatchID, domain.WatchStatusPaused))
	must(db.MarkWatchChecked(ctx, otherWatchID, false, "luxmed_authentication"))

	var authorized atomic.Bool
	var termsCalls atomic.Int32
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path == "/session" {
			return
		}
		termsCalls.Add(1)
		if req.URL.Path != luxmed.TermsURL || req.URL.Query().Get("serviceVariantId") != "12" {
			t.Errorf("unexpected protected request: %s", req.URL.RequestURI())
		}
		if !authorized.Load() {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		fmt.Fprint(w, `{"termsForService":{"termsForDays":[]}}`)
	}))
	t.Cleanup(worker.Close)
	browser, err := luxmed.NewBrowser(worker.URL, "http://localhost:18769")
	must(err)
	runner.UseBrowsers(map[int64]*luxmed.Browser{42: browser})
	scheduler := New(db, runner, time.Second, 0, slog.New(slog.NewTextHandler(io.Discard, nil)))
	user, err := db.UserByID(ctx, watch.UserID)
	must(err)
	if pending, err := runner.CheckBrowserSession(ctx, user); err != nil || !pending.VerificationPending || termsCalls.Load() != 0 {
		t.Fatalf("manual probe bypassed legacy verification: pending=%v calls=%d err=%v", pending, termsCalls.Load(), err)
	}
	if ids := scheduler.maintainReconnectSessions(ctx); !slices.Equal(ids, []int64{42}) || termsCalls.Load() != 1 {
		t.Fatalf("wrong browser recovery candidates: ids=%v calls=%d", ids, termsCalls.Load())
	}
	before, err := db.Watch(ctx, watch.ID)
	must(err)
	if before.LastError != "luxmed_authentication" || before.LastCheckedAt == nil {
		t.Fatalf("unverified browser cleared a legacy error: %+v", before)
	}
	authorized.Store(true)
	scheduler.maintainReconnectSessions(ctx)
	after, err := db.Watch(ctx, watch.ID)
	must(err)
	if termsCalls.Load() != 2 || after.LastError != "" || after.Status != domain.WatchStatusPaused ||
		after.LastCheckedAt == nil || !after.LastCheckedAt.Equal(*before.LastCheckedAt) {
		t.Fatalf("protected browser recovery changed the pause schedule: %+v", after)
	}
	if required, err := db.LuxMedReconnectRequired(ctx, watch.UserID); err != nil || required {
		t.Fatalf("legacy browser recovery created an episode: required=%v err=%v", required, err)
	}
}

func TestProtectedVerificationSurvivesEditingOnlyPausedSearch(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "watcher.sqlite")
	runner, db, watch := testRunnerAt(t, nil, path)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(db.DeleteLuxMedAccount(ctx, watch.UserID))
	must(db.MarkWatchChecked(ctx, watch.ID, false, "luxmed_authentication"))

	var authorized atomic.Bool
	var termsCalls atomic.Int32
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path == "/session" {
			return
		}
		termsCalls.Add(1)
		if req.URL.Path != luxmed.TermsURL || req.URL.Query().Get("serviceVariantId") != "34" {
			t.Errorf("verification did not use the edited filters: %s", req.URL.RequestURI())
		}
		if !authorized.Load() {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		fmt.Fprint(w, `{"termsForService":{"termsForDays":[]}}`)
	}))
	t.Cleanup(worker.Close)
	browser, err := luxmed.NewBrowser(worker.URL, "http://localhost:18769")
	must(err)
	browsers := map[int64]*luxmed.Browser{42: browser}
	runner.UseBrowsers(browsers)
	user, err := db.UserByID(ctx, watch.UserID)
	must(err)

	// Editing before the first manual check must also preserve legacy evidence.
	watch.CityID, watch.ServiceID = 1, 12
	must(db.UpdateWatch(ctx, watch))
	must(db.SetUserWatchStatus(ctx, watch.UserID, watch.ID, domain.WatchStatusPaused))
	checkPending := func() {
		t.Helper()
		result, err := runner.CheckBrowserSession(ctx, user)
		if err != nil || !result.VerificationPending || result.EpisodeID != "" {
			t.Fatalf("session page bypassed protected verification: %+v, %v", result, err)
		}
	}
	checkPending()

	watch.ServiceID = 34
	must(db.UpdateWatch(ctx, watch))
	saved, err := db.Watch(ctx, watch.ID)
	must(err)
	if saved.LastError != "" {
		t.Fatalf("legacy card still carries the requirement: %+v", saved)
	}
	must(db.Close())

	db, err = storage.Open(ctx, path)
	must(err)
	t.Cleanup(func() { db.Close() })
	runner = NewRunner(db, nil, nil, 0, time.Hour, false)
	runner.UseBrowsers(browsers)
	scheduler := New(db, runner, time.Second, 0, slog.New(slog.NewTextHandler(io.Discard, nil)))

	checkPending()
	if ids := scheduler.maintainReconnectSessions(ctx); !slices.Equal(ids, []int64{42}) || termsCalls.Load() != 1 {
		t.Fatalf("edited paused search lost background verification: ids=%v calls=%d", ids, termsCalls.Load())
	}
	_, access, err := db.WatchesWithReconnectState(ctx, watch.UserID)
	must(err)
	if access.Status != storage.LuxMedAccessRejected || access.EpisodeID != "" {
		t.Fatalf("protected 401 should reject access without opening an episode: %+v", access)
	}

	checkPending()
	authorized.Store(true)
	if ids := scheduler.maintainReconnectSessions(ctx); !slices.Equal(ids, []int64{42}) || termsCalls.Load() != 2 {
		t.Fatalf("protected success was not checked: ids=%v calls=%d", ids, termsCalls.Load())
	}
	watches, access, err := db.WatchesWithReconnectState(ctx, watch.UserID)
	must(err)
	if access.Status != storage.LuxMedAccessConnected || access.EpisodeID != "" ||
		len(watches) != 1 || watches[0].Status != domain.WatchStatusPaused || watches[0].ServiceID != 34 ||
		watches[0].LastCheckedAt != nil {
		t.Fatalf("recovery changed the edited search or did not confirm access: %+v, %+v", watches, access)
	}
	if ids := scheduler.maintainReconnectSessions(ctx); len(ids) != 0 {
		t.Fatalf("confirmed account stayed in the recovery queue: %v", ids)
	}
}

func TestRejectedActiveFilterFallsBackToPausedSearchAfterCooldown(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "watcher.sqlite")
	notifier := &reconnectRecorder{}
	runner, db, active := testRunnerAt(t, notifier, path)
	runner.minimumInterval = time.Minute
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	active.CityID, active.ServiceID = 1, 99
	must(db.UpdateWatch(ctx, active))
	pausedID, err := db.CreateWatch(ctx, domain.Watch{UserID: active.UserID, CityID: 1, ServiceID: 12})
	must(err)
	must(db.SetWatchStatus(ctx, pausedID, domain.WatchStatusPaused))
	must(db.MarkWatchChecked(ctx, pausedID, false, "luxmed_authentication"))
	paused, err := db.Watch(ctx, pausedID)
	must(err)

	var rejected, accepted atomic.Int32
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch {
		case req.URL.Path == "/session":
		case req.URL.Path == luxmed.TermsURL && req.URL.Query().Get("serviceVariantId") == "99":
			rejected.Add(1)
			w.WriteHeader(http.StatusBadRequest)
		case req.URL.Path == luxmed.TermsURL && req.URL.Query().Get("serviceVariantId") == "12":
			if accepted.Add(1) == 1 {
				w.Header().Set("Retry-After", "120")
				w.WriteHeader(http.StatusTooManyRequests)
				return
			}
			fmt.Fprint(w, `{"termsForService":{"termsForDays":[]}}`)
		default:
			t.Errorf("unexpected recovery request: %s", req.URL.RequestURI())
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(worker.Close)
	browser, err := luxmed.NewBrowser(worker.URL, "http://localhost:18769")
	must(err)
	runner.UseBrowsers(map[int64]*luxmed.Browser{42: browser})

	user, err := db.UserByID(ctx, active.UserID)
	must(err)
	if result, err := runner.CheckBrowserSession(ctx, user); err != nil || !result.VerificationPending {
		t.Fatalf("manual check should queue verification: %+v, %v", result, err)
	}
	scheduler := New(db, runner, time.Second, 0, slog.New(slog.NewTextHandler(io.Discard, nil)))
	scheduler.maintainReconnectSessions(ctx)
	if rejected.Load() != 1 || accepted.Load() != 0 {
		t.Fatalf("initial search did not reject the active filter: rejected=%d alternate=%d", rejected.Load(), accepted.Load())
	}

	// Expire the in-memory backoff while the persisted search reservation is still current.
	runner.mu.Lock()
	runner.accounts[user.ID].next = time.Time{}
	runner.mu.Unlock()
	scheduler.maintainReconnectSessions(ctx)
	if rejected.Load() != 1 || accepted.Load() != 0 {
		t.Fatalf("fallback bypassed the persisted cooldown: rejected=%d alternate=%d", rejected.Load(), accepted.Load())
	}
	sqlDB, err := sql.Open("sqlite", path)
	must(err)
	t.Cleanup(func() { sqlDB.Close() })
	expireReservation := func() {
		t.Helper()
		_, err := sqlDB.ExecContext(ctx, `UPDATE luxmed_search_cooldown SET started_at=$1 WHERE user_id=$2`,
			time.Now().UTC().Add(-2*time.Minute), user.ID)
		must(err)
	}
	expireReservation()

	beforeRateLimit := time.Now()
	scheduler.maintainReconnectSessions(ctx)
	runner.mu.Lock()
	retryAt := runner.accounts[user.ID].retryAfter
	runner.mu.Unlock()
	if accepted.Load() != 1 || retryAt.Before(beforeRateLimit.Add(2*time.Minute)) {
		t.Fatalf("fallback ignored Retry-After: alternate=%d retry=%v", accepted.Load(), retryAt)
	}
	expireReservation()
	scheduler.maintainReconnectSessions(ctx)
	if accepted.Load() != 1 || rejected.Load() != 1 {
		t.Fatalf("fallback retried before Retry-After: rejected=%d alternate=%d", rejected.Load(), accepted.Load())
	}
	runner.mu.Lock()
	runner.accounts[user.ID].next = time.Time{}
	runner.accounts[user.ID].retryAfter = time.Time{}
	runner.mu.Unlock()
	scheduler.maintainReconnectSessions(ctx)
	_, access, err := db.WatchesWithReconnectState(ctx, user.ID)
	must(err)
	if rejected.Load() != 1 || accepted.Load() != 2 || access.Status != storage.LuxMedAccessConnected ||
		access.EpisodeID != "" || notifier.attempts != 0 {
		t.Fatalf("fallback did not confirm access: rejected=%d alternate=%d access=%+v warnings=%d",
			rejected.Load(), accepted.Load(), access, notifier.attempts)
	}
	saved, err := db.Watch(ctx, pausedID)
	must(err)
	if saved.Status != domain.WatchStatusPaused || saved.ServiceID != paused.ServiceID || saved.LastError != "" ||
		saved.LastCheckedAt == nil || !saved.LastCheckedAt.Equal(*paused.LastCheckedAt) {
		t.Fatalf("fallback changed the paused search schedule or filters: %+v", saved)
	}
}

func TestPasswordReconnectUsesFallbackIndependentlyOfWarningDelivery(t *testing.T) {
	for _, test := range []struct {
		name      string
		delivered bool
		authErr   error
	}{
		{name: "warning already delivered", delivered: true},
		{name: "rejected credentials still send a due warning", authErr: luxmed.ErrAuthentication},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := t.Context()
			sends := 0
			runner, db, active := testRunner(t, reconnectNotifierFunc{send: func() error {
				sends++
				return nil
			}})
			must := func(err error) {
				t.Helper()
				if err != nil {
					t.Fatal(err)
				}
			}
			active.CityID, active.ServiceID = 1, 99
			must(db.UpdateWatch(ctx, active))
			id, err := db.CreateWatch(ctx, domain.Watch{UserID: active.UserID, CityID: 1, ServiceID: 12})
			must(err)
			must(db.SetWatchStatus(ctx, id, domain.WatchStatusPaused))
			must(db.BeginLuxMedReconnect(ctx, active.UserID))
			if test.delivered {
				must(db.MarkLuxMedReconnectNotified(ctx, active.UserID))
			}
			badFilter := &luxmed.HTTPError{StatusCode: http.StatusBadRequest}
			var filters []int
			client := &termsProbeClient{
				fakeLuxMed: fakeLuxMed{search: func(context.Context) ([]domain.Appointment, error) {
					return nil, badFilter
				}},
				probe: func(_ context.Context, watch domain.Watch) error {
					filters = append(filters, watch.ServiceID)
					return nil
				},
			}
			runner.newClient = func(bool) luxmed.Client { return client }
			if err := runner.renewBrowserSession(ctx, 42, nil); !errors.Is(err, badFilter) {
				t.Fatalf("expected the initial active filter to be rejected, got %v", err)
			}
			if test.authErr != nil {
				client.authErr = test.authErr
				runner.accounts[active.UserID].authenticatedAt = time.Time{}
			}
			err = runner.renewBrowserSession(ctx, 42, nil)
			_, access, readErr := db.WatchesWithReconnectState(ctx, active.UserID)
			must(readErr)
			if test.authErr != nil {
				if !errors.Is(err, test.authErr) || len(filters) != 0 || sends != 1 || access.EpisodeID == "" {
					t.Fatalf("password rejection lost the due warning: err=%v filters=%v sends=%d access=%+v", err, filters, sends, access)
				}
				return
			}
			if err != nil || !slices.Equal(filters, []int{12}) || sends != 0 || access.Status != storage.LuxMedAccessConnected {
				t.Fatalf("delivered warning blocked fallback: err=%v filters=%v sends=%d access=%+v", err, filters, sends, access)
			}
		})
	}
}

func TestTickSkipsRejectedWatchAndChecksAnotherWatchForSameAccount(t *testing.T) {
	ctx := t.Context()
	runner, db, rejected := testRunner(t, nil)
	other, err := db.CreateWatch(ctx, domain.Watch{UserID: rejected.UserID, CityID: 1, ServiceID: 12})
	if err != nil {
		t.Fatal(err)
	}
	state, _ := runner.acquireAccount(rejected.UserID, false)
	state.rejectedReconnectWatch = &rejected
	runner.releaseAccount(state)
	runner.newClient = func(bool) luxmed.Client {
		return &fakeLuxMed{search: func(context.Context) ([]domain.Appointment, error) { return nil, nil }}
	}
	s := New(db, runner, time.Second, 0, slog.New(slog.NewTextHandler(io.Discard, nil)))
	var checks sync.WaitGroup
	s.tick(ctx, &checks, make(chan struct{}, 1))
	checks.Wait()
	first, err := db.Watch(ctx, rejected.ID)
	if err != nil {
		t.Fatal(err)
	}
	next, err := db.Watch(ctx, other)
	if err != nil {
		t.Fatal(err)
	}
	if first.LastCheckedAt != nil || next.LastSuccessAt == nil {
		t.Fatalf("scheduler failed to select the alternative: rejected=%+v other=%+v", first, next)
	}
}

func TestPausedLegacySignInErrorRecoversWithAlternativeProtectedFilter(t *testing.T) {
	ctx := t.Context()
	runner, db, good := testRunner(t, nil)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	good.CityID, good.ServiceID = 1, 12
	must(db.UpdateWatch(ctx, good))
	badID, err := db.CreateWatch(ctx, domain.Watch{UserID: good.UserID, CityID: 1, ServiceID: 99})
	must(err)
	for _, id := range []int64{good.ID, badID} {
		must(db.SetWatchStatus(ctx, id, domain.WatchStatusPaused))
		must(db.MarkWatchChecked(ctx, id, false, "luxmed_authentication"))
	}
	account, err := db.LuxMedAccount(ctx, good.UserID)
	must(err)
	account.Status = domain.AccountStatusActive
	must(runner.SavePendingLuxMedAccount(ctx, account))

	var temporary atomic.Bool
	temporary.Store(true)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch req.URL.Query().Get("serviceVariantId") {
		case "99":
			if temporary.Load() {
				w.Header().Set("Retry-After", "60")
				w.WriteHeader(http.StatusServiceUnavailable)
			} else {
				w.WriteHeader(http.StatusBadRequest)
			}
		case "12":
			fmt.Fprint(w, `{"termsForService":{"termsForDays":[{"terms":[{"dateTimeFrom":"2026-12-01T10:00:00+01:00"},{"dateTimeFrom":"invalid"}]}]}}`)
		default:
			t.Errorf("unexpected protected filter: %s", req.URL.RequestURI())
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	t.Cleanup(server.Close)
	browser, err := luxmed.NewBrowser(server.URL, "http://localhost:18769")
	must(err)
	var filters []int
	client := &termsProbeClient{probe: func(ctx context.Context, watch domain.Watch) error {
		filters = append(filters, watch.ServiceID)
		return browser.Client().CheckTermsAccess(ctx, watch)
	}}
	runner.newClient = func(bool) luxmed.Client { return client }
	scheduler := New(db, runner, time.Second, 0, slog.New(slog.NewTextHandler(io.Discard, nil)))
	scheduler.maintainReconnectSessions(ctx)
	watched, err := db.Watch(ctx, good.ID)
	if err != nil || watched.LastError != "luxmed_authentication" || !slices.Equal(filters, []int{99}) {
		t.Fatalf("temporary failure changed sign-in state or tried a new filter: filters=%v error=%q err=%v", filters, watched.LastError, err)
	}
	temporary.Store(false)
	scheduler.maintainReconnectSessions(ctx)
	if !slices.Equal(filters, []int{99}) {
		t.Fatalf("probe retried during a temporary failure backoff: %v", filters)
	}
	runner.mu.Lock()
	runner.accounts[good.UserID].next = time.Time{}
	runner.mu.Unlock()
	scheduler.maintainReconnectSessions(ctx)
	if !slices.Equal(filters, []int{99, 99, 12}) {
		t.Fatalf("filter rejection blocked alternate verification: %v", filters)
	}
	for _, id := range []int64{good.ID, badID} {
		watch, err := db.Watch(ctx, id)
		if err != nil || watch.Status != domain.WatchStatusPaused || watch.LastError != "" || watch.LastCheckedAt == nil {
			t.Fatalf("verified partial response failed to clear paused search: %+v %v", watch, err)
		}
	}
	if required, err := db.LuxMedReconnectRequired(ctx, good.UserID); err != nil || required {
		t.Fatalf("legacy recovery manufactured an episode: required=%v err=%v", required, err)
	}
}

func TestSharedBrowserPathsShareRetryAfter(t *testing.T) {
	for _, source := range []string{"session", "slot search", "protected probe"} {
		t.Run(source, func(t *testing.T) {
			ctx := t.Context()
			runner, db, watch := testRunner(t, nil)
			must := func(err error) {
				t.Helper()
				if err != nil {
					t.Fatal(err)
				}
			}
			watch.CityID, watch.ServiceID = 1, 12
			must(db.UpdateWatch(ctx, watch))
			must(db.BeginLuxMedReconnect(ctx, watch.UserID))
			if source == "protected probe" {
				must(db.SetWatchStatus(ctx, watch.ID, domain.WatchStatusPaused))
			}
			var sessions, searches atomic.Int32
			worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				switch req.URL.Path {
				case "/accounts/42/session":
					if sessions.Add(1) != 1 || source != "session" {
						return
					}
				case "/accounts/42" + luxmed.TermsURL:
					if searches.Add(1) != 1 || source == "session" {
						fmt.Fprint(w, `{"termsForService":{"termsForDays":[]}}`)
						return
					}
				default:
					t.Errorf("unexpected path: %s", req.URL.Path)
					w.WriteHeader(http.StatusNotFound)
					return
				}
				w.Header().Set("Retry-After", "120")
				w.WriteHeader(http.StatusTooManyRequests)
			}))
			t.Cleanup(worker.Close)
			browser, err := luxmed.NewRoutedBrowser(42, luxmed.BrowserRoute{
				API: worker.URL + "/accounts/42", Shared: true,
			}, "http://localhost:18769", strings.Repeat("a", 64))
			must(err)
			runner.UseBrowsers(map[int64]*luxmed.Browser{42: browser})

			before := time.Now()
			if source == "slot search" {
				err = runner.CheckWatch(ctx, watch)
			} else {
				err = runner.renewBrowserSession(ctx, 42, browser)
			}
			if !errors.Is(err, luxmed.ErrUnavailable) {
				t.Fatalf("expected upstream throttle, got %v", err)
			}
			state := runner.accounts[watch.UserID]
			if state.retryAfter.Before(before.Add(2 * time.Minute)) {
				t.Fatalf("account can retry before Retry-After: %v", state.retryAfter)
			}
			firstSessions, firstSearches := sessions.Load(), searches.Load()
			must(runner.renewBrowserSession(ctx, 42, browser))
			if err := runner.CheckWatch(ctx, watch); !errors.Is(err, ErrRateLimited) {
				t.Fatalf("scheduled search ignored the shared deadline: %v", err)
			}
			user, err := db.UserByID(ctx, watch.UserID)
			must(err)
			if _, err := runner.CheckBrowserSession(ctx, user); !errors.Is(err, luxmed.ErrUnavailable) {
				t.Fatalf("manual check ignored the shared deadline: %v", err)
			}
			if sessions.Load() != firstSessions || searches.Load() != firstSearches {
				t.Fatalf("called upstream before Retry-After: sessions=%d terms=%d", sessions.Load(), searches.Load())
			}
			state.retryAfter = time.Time{}
			state.next = time.Time{}
			must(runner.renewBrowserSession(ctx, 42, browser))
			if sessions.Load() != firstSessions+1 || searches.Load() != firstSearches+1 {
				t.Fatalf("checks did not resume after Retry-After: sessions=%d terms=%d", sessions.Load(), searches.Load())
			}
		})
	}
}

func TestSessionRejectionRequiresSearchAccessToCloseEpisode(t *testing.T) {
	for _, status := range []domain.WatchStatus{domain.WatchStatusActive, domain.WatchStatusPaused, domain.WatchStatusDeleted} {
		t.Run(string(status), func(t *testing.T) {
			ctx := t.Context()
			sends := 0
			runner, db, watch := testRunner(t, reconnectNotifierFunc{send: func() error {
				sends++
				return nil
			}})
			must := func(err error) {
				t.Helper()
				if err != nil {
					t.Fatal(err)
				}
			}
			watch.CityID, watch.ServiceID = 1, 12
			must(db.UpdateWatch(ctx, watch))

			var sessions, terms, catalog atomic.Int32
			var searchReady atomic.Bool
			worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				switch req.URL.Path {
				case "/accounts/42/session":
					// The shared worker can check its catalog once sign-in completes.
					if sessions.Add(1) == 1 {
						w.WriteHeader(http.StatusUnauthorized)
					}
				case "/accounts/42" + luxmed.TermsURL:
					terms.Add(1)
					if !searchReady.Load() {
						w.WriteHeader(http.StatusUnauthorized)
						return
					}
					fmt.Fprint(w, `{"termsForService":{"termsForDays":[]}}`)
				default:
					catalog.Add(1)
					fmt.Fprint(w, `[]`)
				}
			}))
			t.Cleanup(worker.Close)
			browser, err := luxmed.NewRoutedBrowser(42, luxmed.BrowserRoute{
				API: worker.URL + "/accounts/42", Shared: true,
			}, "http://localhost:18769", strings.Repeat("a", 64))
			must(err)
			runner.UseBrowsers(map[int64]*luxmed.Browser{42: browser})
			must(runner.renewBrowserSession(ctx, 42, browser))
			episodeID, err := db.LuxMedReconnectEpisode(ctx, watch.UserID)
			must(err)
			if episodeID == "" || sends != 1 {
				t.Fatalf("session 401 did not open one episode: id=%q sends=%d", episodeID, sends)
			}
			if status != domain.WatchStatusActive {
				must(db.SetUserWatchStatus(ctx, watch.UserID, watch.ID, status))
			}
			user, err := db.UserByID(ctx, watch.UserID)
			must(err)
			result, err := runner.CheckBrowserSession(ctx, user)
			must(err)
			if !result.VerificationPending || result.EpisodeID != episodeID || terms.Load() != 0 {
				t.Fatalf("session preflight bypassed pending search verification: %+v", result)
			}

			err = runner.renewBrowserSession(ctx, 42, browser)
			if status == domain.WatchStatusDeleted {
				must(err)
				if terms.Load() != 0 {
					t.Fatal("tried searching with no usable filter")
				}
			} else if !errors.Is(err, luxmed.ErrAuthentication) || terms.Load() != 1 {
				t.Fatalf("expected protected terms 401, got %v after %d calls", err, terms.Load())
			}
			_, access, err := db.WatchesWithReconnectState(ctx, watch.UserID)
			must(err)
			if access.EpisodeID != episodeID || access.Status == storage.LuxMedAccessConnected || sends != 1 || catalog.Load() != 0 {
				t.Fatalf("catalog or failed search resolved the episode: %+v sends=%d catalog=%d", access, sends, catalog.Load())
			}
			if status == domain.WatchStatusDeleted {
				return
			}

			searchReady.Store(true)
			must(runner.renewBrowserSession(ctx, 42, browser))
			watches, access, err := db.WatchesWithReconnectState(ctx, watch.UserID)
			must(err)
			if access.EpisodeID != "" || access.Status != storage.LuxMedAccessConnected || sends != 1 ||
				len(watches) != 1 || watches[0].Status != status {
				t.Fatalf("successful protected check failed to close only this episode: %+v sends=%d watches=%+v", access, sends, watches)
			}
		})
	}
}

func TestEditedRejectedFilterResumesDespiteUnavailableFallback(t *testing.T) {
	for _, mode := range []string{"background recovery", "scheduled search"} {
		t.Run(mode, func(t *testing.T) {
			ctx := t.Context()
			runner, db, watch := testRunner(t, nil)
			must := func(err error) {
				t.Helper()
				if err != nil {
					t.Fatal(err)
				}
			}
			watch.CityID, watch.ServiceID = 1, 99
			must(db.UpdateWatch(ctx, watch))
			pausedID, err := db.CreateWatch(ctx, domain.Watch{UserID: watch.UserID, CityID: 1, ServiceID: 12})
			must(err)
			must(db.SetWatchStatus(ctx, pausedID, domain.WatchStatusPaused))
			must(db.BeginLuxMedReconnect(ctx, watch.UserID))

			var rejected, fallback, corrected atomic.Int32
			worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				if req.URL.Path == "/session" {
					return
				}
				switch req.URL.Query().Get("serviceVariantId") {
				case "99":
					rejected.Add(1)
					w.WriteHeader(http.StatusBadRequest)
				case "12":
					fallback.Add(1)
					w.Header().Set("Retry-After", "60")
					w.WriteHeader(http.StatusServiceUnavailable)
				case "34":
					corrected.Add(1)
					fmt.Fprint(w, `{"termsForService":{"termsForDays":[]}}`)
				default:
					t.Errorf("unexpected search: %s", req.URL.RequestURI())
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			t.Cleanup(worker.Close)
			browser, err := luxmed.NewBrowser(worker.URL, "http://localhost:18769")
			must(err)
			runner.UseBrowsers(map[int64]*luxmed.Browser{42: browser})
			scheduler := New(db, runner, time.Second, 0, slog.New(slog.NewTextHandler(io.Discard, nil)))

			scheduler.maintainReconnectSessions(ctx)
			scheduler.maintainReconnectSessions(ctx)
			if rejected.Load() != 1 || fallback.Load() != 1 {
				t.Fatalf("expected initial rejection and unavailable fallback: rejected=%d fallback=%d", rejected.Load(), fallback.Load())
			}
			watch.ServiceID = 34
			must(db.UpdateWatch(ctx, watch))
			runner.mu.Lock()
			runner.accounts[watch.UserID].next = time.Time{}
			runner.mu.Unlock()

			if mode == "scheduled search" {
				var checks sync.WaitGroup
				scheduler.tick(ctx, &checks, make(chan struct{}, 1))
				checks.Wait()
			} else {
				scheduler.maintainReconnectSessions(ctx)
			}
			_, access, err := db.WatchesWithReconnectState(ctx, watch.UserID)
			must(err)
			if corrected.Load() != 1 || fallback.Load() != 1 || access.Status != storage.LuxMedAccessConnected {
				t.Fatalf("edited filter stayed excluded: corrected=%d fallback=%d access=%+v", corrected.Load(), fallback.Load(), access)
			}
		})
	}
}

func TestBrowserReconnectWaitsForConfirmedAuthorizationFailure(t *testing.T) {
	ctx := t.Context()
	sends := 0
	runner, db, watch := testRunner(t, reconnectNotifierFunc{send: func() error {
		sends++
		return nil
	}})
	if err := db.BeginLuxMedReconnect(ctx, watch.UserID); err != nil {
		t.Fatal(err)
	}
	var status atomic.Int32
	status.Store(http.StatusGatewayTimeout)
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path != "/session" {
			t.Errorf("unexpected path: %s", req.URL.Path)
		}
		w.Header().Set("Retry-After", "60")
		w.WriteHeader(int(status.Load()))
	}))
	t.Cleanup(worker.Close)
	browser, err := luxmed.NewBrowser(worker.URL, "http://localhost:18769")
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.renewBrowserSession(ctx, 42, browser); !errors.Is(err, luxmed.ErrUnavailable) || sends != 0 {
		t.Fatalf("inconclusive session probe triggered a warning: sends=%d err=%v", sends, err)
	}
	status.Store(http.StatusUnauthorized)
	if err := runner.renewBrowserSession(ctx, 42, browser); err != nil || sends != 1 {
		t.Fatalf("confirmed session rejection did not produce a warning: sends=%d err=%v", sends, err)
	}
}

func TestSessionProbeRechecksActiveSearchesBeforeOpeningOrSending(t *testing.T) {
	for _, test := range []struct {
		name            string
		stop            domain.WatchStatus
		existingEpisode bool
		anotherActive   bool
	}{
		{name: "last search paused", stop: domain.WatchStatusPaused},
		{name: "last search deleted", stop: domain.WatchStatusDeleted},
		{name: "existing warning and last search paused", stop: domain.WatchStatusPaused, existingEpisode: true},
		{name: "another search remains active", stop: domain.WatchStatusPaused, anotherActive: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			sends := 0
			runner, db, watch := testRunner(t, reconnectNotifierFunc{send: func() error {
				sends++
				return nil
			}})
			if test.existingEpisode {
				if err := db.BeginLuxMedReconnect(ctx, watch.UserID); err != nil {
					t.Fatal(err)
				}
			}
			if test.anotherActive {
				if _, err := db.CreateWatch(ctx, domain.Watch{UserID: watch.UserID}); err != nil {
					t.Fatal(err)
				}
			}

			probing, respond := make(chan struct{}), make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				close(probing)
				select {
				case <-respond:
					w.WriteHeader(http.StatusUnauthorized)
				case <-req.Context().Done():
				}
			}))
			t.Cleanup(server.Close)
			releaseResponse := sync.OnceFunc(func() { close(respond) })
			defer releaseResponse()
			browser, err := luxmed.NewBrowser(server.URL, "http://localhost:18769")
			if err != nil {
				t.Fatal(err)
			}

			done := make(chan error, 1)
			go func() { done <- runner.renewBrowserSession(ctx, 42, browser) }()
			select {
			case <-probing:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			if err := db.SetWatchStatus(ctx, watch.ID, test.stop); err != nil {
				t.Fatal(err)
			}
			releaseResponse()
			if err := <-done; err != nil {
				t.Fatal(err)
			}

			required, err := db.LuxMedReconnectRequired(ctx, watch.UserID)
			if err != nil || required != (test.existingEpisode || test.anotherActive) ||
				(sends == 1) != test.anotherActive || sends > 1 {
				t.Fatalf("after the probe: reconnect=%v sends=%d err=%v", required, sends, err)
			}
		})
	}
}
