package scheduler

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"slices"
	"sync"
	"time"

	"github.com/maksimsurmach/luxmed-watcher/internal/domain"
	"github.com/maksimsurmach/luxmed-watcher/internal/luxmed"
	"github.com/maksimsurmach/luxmed-watcher/internal/security"
	"github.com/maksimsurmach/luxmed-watcher/internal/storage"
)

var (
	ErrRateLimited = errors.New("watch check is cooling down")
	ErrInactive    = errors.New("watch or user is inactive")
	ErrNoAccount   = errors.New("LuxMed account is not connected")
)

type Notifier interface {
	NotifyAppointments(context.Context, domain.User, domain.Watch, []domain.AppointmentHistory, int) (int, error)
}

// ReconnectDeliveryRejected marks a retryable attempt known to have failed.
type ReconnectDeliveryRejected struct {
	RetryAfter time.Duration
	BeforeSend bool
	// A nonzero limit counts only explicit Telegram 403 responses in this episode.
	ForbiddenLimit int
}

func (err *ReconnectDeliveryRejected) Error() string {
	if err.BeforeSend {
		return "reconnect warning could not be sent to telegram"
	}
	return "telegram temporarily rejected reconnect warning"
}

type reconnectNotifier interface {
	NotifyLuxMedReconnect(context.Context, domain.User) error
}

type accountCheck struct {
	busy                     bool
	next, lastCheck          time.Time
	failures                 int
	authenticatedAt          time.Time
	login, encryptedPassword string
	client                   luxmed.Client
}

type Runner struct {
	browsers             map[int64]*luxmed.Browser
	db                   *storage.DB
	cryptor              *security.Cryptor
	notifier             Notifier
	newClient            func(bool) luxmed.Client
	minimumInterval      time.Duration
	notificationCooldown time.Duration
	rawPayloads          bool
	mu                   sync.Mutex
	accounts             map[int64]*accountCheck
	capacity             chan struct{}
}

func NewRunner(db *storage.DB, cryptor *security.Cryptor, notifier Notifier, minimumInterval, cooldown time.Duration, rawPayloads bool) *Runner {
	return &Runner{
		db: db, cryptor: cryptor, notifier: notifier,
		newClient:       func(raw bool) luxmed.Client { return luxmed.NewHTTPClient(raw) },
		minimumInterval: minimumInterval, notificationCooldown: cooldown, rawPayloads: rawPayloads,
		accounts: make(map[int64]*accountCheck), capacity: make(chan struct{}, 3),
	}
}

func (r *Runner) UseBrowsers(browsers map[int64]*luxmed.Browser) {
	r.browsers = browsers
}

func (r *Runner) CheckWatch(ctx context.Context, watch domain.Watch) error {
	_, err := r.check(ctx, watch, false)
	return err
}

func (r *Runner) ManualCheck(ctx context.Context, watch domain.Watch) (int, error) {
	return r.check(ctx, watch, true)
}

func (r *Runner) acquireAccount(userID int64, respectCooldown bool) *accountCheck {
	r.mu.Lock()
	defer r.mu.Unlock()

	state := r.accounts[userID]
	if state == nil {
		state = &accountCheck{}
		r.accounts[userID] = state
	}
	if state.busy || respectCooldown && time.Now().Before(state.next) {
		return nil
	}
	state.busy = true
	state.lastCheck = time.Now()
	return state
}

func (r *Runner) releaseAccount(state *accountCheck) {
	r.mu.Lock()
	defer r.mu.Unlock()
	state.busy = false
}

