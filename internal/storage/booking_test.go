package storage

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/maksimsurmach/luxmed-watcher/internal/domain"
)

func TestBookingPersistenceAndReminders(t *testing.T) {
	testStores(t, func(t *testing.T, ctx context.Context, db *DB) {
		now := time.Now().UTC().Truncate(time.Second)
		user, err := db.UpsertTelegramUser(ctx, domain.User{
			TelegramUserID: 72, TelegramChatID: 72, Locale: "en", Status: domain.UserStatusActive, Role: domain.UserRoleUser,
		})
		if err != nil {
			t.Fatal(err)
		}
		id, err := db.CreateWatch(ctx, domain.Watch{UserID: user.ID, AutoBook: true})
		if err != nil {
			t.Fatal(err)
		}
		watch, err := db.Watch(ctx, id)
		if err != nil || !watch.AutoBook {
			t.Fatalf("autobook = %v, %v", watch.AutoBook, err)
		}
		slot, _, err := db.UpsertAppointmentHistory(ctx, id, domain.Appointment{
			Fingerprint: "same-across-watches", DateTime: now.Add(30 * time.Hour), DoctorName: "Doctor", Address: "Street",
		})
		if err != nil {
			t.Fatal(err)
		}
		booked, err := db.ClaimBooking(ctx, user, watch, slot, true)
		if err != nil || booked.Status != "pending" {
			t.Fatalf("claim = %+v, %v", booked, err)
		}
		if _, err = db.ClaimBooking(ctx, user, watch, slot, true); !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("duplicate claim = %v", err)
		}
		if err = db.FinishBooking(ctx, booked, "confirmed-by-provider", now); err != nil {
			t.Fatal(err)
		}
		after, err := db.Watch(ctx, id)
		if err != nil || after.Status != domain.WatchStatusPaused || after.AutoBook {
			t.Fatalf("watch after booking = %+v, %v", after, err)
		}
		visits, err := db.UserBookings(ctx, user.ID, now)
		if err != nil || len(visits) != 1 || !visits[0].Reminders || visits[0].Status != "confirmed" {
			t.Fatalf("visits = %+v, %v", visits, err)
		}
		next := func(at time.Time, kind string) {
			t.Helper()
			message, err := db.ClaimBookingMessage(ctx, at)
			if err != nil || message.Kind != kind {
				t.Fatalf("message at %s = %+v, %v, want %s", at, message, err, kind)
			}
			if err := db.MarkBookingMessageSent(ctx, message); err != nil {
				t.Fatal(err)
			}
		}
		nothingDue := func(at time.Time) {
			t.Helper()
			if msg, err := db.ClaimBookingMessage(ctx, at); !errors.Is(err, sql.ErrNoRows) {
				t.Fatalf("unexpected due message %+v, err %v", msg, err)
			}
		}

		next(now, "confirmed")
		nothingDue(now.Add(6*time.Hour - time.Second))
		next(now.Add(6*time.Hour), "day")
		if _, err = db.SetBookingReminders(ctx, user.ID+1, booked.ID, false, now); !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("cross-user reminder update = %v", err)
		}
		if _, err = db.SetBookingReminders(ctx, user.ID, booked.ID, false, now.Add(7*time.Hour)); err != nil {
			t.Fatal(err)
		}
		nothingDue(now.Add(28 * time.Hour))
		if _, err = db.SetBookingReminders(ctx, user.ID, booked.ID, true, now.Add(7*time.Hour)); err != nil {
			t.Fatal(err)
		}
		nothingDue(now.Add(7 * time.Hour))
		next(now.Add(28*time.Hour), "two_hours")
		nothingDue(now.Add(29 * time.Hour))
	})
}
