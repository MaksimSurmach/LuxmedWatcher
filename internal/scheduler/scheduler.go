package scheduler

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net/http"
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
	credentialRevision     int64
	inFlight               chan struct{}
	next, lastCheck        time.Time
	failures               int
	authenticatedAt        time.Time
	encryptedPassword      string
	client                 luxmed.Client
	recoveryPending        bool
	nextProbeFilter        int
	rejectedReconnectWatch *domain.Watch
	retryAfter             time.Time
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
		accounts: make(map[int64]*accountCheck),
		capacity: make(chan struct{}, 3),
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

func (r *Runner) acquireAccount(userID int64, respectCooldown bool) (*accountCheck, <-chan struct{}) {
	r.mu.Lock()
	defer r.mu.Unlock()

	state := r.accounts[userID]
	if state == nil {
		state = &accountCheck{}
		r.accounts[userID] = state
	}
	if state.inFlight != nil {
		return nil, state.inFlight
	}
	if respectCooldown && !state.searchReady(time.Now()) {
		return nil, nil
	}
	state.inFlight = make(chan struct{})
	state.lastCheck = time.Now()
	return state, nil
}

func (r *Runner) releaseAccount(state *accountCheck) {
	r.mu.Lock()
	defer r.mu.Unlock()
	close(state.inFlight)
	state.inFlight = nil
}

// SaveVerifiedLuxMedAccount commits sign-in after any in-flight check or delivery.
func (r *Runner) SaveVerifiedLuxMedAccount(ctx context.Context, account domain.LuxMedAccount) error {
	return r.saveLuxMedAccount(ctx, account.UserID, func() error {
		return r.db.SaveVerifiedLuxMedAccount(ctx, account)
	})
}

func (r *Runner) SavePendingLuxMedAccount(ctx context.Context, account domain.LuxMedAccount) error {
	return r.saveLuxMedAccount(ctx, account.UserID, func() error {
		return r.db.SavePendingLuxMedAccount(ctx, account)
	})
}