func (r *Runner) check(ctx context.Context, requested domain.Watch, manual bool) (count int, err error) {
	state := r.acquireAccount(requested.UserID, true)
	if state == nil {
		return 0, ErrRateLimited
	}
	defer r.releaseAccount(state)

	select {
	case r.capacity <- struct{}{}:
		defer func() { <-r.capacity }()
	case <-ctx.Done():
		return 0, ctx.Err()
	}

	parent := ctx
	ctx, cancel := context.WithTimeout(parent, 70*time.Second)
	defer cancel()

	watch, err := r.db.Watch(ctx, requested.ID)
	if err != nil {
		return 0, fmt.Errorf("load watch: %w", err)
	}
	if watch.UserID != requested.UserID || watch.Status != domain.WatchStatusActive {
		return 0, ErrInactive
	}
	interval := max(time.Duration(watch.CheckIntervalSeconds)*time.Second, r.minimumInterval)
	if manual {
		interval = r.minimumInterval
	}
	if watch.LastCheckedAt != nil && time.Since(*watch.LastCheckedAt) < interval {
		return 0, ErrRateLimited
	}

	user, err := r.db.UserByID(ctx, watch.UserID)
	if err != nil {
		return 0, fmt.Errorf("load watch owner: %w", err)
	}
	if !user.IsActive() {
		return 0, ErrInactive
	}
	started := time.Now().UTC()
	if r.minimumInterval > 0 {
		reserved, err := r.db.ReserveLuxMedSearch(ctx, user.ID, started, r.minimumInterval)
		if err != nil {
			return 0, fmt.Errorf("reserve LuxMed search: %w", err)
		}
		if !reserved {
			r.mu.Lock()
			state.next = started.Add(min(15*time.Second, r.minimumInterval))
			r.mu.Unlock()
			return 0, ErrRateLimited
		}
	}
	count, err = r.run(ctx, user, watch, state, started)
	if errors.Is(err, luxmed.ErrBrowserBusy) && !errors.Is(err, luxmed.ErrPartialResults) && count == 0 {
		r.mu.Lock()
		state.next = time.Now().Add(3*time.Second + jitter(time.Second))
		r.mu.Unlock()

		if r.minimumInterval > 0 {
			releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(parent), 5*time.Second)
			defer cancel()
			if releaseErr := r.db.ReleaseLuxMedSearch(releaseCtx, user.ID, started); releaseErr != nil {
				return 0, fmt.Errorf("release unused LuxMed search reservation: %w", releaseErr)
			}
		}
		return 0, errors.Join(err, parent.Err())
	}
	if parent.Err() != nil {
		return count, errors.Join(err, parent.Err())
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		err = errors.Join(err, luxmed.ErrUnavailable, ctx.Err())
	}
	r.mu.Lock()
	state.next = time.Now().Add(r.minimumInterval + jitter(r.minimumInterval/10))
	var status string
	if err != nil {
		status = checkStatus(err)
		if errors.Is(err, luxmed.ErrUnavailable) {
			state.failures = min(state.failures+1, 5)
			backoff := (15 * time.Second) << (state.failures - 1)
			var upstream *luxmed.HTTPError
			if errors.As(err, &upstream) {
				backoff = max(backoff, min(upstream.RetryAfter, 24*time.Hour))
			}
			state.next = maxTime(state.next, time.Now().Add(backoff+jitter(backoff/5)))
		}
	} else {
		state.failures = 0
	}
	r.mu.Unlock()
	if errors.Is(err, ErrInactive) {
		return count, err
	}
	authFailed := errors.Is(err, luxmed.ErrAuthentication) && !errors.Is(err, luxmed.ErrUnavailable)
	if authFailed {
		notifyCtx, cancel := context.WithTimeout(parent, 10*time.Second)
		err = errors.Join(err, r.requireReconnect(notifyCtx, user))
		cancel()
	}
	recordCtx, stop := context.WithTimeout(parent, 5*time.Second)
	defer stop()
	if writeErr := r.db.MarkWatchChecked(recordCtx, watch.ID, err == nil, status); writeErr != nil {
		err = errors.Join(err, fmt.Errorf("record watch result: %w", writeErr))
	}
	return count, err
}

