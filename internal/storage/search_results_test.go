package storage

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/maksimsurmach/luxmed-watcher/internal/domain"
	"github.com/maksimsurmach/luxmed-watcher/internal/security"
)

func TestSearchWritesRequireCurrentCredentialRevision(t *testing.T) {
	testStores(t, func(t *testing.T, ctx context.Context, db *DB) {
		must := func(err error) {
			t.Helper()
			if err != nil {
				t.Fatal(err)
			}
		}
		user, err := db.UpsertTelegramUser(ctx, domain.User{
			TelegramUserID: 42, Status: domain.UserStatusActive, Role: domain.UserRoleUser,
		})
		must(err)
		key, err := security.NewCryptor(make([]byte, 32))
		must(err)
		_, err = db.SaveCredentials(ctx, key, user, security.Credentials{Login: "fixture", Password: "synthetic"})
		must(err)
		user, err = db.UserByID(ctx, user.ID)
		must(err)
		watchID, err := db.CreateWatch(ctx, domain.Watch{UserID: user.ID, CityID: 1, ServiceID: 2})
		must(err)
		watch, err := db.Watch(ctx, watchID)
		must(err)
		must(db.BeginLuxMedReconnectForMode(ctx, user))
		episode, err := db.LuxMedReconnectEpisode(ctx, user.ID)
		must(err)
		must(db.ForgetCredentials(ctx, user))

		now := time.Now()
		update := SearchHistoryUpdate{
			WatchID: watchID, Complete: true, StartedAt: now,
			Appointments: []domain.Appointment{{Fingerprint: "fixture", DateTime: now.Add(time.Hour)}},
		}
		_, historyErr := db.SaveSearchHistory(ctx, user, update)
		claim, err := db.ClaimLuxMedReconnectDeliveryForMode(ctx, user, now)
		must(err)
		if claim.Attempt != 0 {
			t.Fatal("old mode reserved a reconnect warning")
		}
		for _, err := range []error{
			historyErr,
			db.ResolveLuxMedReconnectForMode(ctx, user, true),
			db.BeginLuxMedReconnectForMode(ctx, user),
			db.SetLuxMedAccessForMode(ctx, user, LuxMedAccessRejected, true),
			db.MarkWatchCheckedForMode(ctx, user, watch, true, ""),
		} {
			if !errors.Is(err, ErrCredentialsChanged) {
				t.Fatalf("old mode wrote search state: %v", err)
			}
		}
		watches, access, err := db.WatchesWithReconnectState(ctx, user.ID)
		must(err)
		if access.Status != LuxMedAccessPending || access.EpisodeID != episode || watches[0].LastCheckedAt != nil {
			t.Fatalf("old mode altered current search state: %+v, %+v", watches, access)
		}

		user, err = db.UserByID(ctx, user.ID)
		must(err)
		pending, err := db.SaveSearchHistory(ctx, user, update)
		must(err)
		if len(pending) != 1 || pending[0].SeenCount != 1 {
			t.Fatalf("old result was persisted or current result was lost: %+v", pending)
		}
		must(db.ResolveLuxMedReconnectForMode(ctx, user, true))
		must(db.MarkWatchCheckedForMode(ctx, user, watch, true, ""))
		_, access, err = db.WatchesWithReconnectState(ctx, user.ID)
		must(err)
		if access.Status != LuxMedAccessConnected || access.EpisodeID != "" {
			t.Fatalf("current mode did not restore access: %+v", access)
		}
	})
}

func TestPartialSearchWithdrawsOnlyVerifiedDays(t *testing.T) {
	testStores(t, func(t *testing.T, ctx context.Context, db *DB) {
		must := func(err error) {
			t.Helper()
			if err != nil {
				t.Fatal(err)
			}
		}
		user, err := db.UpsertTelegramUser(ctx, domain.User{TelegramUserID: 10, Status: domain.UserStatusActive})
		must(err)
		watch, err := db.CreateWatch(ctx, domain.Watch{UserID: user.ID, CityID: 1, ServiceID: 2})
		must(err)
		other, err := db.CreateWatch(ctx, domain.Watch{UserID: user.ID, CityID: 1, ServiceID: 2})
		must(err)
		// On the short Warsaw day the next midnight is 23 hours later.
		zone, err := time.LoadLocation("Europe/Warsaw")
		must(err)
		day := time.Date(2030, 3, 31, 0, 0, 0, 0, zone)
		put := func(watch int64, key string, when time.Time) domain.AppointmentHistory {
			t.Helper()
			row, _, err := db.UpsertAppointmentHistory(ctx, watch, domain.Appointment{Fingerprint: key, DateTime: when})
			must(err)
			return row
		}
		adjacent := put(watch, "tomorrow", day.AddDate(0, 0, 1))
		duplicate := put(other, "tomorrow", day.AddDate(0, 0, 1))
		gone := put(watch, "gone", day.Add(time.Hour))
		start := time.Now().Add(time.Second)
		_, err = db.SaveSearchHistory(ctx, user, SearchHistoryUpdate{WatchID: watch, StartedAt: start, RenotifyAt: start.Add(-time.Hour), CompleteDays: []time.Time{day}})
		must(err)
		if _, err := db.AppointmentHistoryByID(ctx, gone.ID); !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("withdrawn row remains: %v", err)
		}
		for _, row := range []domain.AppointmentHistory{adjacent, duplicate} {
			_, err := db.AppointmentHistoryByID(ctx, row.ID)
			must(err)
		}
		index, err := db.AvailableHistoryIndex(ctx, user.ID, day)
		must(err)
		if len(index) != 1 || index[0].ID != adjacent.ID {
			t.Fatalf("unverified day or duplicate: %+v", index)
		}
		if next := put(watch, "replacement", day.Add(2*time.Hour)); next.ID <= gone.ID {
			t.Fatal("stale slot ID reused")
		}
		must(db.SetUserWatchStatus(ctx, user.ID, watch, domain.WatchStatusPaused))
		must(db.ResumeWatchForMode(ctx, user, watch))
		rows, err := db.HistoryByIDs(ctx, user.ID, day, []int64{adjacent.ID, duplicate.ID})
		must(err)
		if len(rows) != 1 || rows[0].ID != duplicate.ID {
			t.Fatalf("unverified slots returned after resume: %+v", rows)
		}
	})
}
