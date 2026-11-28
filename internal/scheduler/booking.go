package scheduler

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"time"

	"github.com/maksimsurmach/luxmed-watcher/internal/domain"
	"github.com/maksimsurmach/luxmed-watcher/internal/luxmed"
)

var errAutoBooked = errors.New("watch completed by automatic booking")

type appointmentBooker interface {
	BookAppointment(context.Context, domain.Appointment) (string, error)
}

func (r *Runner) BookSlot(ctx context.Context, user domain.User, slotID int64) (domain.Booking, error) {
	var state *accountCheck
	for {
		var done <-chan struct{}
		state, done = r.acquireAccount(user.ID, false)
		if state != nil {
			break
		}
		select {
		case <-done:
		case <-ctx.Done():
			return domain.Booking{}, luxmed.ErrBrowserBusy
		}
	}
	defer r.releaseAccount(state)
	previous, err := r.db.BookingBySlot(ctx, user.ID, slotID)
	if err == nil {
		return previous, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return domain.Booking{}, err
	}
	select {
	case r.capacity <- struct{}{}:
		defer func() { <-r.capacity }()
	case <-ctx.Done():
		return domain.Booking{}, ctx.Err()
	}

	items, err := r.db.HistoryByIDs(ctx, user.ID, time.Now(), []int64{slotID})
	if err != nil {
		return domain.Booking{}, err
	}
	if len(items) != 1 {
		return domain.Booking{}, luxmed.ErrSlotTaken
	}
	slot := items[0]
	watch, err := r.db.Watch(ctx, slot.WatchID)
	if err != nil {
		return domain.Booking{}, err
	}
	state.useCredentialRevision(user.CredentialRevision)
	client, _, err := r.searchClient(ctx, user, state)
	if err != nil {
		return domain.Booking{}, err
	}

	// Confirm the original slot against a fresh response for its date and clinic.
	day := slot.DateTime.In(luxmed.Location())
	search := watch
	search.DateFrom, search.DateTo = &day, &day
	if slot.FacilityID > 0 {
		search.FacilityIDs = []int{slot.FacilityID}
	}
	if slot.DoctorID > 0 {
		search.DoctorID = &slot.DoctorID
	}
	result, err := client.SearchAppointments(ctx, search)
	if err != nil && !errors.Is(err, luxmed.ErrPartialResults) {
		return domain.Booking{}, err
	}
	for _, fresh := range result.Appointments {
		if fresh.Fingerprint == slot.Fingerprint || fresh.LegacyFingerprint == slot.Fingerprint &&
			domain.NormalizeAppointmentAddress(fresh.Address) == domain.NormalizeAppointmentAddress(slot.Address) {
			return r.bookSlot(ctx, user, watch, slot, fresh, client, false)
		}
	}
	if err != nil {
		return domain.Booking{}, err
	}
	return domain.Booking{}, luxmed.ErrSlotTaken
}

func (r *Runner) bookSlot(ctx context.Context, user domain.User, watch domain.Watch, slot domain.AppointmentHistory, fresh domain.Appointment, client luxmed.Client, automatic bool) (domain.Booking, error) {
	booker, ok := client.(appointmentBooker)
	if !ok || len(fresh.BookingDetails) == 0 {
		return domain.Booking{}, luxmed.ErrBookingInPortal
	}
	slot.Fingerprint = fresh.Fingerprint
	booking, err := r.db.ClaimBooking(ctx, user, watch, slot, automatic)
	if errors.Is(err, sql.ErrNoRows) {
		return r.db.BookingByFingerprint(ctx, user.ID, slot.Fingerprint)
	}
	if err != nil {
		return domain.Booking{}, err
	}

	id, bookErr := booker.BookAppointment(ctx, fresh)
	record, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	switch {
	case errors.Is(bookErr, luxmed.ErrBookingUnknown) || bookErr == nil:
		if err := r.db.FinishBooking(record, booking, id, time.Now().UTC()); err != nil {
			return domain.Booking{}, errors.Join(luxmed.ErrBookingUnknown, err)
		}
		if id == "" {
			booking.Status = "unknown"
		} else {
			booking.Status, booking.Reminders = "confirmed", automatic
		}
		return booking, nil
	default:
		return domain.Booking{}, errors.Join(bookErr, r.db.RejectBooking(record, booking.ID, errors.Is(bookErr, luxmed.ErrSlotTaken)))
	}
}

func (r *Runner) autoBook(ctx context.Context, user domain.User, watch domain.Watch, slots []domain.Appointment, client luxmed.Client) error {
	if !watch.AutoBook || len(slots) == 0 {
		return nil
	}
	if held, err := r.db.AutoBookingHeld(ctx, watch.ID); err != nil || held {
		return err
	}
	for _, fresh := range slots {
		if len(fresh.BookingDetails) == 0 || !fresh.DateTime.After(time.Now()) {
			continue
		}
		slot, err := r.db.BookableWatchSlot(ctx, watch.ID, fresh, time.Now())
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return err
		}
		booking, err := r.bookSlot(ctx, user, watch, slot, fresh, client, true)
		if errors.Is(err, luxmed.ErrSlotTaken) || errors.Is(err, luxmed.ErrBookingInPortal) {
			continue
		}
		if err != nil {
			return err
		}
		if booking.Status == "confirmed" && booking.Automatic && booking.WatchID == watch.ID {
			return errAutoBooked
		}
		if booking.Status != "confirmed" {
			break
		}
	}
	return nil
}

type bookingNotifier interface {
	NotifyBooking(context.Context, domain.User, domain.Booking, string) error
}

func (s *Scheduler) deliverBookingMessages(ctx context.Context) {
	notifier, ok := s.runner.notifier.(bookingNotifier)
	if !ok {
		return
	}
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
	for {
		for range 10 {
			if err := s.deliverBookingMessage(ctx, notifier); err != nil {
				if !errors.Is(err, sql.ErrNoRows) && ctx.Err() == nil {
					s.logger.Warn("deliver booking message", slog.Any("error", err))
				}
				break
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

func (s *Scheduler) deliverBookingMessage(ctx context.Context, notifier bookingNotifier) error {
	ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	m, err := s.db.ClaimBookingMessage(ctx, time.Now())
	if err != nil {
		return err
	}
	user, err := s.db.UserByID(ctx, m.Booking.UserID)
	if err != nil {
		return err
	}
	if user.IsActive() && user.TelegramChatID > 0 && user.TelegramChatID == user.TelegramUserID {
		if err := notifier.NotifyBooking(ctx, user, m.Booking, m.Kind); err != nil {
			return err
		}
	}
	record, stop := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer stop()
	return s.db.MarkBookingMessageSent(record, m)
}