func (r *Runner) run(ctx context.Context, user domain.User, watch domain.Watch, state *accountCheck, started time.Time) (int, error) {
	var client luxmed.Client
	if r.browsers != nil {
		browser := r.browsers[user.TelegramUserID]
		if browser == nil {
			return 0, ErrInactive
		}
		client = browser.Client()
	} else {
		var err error
		client, err = r.passwordClient(ctx, user.ID, state)
		if err != nil {
			return 0, err
		}
	}
	result, searchErr := client.SearchAppointments(ctx, watch)
	appointments := result.Appointments
	if errors.Is(searchErr, luxmed.ErrAuthentication) {
		state.client = nil
		return 0, searchErr
	}
	if searchErr != nil && len(appointments) == 0 {
		return 0, searchErr
	}
	if err := r.checkWatchUnchanged(ctx, watch); err != nil {
		return 0, errors.Join(searchErr, err)
	}

	if searchErr == nil && result.Checked {
		if err := r.resolveReconnect(ctx, user.ID); err != nil {
			return 0, err
		}
	}

	slices.SortFunc(appointments, func(a, b domain.Appointment) int {
		return cmp.Or(
			a.DateTime.Compare(b.DateTime),
			cmp.Compare(a.FacilityName, b.FacilityName),
			cmp.Compare(a.DoctorName, b.DoctorName),
		)
	})
	seen := make(map[string]struct{}, len(appointments))
	var pending []domain.AppointmentHistory
	for _, appointment := range filterAppointmentsByTimeWindows(appointments, watch.TimeWindows) {
		if _, exists := seen[appointment.Fingerprint]; exists {
			continue
		}
		seen[appointment.Fingerprint] = struct{}{}
		history, _, err := r.db.UpsertAppointmentHistory(ctx, watch.ID, appointment)
		if err != nil {
			return len(seen), errors.Join(searchErr, fmt.Errorf("write appointment history: %w", err))
		}
		if history.Status != domain.HistoryStatusHidden && history.LastNotifiedAt == nil {
			pending = append(pending, history)
		}
	}
	if searchErr == nil {
		if err := r.db.MarkMissingAppointments(ctx, watch.ID, started, started.Add(-r.notificationCooldown)); err != nil {
			return len(seen), fmt.Errorf("update missing appointments: %w", err)
		}
	}
	if len(pending) == 0 {
		return len(seen), searchErr
	}

	if err := r.checkWatchUnchanged(ctx, watch); err != nil {
		return len(seen), errors.Join(searchErr, err)
	}
	recipient, err := r.db.UserByID(ctx, user.ID)
	if err != nil {
		return len(seen), errors.Join(searchErr, fmt.Errorf("reload notification recipient: %w", err))
	}
	if !recipient.IsActive() {
		return len(seen), errors.Join(searchErr, ErrInactive)
	}
	preview := pending[:min(10, len(pending))]
	messageID, notifyErr := r.notifier.NotifyAppointments(ctx, recipient, watch, preview, len(pending))
	status, errText := domain.NotificationStatusSent, ""
	if notifyErr != nil {
		status, errText = domain.NotificationStatusFailed, "telegram delivery failed"
		notifyErr = errors.New(errText)
	}
	deliveryCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	recordErr := r.db.MarkNotifications(deliveryCtx, pending, messageID, status, errText)
	if recordErr != nil {
		recordErr = fmt.Errorf("record notification delivery: %w", recordErr)
	}
	return len(seen), errors.Join(searchErr, notifyErr, recordErr)
}

func (r *Runner) requireReconnect(ctx context.Context, user domain.User) error {
	if err := r.db.BeginLuxMedReconnect(ctx, user.ID); err != nil {
		return fmt.Errorf("save LuxMed reconnect status: %w", err)
	}
	return r.deliverReconnect(ctx, user, time.Now())
}

