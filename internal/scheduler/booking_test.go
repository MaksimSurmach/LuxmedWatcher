package scheduler

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/maksimsurmach/luxmed-watcher/internal/domain"
	"github.com/maksimsurmach/luxmed-watcher/internal/luxmed"
)

type bookingTestClient struct {
	fakeLuxMed
	book func(domain.Appointment) (string, error)
}

func (c *bookingTestClient) BookAppointment(_ context.Context, slot domain.Appointment) (string, error) {
	return c.book(slot)
}

func TestAutoBooksFirstSlotThatCanBeConfirmed(t *testing.T) {
	runner, db, watch := testRunner(t, nil)
	ctx := t.Context()
	watch.NextDays, watch.AutoBook = 14, true
	if err := db.UpdateWatch(ctx, watch); err != nil {
		t.Fatal(err)
	}

	now := time.Now()
	early := domain.Appointment{Fingerprint: "early", BookingDetails: []byte("{}"), DateTime: now.Add(28 * time.Hour)}
	middle := domain.Appointment{Fingerprint: "middle", BookingDetails: []byte("{}"), DateTime: now.Add(29 * time.Hour)}
	late := domain.Appointment{Fingerprint: "late", BookingDetails: []byte("{}"), DateTime: now.Add(30 * time.Hour)}
	var tried []string
	runner.newClient = func(bool) luxmed.Client {
		return &bookingTestClient{
			fakeLuxMed: fakeLuxMed{search: func(context.Context) ([]domain.Appointment, error) {
				return []domain.Appointment{late, middle, early}, nil
			}},
			book: func(slot domain.Appointment) (string, error) {
				tried = append(tried, slot.Fingerprint)
				if slot.Fingerprint == "early" {
					return "", luxmed.ErrSlotTaken
				}
				return "provider-confirmed", nil
			},
		}
	}
	if err := runner.CheckWatch(ctx, watch); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(tried, []string{"early", "middle"}) {
		t.Fatalf("booking order = %v", tried)
	}
	watch, err := db.Watch(ctx, watch.ID)
	if err != nil || watch.Status != domain.WatchStatusPaused || watch.AutoBook {
		t.Fatalf("watch after booking = %+v, %v", watch, err)
	}
}

func TestManualBookingIsIdempotentAndKeepsWatchRunning(t *testing.T) {
	runner, db, watch := testRunner(t, nil)
	ctx := t.Context()
	app := domain.Appointment{Fingerprint: "manual", BookingDetails: []byte("{}"), DateTime: time.Now().Add(26 * time.Hour)}
	slot, _, err := db.UpsertAppointmentHistory(ctx, watch.ID, app)
	if err != nil {
		t.Fatal(err)
	}
	user, err := db.UserByID(ctx, watch.UserID)
	if err != nil {
		t.Fatal(err)
	}
	var calls int
	runner.newClient = func(bool) luxmed.Client {
		return &bookingTestClient{
			fakeLuxMed: fakeLuxMed{search: func(context.Context) ([]domain.Appointment, error) {
				return []domain.Appointment{app}, nil
			}},
			book: func(domain.Appointment) (string, error) {
				calls++
				return "provider-confirmed", nil
			},
		}
	}
	for range 2 {
		visit, err := runner.BookSlot(ctx, user, slot.ID)
		if err != nil || visit.Status != "confirmed" || visit.Reminders || visit.Automatic {
			t.Fatalf("manual visit = %+v, %v", visit, err)
		}
	}
	watch, err = db.Watch(ctx, watch.ID)
	if err != nil || watch.Status != domain.WatchStatusActive || calls != 1 {
		t.Fatalf("watch status %s, booking calls %d, err %v", watch.Status, calls, err)
	}
}
