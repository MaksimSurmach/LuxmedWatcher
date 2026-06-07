package scheduler

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/maksimsurmach/luxmed-watcher/internal/domain"
	"github.com/maksimsurmach/luxmed-watcher/internal/luxmed"
	"github.com/maksimsurmach/luxmed-watcher/internal/security"
	"github.com/maksimsurmach/luxmed-watcher/internal/storage"
)

type Notifier interface {
	NotifyAppointment(ctx context.Context, user domain.User, watch domain.Watch, history domain.AppointmentHistory) (int, error)
}

type Runner struct {
	db                   *storage.DB
	cryptor              *security.Cryptor
	notifier             Notifier
	newClient            func(rawPayloads bool) luxmed.Client
	notificationCooldown time.Duration
	rawPayloads          bool
	logger               *slog.Logger
}

func NewRunner(db *storage.DB, cryptor *security.Cryptor, notifier Notifier, cooldown time.Duration, rawPayloads bool, logger *slog.Logger) *Runner {
	return &Runner{
		db:                   db,
		cryptor:              cryptor,
		notifier:             notifier,
		newClient:            func(rawPayloads bool) luxmed.Client { return luxmed.NewHTTPClient(rawPayloads) },
		notificationCooldown: cooldown,
		rawPayloads:          rawPayloads,
		logger:               logger,
	}
}

func (r *Runner) CheckWatch(ctx context.Context, watch domain.Watch) error {
	user, err := r.db.UserByTelegramID(ctx, 0)
	if err == nil {
		_ = user
	}
	account, err := r.db.LuxMedAccount(ctx, watch.UserID)
	if err != nil {
		_ = r.db.MarkWatchChecked(ctx, watch.ID, false, "luxmed account not configured")
		return fmt.Errorf("load luxmed account: %w", err)
	}
	password, err := r.cryptor.Decrypt(account.EncryptedPassword)
	if err != nil {
		_ = r.db.MarkWatchChecked(ctx, watch.ID, false, "decrypt credentials failed")
		return err
	}
	client := r.newClient(r.rawPayloads)
	if err := client.Authenticate(ctx, luxmed.Credentials{Login: account.Login, Password: password}); err != nil {
		account.Status = domain.AccountStatusAuthFailed
		account.LastLoginError = err.Error()
		_ = r.db.UpsertLuxMedAccount(ctx, account)
		_ = r.db.MarkWatchChecked(ctx, watch.ID, false, err.Error())
		return err
	}
	now := time.Now().UTC()
	account.Status = domain.AccountStatusActive
	account.LastLoginAt = &now
	account.LastLoginError = ""
	_ = r.db.UpsertLuxMedAccount(ctx, account)

	apps, err := client.SearchAppointments(ctx, watch)
	if err != nil {
		_ = r.db.MarkWatchChecked(ctx, watch.ID, false, err.Error())
		return err
	}

	apps = filterAppointmentsByTimeWindows(apps, watch.TimeWindows)

	user, err = r.userByID(ctx, watch.UserID)
	if err != nil {
		_ = r.db.MarkWatchChecked(ctx, watch.ID, false, err.Error())
		return err
	}
	for _, app := range apps {
		history, isNew, err := r.db.UpsertAppointmentHistory(ctx, watch.ID, app)
		if err != nil {
			r.logger.Warn("store appointment history failed", "watch_id", watch.ID, "err", err)
			continue
		}
		if !isNew && !r.cooldownExpired(ctx, watch.ID, history.Fingerprint) {
			continue
		}
		messageID, notifyErr := r.notifier.NotifyAppointment(ctx, user, watch, history)
		status := domain.NotificationStatusSent
		errText := ""
		if notifyErr != nil {
			status = domain.NotificationStatusFailed
			errText = notifyErr.Error()
		}
		if err := r.db.MarkNotified(ctx, watch.ID, history.ID, history.Fingerprint, messageID, status, errText); err != nil {
			r.logger.Warn("store notification history failed", "watch_id", watch.ID, "err", err)
		}
	}
	return r.db.MarkWatchChecked(ctx, watch.ID, true, "")
}

func (r *Runner) cooldownExpired(ctx context.Context, watchID int64, fingerprint string) bool {
	last, err := r.db.LastNotificationAt(ctx, watchID, fingerprint)
	if err != nil || last == nil {
		return true
	}
	return time.Since(*last) >= r.notificationCooldown
}

func (r *Runner) userByID(ctx context.Context, userID int64) (domain.User, error) {
	return r.db.UserByID(ctx, userID)
}

type Scheduler struct {
	db       *storage.DB
	runner   *Runner
	interval time.Duration
	logger   *slog.Logger
}

func New(db *storage.DB, runner *Runner, interval time.Duration, logger *slog.Logger) *Scheduler {
	return &Scheduler{db: db, runner: runner, interval: interval, logger: logger}
}

func (s *Scheduler) Run(ctx context.Context) {
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for {
		s.tick(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *Scheduler) tick(ctx context.Context) {
	watches, err := s.db.DueWatches(ctx, 20)
	if err != nil {
		s.logger.Warn("load due watches failed", "err", err)
		return
	}
	for _, watch := range watches {
		if err := s.runner.CheckWatch(ctx, watch); err != nil {
			s.logger.Warn("watch check failed", "watch_id", watch.ID, "err", err)
		}
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
	appMinutes := app.DateTime.Hour()*60 + app.DateTime.Minute()
	appWeekday := int(app.DateTime.Weekday())

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