func (r *Runner) deliverReconnect(ctx context.Context, user domain.User, now time.Time) error {
	notifier, ok := r.notifier.(reconnectNotifier)
	if !ok {
		return nil
	}
	attempt, err := r.db.ClaimLuxMedReconnectDelivery(ctx, user.ID, now)
	if err != nil {
		return fmt.Errorf("claim LuxMed reconnect delivery: %w", err)
	}
	if attempt == 0 {
		return nil
	}

	sendErr := notifier.NotifyLuxMedReconnect(ctx, user)
	recordCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if sendErr == nil {
		if err := r.db.MarkLuxMedReconnectNotified(recordCtx, user.ID); err != nil {
			return fmt.Errorf("record LuxMed reconnect warning delivery: %w", err)
		}
		return nil
	}
	if rejected, ok := errors.AsType[*ReconnectDeliveryRejected](sendErr); ok {
		backoff := min(time.Hour, time.Minute<<min(attempt-1, 6))
		retryAt := time.Now().Add(max(backoff, rejected.RetryAfter))
		if err := r.db.RecordLuxMedReconnectRejection(recordCtx, user.ID, retryAt, rejected.ForbiddenLimit); err != nil {
			return errors.Join(sendErr, fmt.Errorf("record LuxMed reconnect rejection: %w", err))
		}
	}
	return fmt.Errorf("send LuxMed reconnect warning: %w", sendErr)
}

func (r *Runner) resolveReconnect(ctx context.Context, userID int64) error {
	required, err := r.db.LuxMedReconnectRequired(ctx, userID)
	if err == nil && required {
		err = r.db.ResolveLuxMedReconnect(ctx, userID)
	}
	if err != nil {
		return fmt.Errorf("resolve LuxMed reconnect status: %w", err)
	}
	return nil
}

// CheckBrowserSession confirms a manual sign-in and restores saved active searches.
func (r *Runner) CheckBrowserSession(ctx context.Context, user domain.User) error {
	browser := r.browsers[user.TelegramUserID]
	if browser == nil {
		return luxmed.ErrAuthentication
	}
	return r.probeBrowserSession(ctx, user, browser, false)
}

func (r *Runner) renewBrowserSession(ctx context.Context, telegramID int64, browser *luxmed.Browser) error {
	user, err := r.db.UserByTelegramID(ctx, telegramID)
	if err != nil {
		return fmt.Errorf("load LuxMed browser session owner: %w", err)
	}
	return r.probeBrowserSession(ctx, user, browser, true)
}

func (r *Runner) probeBrowserSession(ctx context.Context, user domain.User, browser *luxmed.Browser, background bool) error {
	if !user.IsActive() {
		return nil
	}
	state := r.acquireAccount(user.ID, false)
	if state == nil {
		return luxmed.ErrBrowserBusy
	}
	defer r.releaseAccount(state)

	if browser == nil {
		return r.deliverReconnect(ctx, user, time.Now())
	}
	probeCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
	err := browser.Check(probeCtx)
	cancel()
	if err == nil {
		return r.resolveReconnect(ctx, user.ID)
	}
	if !background || errors.Is(err, luxmed.ErrBrowserBusy) {
		return err
	}
	if errors.Is(err, luxmed.ErrAuthentication) {
		recordCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		return r.requireReconnect(recordCtx, user)
	}
	return errors.Join(err, r.deliverReconnect(ctx, user, time.Now()))
}

func (r *Runner) passwordClient(ctx context.Context, userID int64, state *accountCheck) (luxmed.Client, error) {
	account, err := r.db.LuxMedAccount(ctx, userID)
	if errors.Is(err, sql.ErrNoRows) {
		state.client = nil
		state.login, state.encryptedPassword = "", ""
		return nil, ErrNoAccount
	}
	if err != nil {
		return nil, fmt.Errorf("load LuxMed account: %w", err)
	}
	if account.Status == domain.AccountStatusAuthFailed {
		return nil, luxmed.ErrAuthentication
	}
	if state.client == nil || state.login != account.Login || state.encryptedPassword != account.EncryptedPassword || time.Since(state.authenticatedAt) > 15*time.Minute {
		state.client = nil
		password, err := r.cryptor.Decrypt(account.EncryptedPassword)
		if err != nil {
			return nil, fmt.Errorf("decrypt LuxMed credentials: %w", err)
		}
		client := r.newClient(r.rawPayloads)
		if err := client.Authenticate(ctx, luxmed.Credentials{Login: account.Login, Password: password}); err != nil {
			if errors.Is(err, luxmed.ErrAuthentication) {
				return nil, errors.Join(err, r.recordAuthFailure(ctx, account))
			}
			return nil, err
		}

		now := time.Now().UTC()
		account.Status, account.LastLoginAt, account.LastLoginError = domain.AccountStatusActive, &now, ""
		if err := r.db.UpdateLuxMedAccountAuth(ctx, account); err != nil {
			return nil, fmt.Errorf("save LuxMed account status: %w", err)
		}
		state.client, state.authenticatedAt = client, now
		state.login, state.encryptedPassword = account.Login, account.EncryptedPassword
	}

	return state.client, nil
}

