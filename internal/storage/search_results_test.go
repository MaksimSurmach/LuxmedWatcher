package storage

import (
	"context"
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
		must(db.ForgetCredentials(ctx, user.ID))

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