func (r *Runner) saveLuxMedAccount(ctx context.Context, userID int64, save func() error) error {
	for ctx.Err() == nil {
		state, done := r.acquireAccount(userID, false)
		if state != nil {
			defer r.releaseAccount(state)
			err := save()
			if err == nil {
				state.client = nil
				state.recoveryPending = false
				state.rejectedReconnectWatch = nil
				state.nextProbeFilter = 0
			}
			return err
		}
		select {
		case <-done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return ctx.Err()
}

// ResumeLuxMedReconnectDelivery lets the next session check deliver a warning after an unblock.
func (r *Runner) ResumeLuxMedReconnectDelivery(ctx context.Context, userID int64) error {
	state, _ := r.acquireAccount(userID, false)
	if state == nil {
		return luxmed.ErrBrowserBusy
	}
	defer r.releaseAccount(state)
	replayErr := r.db.ReplayReconnectRejection(ctx, userID)
	if replayErr != nil && !errors.Is(replayErr, storage.ErrReconnectJournalUnreadable) {
		return replayErr
	}
	resumeErr := r.db.ResumeLuxMedReconnectDelivery(ctx, userID, time.Now())
	if errors.Is(replayErr, storage.ErrReconnectJournalUnreadable) {
		return resumeErr
	}
	return errors.Join(replayErr, resumeErr)
}

func (r *Runner) check(ctx context.Context, requested domain.Watch, manual bool) (count int, err error) {
	state, _ := r.acquireAccount(requested.UserID, true)
	if state == nil {
		return 0, ErrRateLimited
	}
	defer r.releaseAccount(state)
	return r.checkWithAccount(ctx, requested, manual, state)
}

func (r *Runner) checkWithAccount(ctx context.Context, requested domain.Watch, manual bool, state *accountCheck) (count int, err error) {
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
	if !luxmed.SearchWindowOpen(watch, time.Now()) {
		return 0, nil
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
	state.useCredentialRevision(user.CredentialRevision)
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
	r.noteAccessFailure(state, err)
	if errors.Is(err, luxmed.ErrBrowserBusy) && !errors.Is(err, luxmed.ErrUnavailable) &&
		!errors.Is(err, luxmed.ErrPartialResults) && count == 0 {
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
			state.backOffUnavailable()
		}
	} else {
		state.failures = 0
	}
	r.mu.Unlock()
	if errors.Is(err, storage.ErrCredentialsChanged) {
		return count, errors.Join(ErrInactive, err)
	}
	if errors.Is(err, ErrInactive) {
		return count, err
	}
	authFailed := errors.Is(err, luxmed.ErrAuthentication) && !errors.Is(err, luxmed.ErrUnavailable)
	if authFailed {
		state.recoveryPending = false
		notifyCtx, cancel := context.WithTimeout(parent, 10*time.Second)
		beginErr := r.db.BeginLuxMedReconnectForMode(notifyCtx, user)
		if beginErr == nil {
			err = errors.Join(err, r.deliverReconnect(notifyCtx, user, time.Now()))
		}
		cancel()
		if errors.Is(beginErr, storage.ErrCredentialsChanged) {
			return count, errors.Join(ErrInactive, beginErr)
		}
		if beginErr != nil {
			return count, errors.Join(err, fmt.Errorf("save LuxMed reconnect status: %w", beginErr))
		}
	}
	recordCtx, stop := context.WithTimeout(parent, 5*time.Second)
	defer stop()
	if writeErr := r.db.MarkWatchCheckedForMode(recordCtx, user, watch, err == nil, status); errors.Is(writeErr, sql.ErrNoRows) || errors.Is(writeErr, storage.ErrCredentialsChanged) {
		err = errors.Join(err, ErrInactive)
	} else if writeErr != nil {
		err = errors.Join(err, fmt.Errorf("record watch result: %w", writeErr))
	}
	return count, err
}

func (r *Runner) run(ctx context.Context, user domain.User, watch domain.Watch, state *accountCheck, started time.Time) (int, error) {
	browser := r.browsers[user.TelegramUserID]
	if r.browsers != nil && browser == nil {
		return 0, ErrInactive
	}
	account, err := r.db.LoginAccount(ctx, user.ID, browser != nil)
	var client luxmed.Client
	switch {
	case errors.Is(err, sql.ErrNoRows):
		state.client = nil
		state.encryptedPassword = ""
		if browser == nil {
			return 0, ErrNoAccount
		}
		client = browser.Client()
	case err != nil:
		return 0, fmt.Errorf("load LuxMed account: %w", err)
	default:
		client, err = r.passwordClient(ctx, account, state)
		if err != nil {
			return 0, err
		}
	}
	result, searchErr := client.SearchAppointments(ctx, watch)
	if searchErr == nil && !result.Checked && !luxmed.SearchWindowOpen(watch, time.Now()) {
		return 0, ErrInactive
	}
	appointments := result.Appointments
	if errors.Is(searchErr, luxmed.ErrAuthentication) {
		state.client = nil
		state.recoveryPending = false
		return 0, r.recordPasswordAccessFailure(ctx, state, account, searchErr)
	}
	if result.Checked {
		if err := r.confirmReconnectRecovery(ctx, user, state); err != nil {
			return 0, errors.Join(searchErr, err)
		}
	}
	if searchErr != nil && len(appointments) == 0 {
		return 0, searchErr
	}
	if err := r.checkWatchUnchanged(ctx, watch); err != nil {
		return 0, errors.Join(searchErr, err)
	}

	slices.SortFunc(appointments, func(a, b domain.Appointment) int {
		return cmp.Or(
			a.DateTime.Compare(b.DateTime),
			cmp.Compare(a.FacilityName, b.FacilityName),
			cmp.Compare(a.DoctorName, b.DoctorName),
		)
	})
	seen := make(map[string]struct{}, len(appointments))
	distinct := make([]domain.Appointment, 0, len(appointments))
	for _, appointment := range filterAppointmentsByTimeWindows(appointments, watch.TimeWindows) {
		if _, exists := seen[appointment.Fingerprint]; exists {
			continue
		}
		seen[appointment.Fingerprint] = struct{}{}
		distinct = append(distinct, appointment)
	}
	pending, err := r.db.SaveSearchHistory(ctx, user, storage.SearchHistoryUpdate{
		WatchID: watch.ID, Appointments: distinct, Complete: searchErr == nil,
		StartedAt: started, RenotifyAt: started.Add(-r.notificationCooldown),
	})
	if err != nil {
		return len(seen), errors.Join(searchErr, fmt.Errorf("save search history: %w", err))
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
	if !recipient.IsActive() || recipient.CredentialRevision != user.CredentialRevision {
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
	if err := r.db.BeginLuxMedReconnectForMode(ctx, user); err != nil {
		return fmt.Errorf("save LuxMed reconnect status: %w", err)
	}
	return r.deliverReconnect(ctx, user, time.Now())
}

func (r *Runner) deliverReconnect(ctx context.Context, user domain.User, now time.Time) error {
	return r.deliverReconnectAfterReplay(ctx, user, now, r.replayReconnect(ctx, user.ID))
}

func (r *Runner) replayReconnect(ctx context.Context, userID int64) error {
	replayCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return r.db.ReplayReconnectRejection(replayCtx, userID)
}

func (r *Runner) deliverReconnectAfterReplay(ctx context.Context, user domain.User, now time.Time, replayErr error) error {
	if replayErr != nil && !errors.Is(replayErr, storage.ErrReconnectJournalUnreadable) {
		return replayErr
	}
	notifier, ok := r.notifier.(reconnectNotifier)
	if !ok {
		return nil
	}
	claim, err := r.db.ClaimLuxMedReconnectDeliveryForMode(ctx, user, now)
	if err != nil {
		// The database may have committed this ID before its response was lost.
		// Telegram has not been called, so this particular claim can be retried.
		if claim.ID != "" {
			recordCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			err = errors.Join(err, r.db.JournalUnsentReconnectReservation(recordCtx, user.ID, claim, time.Now().Add(time.Minute)))
			cancel()
		}
		return errors.Join(replayErr, fmt.Errorf("claim LuxMed reconnect delivery: %w", err))
	}
	if claim.Attempt == 0 {
		return replayErr
	}

	sendErr := notifier.NotifyLuxMedReconnect(ctx, user)
	if sendErr == nil {
		recordCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if err := r.db.MarkLuxMedReconnectNotified(recordCtx, user.ID); err != nil {
			return errors.Join(replayErr, fmt.Errorf("record LuxMed reconnect warning delivery: %w", err))
		}
		return replayErr
	}
	if rejected, ok := errors.AsType[*ReconnectDeliveryRejected](sendErr); ok {
		backoff := min(time.Hour, time.Minute<<min(claim.Attempt-1, 6))
		retryAt := time.Now().Add(max(backoff, rejected.RetryAfter))
		sendErr = errors.Join(sendErr, r.recordReconnectRejection(ctx, user.ID, claim, retryAt, rejected.ForbiddenLimit))
	}
	return errors.Join(replayErr, fmt.Errorf("send LuxMed reconnect warning: %w", sendErr))
}

func (r *Runner) recordReconnectRejection(ctx context.Context, userID int64, claim storage.ReconnectDeliveryClaim, retryAt time.Time, forbiddenLimit int) error {
	recordCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := r.db.JournalReconnectRejection(recordCtx, userID, claim, retryAt, forbiddenLimit); err != nil {
		return fmt.Errorf("record LuxMed reconnect rejection: %w", err)
	}
	return nil
}

func (r *Runner) confirmReconnectRecovery(ctx context.Context, user domain.User, state *accountCheck) error {
	err := r.resolveReconnect(ctx, user, true)
	state.recoveryPending = err != nil && !errors.Is(err, storage.ErrCredentialsChanged)
	if err == nil {
		state.rejectedReconnectWatch = nil
		state.nextProbeFilter = 0
	}
	return err
}

func (r *Runner) resolveReconnect(ctx context.Context, user domain.User, requeue bool) error {
	// Persist a confirmed session even if the search expires after a successful response.
	recordCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := r.db.ResolveLuxMedReconnectForMode(recordCtx, user, requeue); err != nil {
		return fmt.Errorf("resolve LuxMed reconnect status: %w", err)
	}
	return nil
}

type BrowserSessionCheck struct {
	EpisodeID           string
	VerificationPending bool
	NoSavedSearches     bool
}

// CheckBrowserSession confirms a manual sign-in and restores saved active searches.
func (r *Runner) CheckBrowserSession(ctx context.Context, user domain.User) (BrowserSessionCheck, error) {
	browser := r.browsers[user.TelegramUserID]
	return r.probeBrowserSession(ctx, user, browser, false)
}

func (r *Runner) renewBrowserSession(ctx context.Context, telegramID int64, browser *luxmed.Browser) error {
	user, err := r.db.UserByTelegramID(ctx, telegramID)
	if err != nil {
		return fmt.Errorf("load LuxMed browser session owner: %w", err)
	}
	_, err = r.probeBrowserSession(ctx, user, browser, true)
	return err
}

func (r *Runner) probeBrowserSession(ctx context.Context, user domain.User, browser *luxmed.Browser, background bool) (BrowserSessionCheck, error) {
	if !user.IsActive() {
		return BrowserSessionCheck{}, nil
	}
	state, _ := r.acquireAccount(user.ID, false)
	if state == nil {
		return BrowserSessionCheck{}, luxmed.ErrBrowserBusy
	}
	defer r.releaseAccount(state)
	state.useCredentialRevision(user.CredentialRevision)

	account, err := r.db.LoginAccount(ctx, user.ID, browser != nil || r.browsers != nil)
	saved := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return BrowserSessionCheck{}, fmt.Errorf("load LuxMed sign-in mode: %w", err)
	}
	if saved || browser == nil {
		if background {
			if state.recoveryPending {
				return BrowserSessionCheck{}, r.confirmReconnectRecovery(ctx, user, state)
			}
			if saved || r.browsers == nil {
				return BrowserSessionCheck{}, r.recheckPasswordSession(ctx, user, state)
			}
			return BrowserSessionCheck{}, nil
		}
		if !saved {
			return BrowserSessionCheck{}, luxmed.ErrAuthentication
		}
	}
	if time.Now().Before(state.retryAfter) {
		if background {
			return BrowserSessionCheck{}, nil
		}
		return BrowserSessionCheck{}, luxmed.ErrUnavailable
	}
	timeout := 25 * time.Second
	if !background {
		timeout = 15 * time.Second
	}
	probeCtx, cancel := context.WithTimeout(ctx, timeout)
	if saved {
		var client luxmed.Client
		// Dictionary access cannot stand in for the protected search below.
		client, err = r.passwordClient(probeCtx, account, state)
		if err == nil {
			_, err = client.GetCities(probeCtx)
			err = r.recordPasswordAccessFailure(probeCtx, state, account, err)
		}
	} else {
		err = browser.Check(probeCtx)
	}
	cancel()
	r.noteAccessFailure(state, err)
	if err == nil {
		verification, readErr := r.db.LuxMedReconnectVerification(ctx, user.ID)
		if readErr != nil {
			return BrowserSessionCheck{}, readErr
		}
		if verification.RequiresTerms && !state.recoveryPending {
			if !verification.HasSavedWatches {
				return BrowserSessionCheck{EpisodeID: verification.EpisodeID, NoSavedSearches: true}, nil
			}
			if !background {
				// The existing reconciliation loop owns the protected search.
				if err := r.db.SetLuxMedAccessForMode(ctx, user, storage.LuxMedAccessPending, true); err != nil {
					return BrowserSessionCheck{}, err
				}
				return BrowserSessionCheck{EpisodeID: verification.EpisodeID, VerificationPending: true}, nil
			}
			return BrowserSessionCheck{}, r.checkReconnectSearch(ctx, user, state, time.Now(), browser)
		}
		return BrowserSessionCheck{}, r.confirmReconnectRecovery(ctx, user, state)
	}
	if errors.Is(err, luxmed.ErrAuthentication) {
		state.recoveryPending = false
	}
	if !background {
		if !errors.Is(err, luxmed.ErrAuthentication) {
			return BrowserSessionCheck{}, err
		}
		// Keep the episode and the rejected probe in the same account operation.
		episodeID, readErr := r.db.LuxMedReconnectEpisode(ctx, user.ID)
		if readErr != nil {
			return BrowserSessionCheck{}, fmt.Errorf("read LuxMed reconnect status: %w", readErr)
		}
		if writeErr := r.db.SetLuxMedAccessForMode(ctx, user, storage.LuxMedAccessRejected, false); writeErr != nil {
			return BrowserSessionCheck{}, writeErr
		}
		return BrowserSessionCheck{EpisodeID: episodeID}, err
	}
	if errors.Is(err, luxmed.ErrBrowserBusy) {
		return BrowserSessionCheck{}, err
	}
	if errors.Is(err, luxmed.ErrAuthentication) {
		recordCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		return BrowserSessionCheck{}, r.requireReconnect(recordCtx, user)
	}
	if state.recoveryPending {
		return BrowserSessionCheck{}, errors.Join(err, r.confirmReconnectRecovery(ctx, user, state))
	}
	return BrowserSessionCheck{}, err
}

func (r *Runner) recheckPasswordSession(ctx context.Context, user domain.User, state *accountCheck) error {
	replayErr := r.replayReconnect(ctx, user.ID)
	return errors.Join(replayErr, r.checkReconnectSearch(ctx, user, state, time.Now(), nil))
}

func (r *Runner) checkReconnectSearch(ctx context.Context, user domain.User, state *accountCheck, now time.Time, browser *luxmed.Browser) error {
	r.mu.Lock()
	ready := state.searchReady(now)
	r.mu.Unlock()
	if !ready {
		return nil
	}
	watches, err := r.db.WatchesByUser(ctx, user.ID)
	if err != nil {
		return err
	}
	if state.rejectedReconnectWatch != nil {
		index := slices.IndexFunc(watches, state.hasRejectedReconnectFilters)
		if index >= 0 && watches[index].Status == domain.WatchStatusActive {
			rejected := watches[index]
			copy(watches[index:], watches[index+1:])
			watches[len(watches)-1] = rejected
			return r.probeReconnectFilters(ctx, user, state, browser, watches)
		}
		state.rejectedReconnectWatch = nil
		state.nextProbeFilter = 0
	}
	hasSearch := false
	for _, watch := range watches {
		if watch.Status == domain.WatchStatusActive && luxmed.SearchWindowOpen(watch, now) {
			hasSearch = true
			// Use the same authenticated search and persisted cooldown as a normal check.
			_, err := r.checkWithAccount(ctx, watch, true, state)
			if errors.Is(err, ErrRateLimited) || errors.Is(err, ErrInactive) {
				continue
			}
			if !errors.Is(err, luxmed.ErrPartialResults) && retryWithAnotherFilter(err) &&
				slices.ContainsFunc(watches, func(candidate domain.Watch) bool {
					return candidate.ID != watch.ID && candidate.CityID > 0 && candidate.ServiceID > 0
				}) {
				// The next probe can use another filter once the normal cooldown expires.
				state.rejectedReconnectWatch = &watch
				state.nextProbeFilter = 0
			}
			return err
		}
	}
	if hasSearch {
		return nil
	}
	return r.probeReconnectFilters(ctx, user, state, browser, watches)
}

// Search results update UpdatedAt too, so compare the saved filters themselves.
func (state *accountCheck) hasRejectedReconnectFilters(watch domain.Watch) bool {
	rejected := state.rejectedReconnectWatch
	if rejected == nil || watch.ID != rejected.ID {
		return false
	}
	return watch.CityID == rejected.CityID && watch.CityName == rejected.CityName &&
		watch.ServiceID == rejected.ServiceID && watch.DoctorMode == rejected.DoctorMode &&
		optionalValue(watch.DoctorID) == optionalValue(rejected.DoctorID) &&
		watch.FacilityMode == rejected.FacilityMode && slices.Equal(watch.FacilityIDs, rejected.FacilityIDs) &&
		optionalValue(watch.DateFrom).Equal(optionalValue(rejected.DateFrom)) &&
		optionalValue(watch.DateTo).Equal(optionalValue(rejected.DateTo)) &&
		watch.NextDays == rejected.NextDays && slices.Equal(watch.TimeWindows, rejected.TimeWindows)
}

func optionalValue[T any](value *T) T {
	if value != nil {
		return *value
	}
	var zero T
	return zero
}

func (r *Runner) probeReconnectFilters(ctx context.Context, user domain.User, state *accountCheck, browser *luxmed.Browser, watches []domain.Watch) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	var client luxmed.Client
	var err error
	var account domain.LuxMedAccount
	if browser != nil {
		client = browser.Client()
	} else {
		account, err = r.db.LoginAccount(ctx, user.ID, r.browsers != nil)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err == nil {
			client, err = r.passwordClient(ctx, account, state)
		}
		switch {
		case errors.Is(err, luxmed.ErrAuthentication) && !errors.Is(err, luxmed.ErrUnavailable):
			return r.recordAccessRejection(ctx, user, state, err)
		}
	}
	if err == nil {
		err = r.probeReconnectAccess(ctx, user, state, client, watches)
		err = r.recordPasswordAccessFailure(ctx, state, account, err)
	}
	r.mu.Lock()
	if errors.Is(err, luxmed.ErrUnavailable) {
		state.backOffUnavailable()
	}
	state.applyRetryAfter(time.Now(), err)
	r.mu.Unlock()
	return err
}

func (r *Runner) probeReconnectAccess(ctx context.Context, user domain.User, state *accountCheck, client luxmed.Client, watches []domain.Watch) error {
	watches = slices.DeleteFunc(watches, func(watch domain.Watch) bool {
		return watch.CityID <= 0 || watch.ServiceID <= 0
	})
	var rejectedFilter error
	start := state.nextProbeFilter
	for offset := range len(watches) {
		index := (start + offset) % len(watches)
		started := time.Now().UTC()
		if r.minimumInterval > 0 {
			ok, err := r.db.ReserveLuxMedSearch(ctx, user.ID, started, r.minimumInterval)
			if err != nil || !ok {
				return errors.Join(rejectedFilter, err)
			}
		}
		err := client.CheckTermsAccess(ctx, watches[index])
		if err == nil {
			if state.rejectedReconnectWatch != nil {
				return r.confirmReconnectRecovery(ctx, user, state)
			}
			state.nextProbeFilter = 0
			return r.resolveReconnect(ctx, user, false)
		}
		if errors.Is(err, luxmed.ErrBrowserBusy) && !errors.Is(err, luxmed.ErrUnavailable) && r.minimumInterval > 0 {
			releaseCtx, stop := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer stop()
			return errors.Join(err, r.db.ReleaseLuxMedSearch(releaseCtx, user.ID, started))
		}
		if errors.Is(err, luxmed.ErrAuthentication) {
			return r.recordAccessRejection(ctx, user, state, err)
		}
		if !retryWithAnotherFilter(err) {
			return err
		}
		rejectedFilter = err
		state.nextProbeFilter = (index + 1) % len(watches)
	}
	// With no usable saved filter, there is no proof that the search endpoint works.
	return rejectedFilter
}

func (r *Runner) recordAccessRejection(ctx context.Context, user domain.User, state *accountCheck, rejection error) error {
	recordCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if state.rejectedReconnectWatch == nil {
		return errors.Join(rejection, r.db.SetLuxMedAccessForMode(recordCtx, user, storage.LuxMedAccessRejected, true))
	}

	if err := r.db.BeginLuxMedReconnectForMode(recordCtx, user); err != nil {
		return errors.Join(rejection, err)
	}
	return errors.Join(rejection, r.deliverReconnect(recordCtx, user, time.Now()))
}

func retryWithAnotherFilter(err error) bool {
	if errors.Is(err, luxmed.ErrAuthentication) || errors.Is(err, luxmed.ErrUnavailable) {
		return false
	}
	if status, ok := errors.AsType[*luxmed.HTTPError](err); ok {
		switch status.StatusCode {
		case http.StatusBadRequest, http.StatusNotFound, http.StatusUnprocessableEntity:
			return true
		}
	}
	return errors.Is(err, luxmed.ErrResponse)
}

func (r *Runner) passwordClient(ctx context.Context, account domain.LuxMedAccount, state *accountCheck) (luxmed.Client, error) {
	if account.Status == domain.AccountStatusAuthFailed {
		state.client = nil
		return nil, luxmed.ErrAuthentication
	}
	if state.client == nil || state.encryptedPassword != account.EncryptedPassword || time.Since(state.authenticatedAt) > 15*time.Minute {
		state.client = nil
		creds, err := r.cryptor.OpenCredentials(account.UserID, account.EncryptedPassword)
		if err != nil {
			return nil, fmt.Errorf("decrypt LuxMed credentials: %w", err)
		}
		client := r.newClient(r.rawPayloads)
		if err := client.Authenticate(ctx, creds); err != nil {
			if errors.Is(err, luxmed.ErrAuthentication) && !errors.Is(err, luxmed.ErrUnavailable) {
				err = errors.Join(luxmed.ErrCredentialsRejected, err)
			}
			return nil, r.recordPasswordAccessFailure(ctx, state, account, err)
		}

		now := time.Now().UTC()
		account.Status, account.LastLoginAt, account.LastLoginError = domain.AccountStatusActive, &now, ""
		if err := r.db.UpdateLuxMedAccountAuth(ctx, account); err != nil {
			return nil, fmt.Errorf("save LuxMed account status: %w", err)
		}
		state.client, state.authenticatedAt = client, now
		state.encryptedPassword = account.EncryptedPassword
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

// backOffUnavailable is called with the runner mutex held.
func (state *accountCheck) backOffUnavailable() {
	state.failures = min(state.failures+1, 5)
	backoff := (15 * time.Second) << (state.failures - 1)
	now := time.Now()
	state.next = maxTime(state.next, now.Add(backoff+jitter(backoff/5)))
}

func (state *accountCheck) useCredentialRevision(revision int64) {
	if state.credentialRevision == revision {
		return
	}
	state.credentialRevision = revision
	state.client = nil
	state.recoveryPending = false
	state.rejectedReconnectWatch = nil
	state.nextProbeFilter = 0
}

// applyRetryAfter is called with the runner mutex held.
func (state *accountCheck) applyRetryAfter(now time.Time, err error) {
	if delay := luxmed.RetryAfter(err); delay > 0 {
		state.retryAfter = maxTime(state.retryAfter, now.Add(delay))
	}
}

// searchReady is called with the runner mutex held.
func (state *accountCheck) searchReady(now time.Time) bool {
	return !now.Before(state.next) && !now.Before(state.retryAfter)
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
			ids, err := s.db.ActiveBrowserWatchTelegramIDs(ctx)
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
	assigned := make([]int64, 0, len(s.runner.browsers))
	for id, browser := range s.runner.browsers {
		if browser != nil {
			assigned = append(assigned, id)
		}
	}
	ids, err := s.db.ReconnectWatchTelegramIDs(ctx, assigned)
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
		if account.inFlight == nil && !account.recoveryPending && account.searchReady(now) && now.Sub(account.lastCheck) > 15*time.Minute {
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
		s.runner.mu.Lock()
		account := s.runner.accounts[watch.UserID]
		blocked := account != nil && (account.inFlight != nil || !account.searchReady(now) || account.hasRejectedReconnectFilters(watch))
		s.runner.mu.Unlock()
		if blocked {
			continue
		}
		accounts[watch.UserID] = true
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