func (r *Runner) checkWatchUnchanged(ctx context.Context, watch domain.Watch) error {
	current, err := r.db.Watch(ctx, watch.ID)
	if err != nil {
		return fmt.Errorf("reload current watch: %w", err)
	}
	if current.Status != domain.WatchStatusActive || !current.UpdatedAt.Equal(watch.UpdatedAt) {
		return ErrInactive
	}
	return nil
}

func (r *Runner) recordAuthFailure(ctx context.Context, account domain.LuxMedAccount) error {
	account.Status, account.LastLoginError = domain.AccountStatusAuthFailed, "luxmed_authentication"
	return r.db.UpdateLuxMedAccountAuth(ctx, account)
}

func checkStatus(err error) string {
	switch {
	case errors.Is(err, ErrNoAccount):
		return "luxmed_account_missing"
	case errors.Is(err, luxmed.ErrAuthentication):
		return "luxmed_authentication"
	case errors.Is(err, luxmed.ErrPartialResults):
		return "luxmed_partial"
	case errors.Is(err, luxmed.ErrUnavailable):
		return "luxmed_unavailable"
	default:
		return "check_failed"
	}
}

func jitter(maximum time.Duration) time.Duration {
	return time.Duration(rand.Int64N(int64(maximum) + 1))
}

func maxTime(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}

type Scheduler struct {
	db        *storage.DB
	runner    *Runner
	interval  time.Duration
	retention time.Duration
	logger    *slog.Logger
}

func New(db *storage.DB, runner *Runner, interval, retention time.Duration, logger *slog.Logger) *Scheduler {
	return &Scheduler{db: db, runner: runner, interval: interval, retention: retention, logger: logger}
}

func (s *Scheduler) Run(ctx context.Context) {
	var nextCleanup time.Time
	var checks sync.WaitGroup
	defer checks.Wait()
	checks.Go(func() { s.maintainBrowserSessions(ctx) })
	dispatched := make(chan struct{}, cap(s.runner.capacity))
	for ctx.Err() == nil {
		if s.retention > 0 && time.Now().After(nextCleanup) {
			if err := s.db.PurgeHistory(ctx, time.Now().Add(-s.retention)); err != nil {
				s.logger.Warn("clean up expired history", "err", err)
			}
			nextCleanup = time.Now().Add(24 * time.Hour)
		}
		s.tick(ctx, &checks, dispatched)

		timer := time.NewTimer(s.interval + jitter(s.interval/5))
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

// Keep shared sessions warm and probe accounts waiting for sign-in separately from slot searches.
func (s *Scheduler) maintainBrowserSessions(ctx context.Context) {
	var nextRenewal time.Time
	for ctx.Err() == nil {
		pending := s.maintainReconnectSessions(ctx)
		if !time.Now().Before(nextRenewal) && len(s.runner.browsers) > 0 {
			ids, err := s.db.ActiveWatchTelegramIDs(ctx)
			if err != nil && ctx.Err() == nil {
				s.logger.Warn("load active accounts for LuxMed session renewal", "err", err)
			}
			for _, id := range ids {
				browser := s.runner.browsers[id]
				if browser != nil && browser.SupportsBackgroundRenewal() && !slices.Contains(pending, id) {
					s.renewSession(ctx, id, browser)
				}
			}
			nextRenewal = time.Now().Add(10 * time.Minute)
		}

		timer := time.NewTimer(time.Minute + jitter(5*time.Second))
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func (s *Scheduler) maintainReconnectSessions(ctx context.Context) []int64 {
	ids, err := s.db.ReconnectWatchTelegramIDs(ctx)
	if err != nil && ctx.Err() == nil {
		s.logger.Warn("load LuxMed accounts waiting for sign-in", "err", err)
	}
	for _, id := range ids {
		s.renewSession(ctx, id, s.runner.browsers[id])
	}
	return ids
}

func (s *Scheduler) renewSession(ctx context.Context, id int64, browser *luxmed.Browser) {
	if ctx.Err() != nil {
		return
	}
	err := s.runner.renewBrowserSession(ctx, id, browser)
	if err != nil && ctx.Err() == nil && !errors.Is(err, luxmed.ErrBrowserBusy) {
		s.logger.Warn("renew LuxMed browser session", "status", checkStatus(err), "err", err)
	}
}

func (s *Scheduler) tick(ctx context.Context, checks *sync.WaitGroup, dispatched chan struct{}) {
	now := time.Now()
	s.runner.mu.Lock()
	for userID, account := range s.runner.accounts {
		if !account.busy && now.After(account.next) && now.Sub(account.lastCheck) > 15*time.Minute {
			delete(s.runner.accounts, userID)
		}
	}
	s.runner.mu.Unlock()

	watches, err := s.db.DueWatches(ctx, 0)
	if err != nil {
		s.logger.Warn("load due watches", "err", err)
		return
	}
	accounts := make(map[int64]bool)
	for _, watch := range watches {
		if ctx.Err() != nil {
			return
		}
		if accounts[watch.UserID] {
			continue
		}
		accounts[watch.UserID] = true
		s.runner.mu.Lock()
		account := s.runner.accounts[watch.UserID]
		blocked := account != nil && (account.busy || now.Before(account.next))
		s.runner.mu.Unlock()
		if blocked {
			continue
		}
		select {
		case dispatched <- struct{}{}:
		default:
			return
		}
		checks.Go(func() {
			defer func() { <-dispatched }()
			count, err := s.runner.check(ctx, watch, false)
			if err == nil {
				s.logger.Info("scheduled slot search completed", "watch_id", watch.ID, "matches", count)
			}
			if err != nil && !errors.Is(err, ErrRateLimited) && !errors.Is(err, ErrInactive) && !errors.Is(err, luxmed.ErrBrowserBusy) {
				s.logger.Warn("watch check failed", "watch_id", watch.ID, "status", checkStatus(err), "err", err)
			}
		})
	}
}

func filterAppointmentsByTimeWindows(apps []domain.Appointment, windows []domain.TimeWindow) []domain.Appointment {
	if len(windows) == 0 {
		return apps
	}

	filtered := make([]domain.Appointment, 0, len(apps))

	for _, app := range apps {
		if appointmentMatchesTimeWindows(app, windows) {
			filtered = append(filtered, app)
		}
	}

	return filtered
}

func appointmentMatchesTimeWindows(app domain.Appointment, windows []domain.TimeWindow) bool {
	local := app.DateTime.In(luxmed.Location())
	appMinutes := local.Hour()*60 + local.Minute()
	appWeekday := (int(local.Weekday())+6)%7 + 1

	for _, window := range windows {
		if window.Weekday > 0 && window.Weekday != appWeekday {
			continue
		}

		from, okFrom := parseClockMinutes(window.From)
		to, okTo := parseClockMinutes(window.To)

		if !okFrom || !okTo {
			continue
		}

		if appMinutes >= from && appMinutes <= to {
			return true
		}
	}

	return false
}

func parseClockMinutes(value string) (int, bool) {
	parsed, err := time.Parse("15:04", value)
	if err != nil {
		return 0, false
	}

	return parsed.Hour()*60 + parsed.Minute(), true
}
