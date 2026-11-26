package storage

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/maksimsurmach/luxmed-watcher/internal/domain"
	"github.com/maksimsurmach/luxmed-watcher/internal/luxmed"
)

func TestInviteRedeemAndWatchHistory(t *testing.T) {
	testStores(t, testInviteRedeemAndWatchHistory)
}

func testInviteRedeemAndWatchHistory(t *testing.T, ctx context.Context, db *DB) {
	user, err := db.UpsertTelegramUser(ctx, domain.User{
		TelegramUserID: 11,
		TelegramChatID: 22,
		Locale:         "en",
		Status:         domain.UserStatusPendingInvite,
		Role:           domain.UserRoleUser,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.CreateInvite(ctx, "secret", user.ID, 1, nil); err != nil {
		t.Fatal(err)
	}
	ok, err := db.RedeemInvite(ctx, "secret")
	if err != nil || !ok {
		t.Fatalf("redeem invite = %v, %v", ok, err)
	}
	ok, err = db.RedeemInvite(ctx, "secret")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("single-use invite redeemed twice")
	}

	watchID, err := db.CreateWatch(ctx, domain.Watch{
		UserID:               user.ID,
		Name:                 "Cardiology - Warsaw",
		CityID:               1,
		CityName:             "Warsaw",
		ServiceID:            2,
		ServiceName:          "Cardiology",
		DoctorMode:           domain.DoctorModeAny,
		FacilityMode:         domain.FacilityModeAll,
		NextDays:             14,
		CheckIntervalSeconds: 120,
	})
	if err != nil {
		t.Fatal(err)
	}
	history, isNew, err := db.UpsertAppointmentHistory(ctx, watchID, domain.Appointment{
		Fingerprint:  "abc",
		DateTime:     time.Date(2026, 6, 12, 14, 30, 0, 0, time.UTC),
		ServiceID:    2,
		ServiceName:  "Cardiology",
		DoctorID:     3,
		DoctorName:   "Dr Test",
		FacilityID:   4,
		FacilityName: "LuxMed",
		CityID:       1,
		CityName:     "Warsaw",
	})
	if err != nil || !isNew {
		t.Fatalf("first history upsert = new %v err %v", isNew, err)
	}
	history, isNew, err = db.UpsertAppointmentHistory(ctx, watchID, history.Appointment)
	if err != nil {
		t.Fatal(err)
	}
	if isNew || history.SeenCount != 2 {
		t.Fatalf("second history upsert = new %v seen %d", isNew, history.SeenCount)
	}
}

func TestCitySearchAndPagination(t *testing.T) {
	testStores(t, testCitySearchAndPagination)
}

func testCitySearchAndPagination(t *testing.T, ctx context.Context, db *DB) {
	err := db.UpsertCities(ctx, []domain.City{
		{ID: 1, Name: "Białystok"},
		{ID: 2, Name: "Łódź"},
		{ID: 3, Name: "Warszawa"},
		{ID: 4, Name: "Wrocław"},
	})
	if err != nil {
		t.Fatal(err)
	}

	page, err := db.CitiesPage(ctx, 2, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(page) != 2 {
		t.Fatalf("expected first page of 2 cities, got %d", len(page))
	}

	results, err := db.SearchCities(ctx, "lodz", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Name != "Łódź" {
		t.Fatalf("expected accent-insensitive match for Łódź, got %#v", results)
	}

	results, err = db.SearchCities(ctx, "wroclaw", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Name != "Wrocław" {
		t.Fatalf("expected accent-insensitive match for Wrocław, got %#v", results)
	}

	city := results[0]
	procedure := domain.Procedure{ID: 1, Name: "Kardiolog", IsRecent: true}
	if err := db.UpsertProcedures(ctx, city, []domain.Procedure{procedure}); err != nil {
		t.Fatal(err)
	}
	procedure.IsRecent = false
	if err := db.UpsertProcedures(ctx, city, []domain.Procedure{procedure}); err != nil {
		t.Fatal(err)
	}
	procedures, err := db.RecentProcedures(ctx, city.ID, 5)
	if err != nil || len(procedures) != 1 || !procedures[0].IsRecent {
		t.Fatalf("recent procedures = %+v, %v", procedures, err)
	}
	procedures, err = db.SearchProceduresPage(ctx, city.ID, "KARD", 1, 0)
	if err != nil || len(procedures) != 1 || procedures[0].ID != procedure.ID {
		t.Fatalf("procedure search = %+v, %v", procedures, err)
	}
	procedures, err = db.ProceduresByLetter(ctx, city.ID, "K", 1, 0)
	if err != nil || len(procedures) != 1 || procedures[0].ID != procedure.ID {
		t.Fatalf("procedures by letter = %+v, %v", procedures, err)
	}
	facility := domain.Facility{ID: 9, Name: "Clinic", Address: "Test address"}
	if err := db.UpsertFacilities(ctx, city, procedure.ID, []domain.Facility{facility}); err != nil {
		t.Fatal(err)
	}
	facilities, err := db.FacilitiesPage(ctx, city.ID, procedure.ID, 1, 0)
	if err != nil || len(facilities) != 1 || facilities[0] != facility {
		t.Fatalf("facilities = %+v, %v", facilities, err)
	}
	user, err := db.UpsertTelegramUser(ctx, domain.User{TelegramUserID: 100})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.SaveFavoriteFacility(ctx, user.ID, city.ID, procedure.ID, facility); err != nil {
		t.Fatal(err)
	}
	favorites, err := db.FavoriteFacilitiesPage(ctx, user.ID, city.ID, procedure.ID, 1, 0)
	if err != nil || len(favorites) != 1 || favorites[0] != facility {
		t.Fatalf("favorite facilities = %+v, %v", favorites, err)
	}
}

func testStores(t *testing.T, test func(*testing.T, context.Context, *DB)) {
	t.Helper()
	t.Run("SQLite", func(t *testing.T) {
		ctx := context.Background()
		db, err := Open(ctx, filepath.Join(t.TempDir(), "test.sqlite"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { db.Close() })
		test(t, ctx, db)
	})
	t.Run("PostgreSQL", func(t *testing.T) {
		dsn := os.Getenv("TEST_DATABASE_URL")
		if dsn == "" {
			t.Skip("TEST_DATABASE_URL is unset")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		t.Cleanup(cancel)
		admin, err := sql.Open("postgres", dsn)
		if err != nil {
			t.Fatal("invalid TEST_DATABASE_URL")
		}
		t.Cleanup(func() { admin.Close() })
		var suffix [8]byte
		if _, err := rand.Read(suffix[:]); err != nil {
			t.Fatal(err)
		}
		schema := "luxmed_test_" + hex.EncodeToString(suffix[:])
		if _, err := admin.ExecContext(ctx, `CREATE SCHEMA `+schema); err != nil {
			t.Fatal("could not create PostgreSQL test schema")
		}
		t.Cleanup(func() {
			if _, err := admin.ExecContext(ctx, `DROP SCHEMA `+schema+` CASCADE`); err != nil {
				t.Errorf("remove PostgreSQL test schema %s: %v", schema, err)
			}
		})
		u, err := url.Parse(dsn)
		if err != nil {
			t.Fatal("invalid TEST_DATABASE_URL")
		}
		query := u.Query()
		query.Set("search_path", schema)
		u.RawQuery = query.Encode()
		check, err := sql.Open("postgres", u.String())
		if err != nil {
			t.Fatal("invalid PostgreSQL test schema connection")
		}
		var current string
		err = check.QueryRowContext(ctx, `SELECT current_schema()`).Scan(&current)
		check.Close()
		if err != nil || current != schema {
			t.Fatal("could not isolate PostgreSQL test schema")
		}
		db, err := OpenPostgres(ctx, u.String())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { db.Close() })
		test(t, ctx, db)
	})
}

func TestDueHistoryAndAccount(t *testing.T) {
	testStores(t, func(t *testing.T, ctx context.Context, db *DB) {
		must := func(err error) {
			t.Helper()
			if err != nil {
				t.Fatal(err)
			}
		}
		must(db.Migrate(ctx))
		user, err := db.UpsertTelegramUser(ctx, domain.User{TelegramUserID: 4_294_967_296, TelegramChatID: 4_294_967_297, Status: domain.UserStatusActive})
		must(err)
		watch := domain.Watch{UserID: user.ID, Name: "Original watch", CheckIntervalSeconds: 60, NextDays: 14}
		watch.ID, err = db.CreateWatch(ctx, watch)
		must(err)
		due, err := db.DueWatches(ctx, 5)
		must(err)
		if len(due) != 1 || due[0].ID != watch.ID {
			t.Fatalf("initial due watches = %+v", due)
		}
		must(db.MarkWatchChecked(ctx, watch.ID, true, ""))
		due, err = db.DueWatches(ctx, 5)
		must(err)
		if len(due) != 0 {
			t.Fatalf("watch unexpectedly due before its interval: %+v", due)
		}
		owners, err := db.ActiveWatchTelegramIDs(ctx)
		must(err)
		if len(owners) != 1 || owners[0] != user.TelegramUserID {
			t.Fatalf("active watch owners between searches = %v", owners)
		}
		past := time.Now().UTC().Add(-2 * time.Minute)
		_, err = db.sql.ExecContext(ctx, `UPDATE watches SET last_checked_at=$1 WHERE id=$2`, past, watch.ID)
		must(err)
		due, err = db.DueWatches(ctx, 5)
		must(err)
		if len(due) != 1 || due[0].ID != watch.ID {
			t.Fatalf("watch not due after its interval: %+v", due)
		}
		watch.Name = "Updated watch"
		watch.UserID++
		if err := db.UpdateWatch(ctx, watch); !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("update another user's watch: %v", err)
		}
		watch.UserID--
		must(db.UpdateWatch(ctx, watch))
		updated, err := db.Watch(ctx, watch.ID)
		must(err)
		if updated.Name != watch.Name || updated.LastCheckedAt != nil {
			t.Fatalf("watch not updated: %+v", updated)
		}

		old := time.Now().UTC().AddDate(0, 0, -14)
		history, _, err := db.UpsertAppointmentHistory(ctx, watch.ID, domain.Appointment{Fingerprint: "old", DateTime: old})
		must(err)
		must(db.MarkNotifications(ctx, []domain.AppointmentHistory{history}, 1, domain.NotificationStatusFailed, "network"))
		last, err := db.LastNotificationAt(ctx, watch.ID, history.Fingerprint)
		must(err)
		if last != nil {
			t.Fatal("failed notification counted as sent")
		}
		must(db.MarkNotifications(ctx, []domain.AppointmentHistory{history}, 2, domain.NotificationStatusSent, ""))
		last, err = db.LastNotificationAt(ctx, watch.ID, history.Fingerprint)
		must(err)
		if last == nil || time.Since(*last) > time.Minute {
			t.Fatalf("last notification = %v", last)
		}
		recent, err := db.WatchHistory(ctx, user.ID, watch.ID, 10, 0)
		must(err)
		if len(recent) != 1 || recent[0].LastNotifiedAt == nil {
			t.Fatalf("watch history = %+v", recent)
		}
		if err := db.HideAppointmentHistory(ctx, user.ID+1, history.ID); !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("hide another user's appointment: %v", err)
		}
		must(db.HideAppointmentHistory(ctx, user.ID, history.ID))
		history, _, err = db.UpsertAppointmentHistory(ctx, watch.ID, history.Appointment)
		must(err)
		if history.Status != domain.HistoryStatusHidden {
			t.Fatal("recheck restored a hidden appointment")
		}
		cutoff := time.Now().UTC().AddDate(0, 0, -7)
		_, err = db.sql.ExecContext(ctx, `UPDATE appointment_history SET last_seen_at=$1 WHERE id=$2`, old, history.ID)
		must(err)
		must(db.PurgeHistory(ctx, cutoff))
		_, err = db.AppointmentHistoryByID(ctx, history.ID)
		must(err) // Recent deliveries retain the associated appointment and cooldown.
		_, err = db.sql.ExecContext(ctx, `UPDATE notification_history SET sent_at=$1 WHERE appointment_history_id=$2`, old, history.ID)
		must(err)
		must(db.PurgeHistory(ctx, cutoff))
		if _, err := db.AppointmentHistoryByID(ctx, history.ID); !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("old appointment was retained: %v", err)
		}

		account := domain.LuxMedAccount{UserID: user.ID, Login: "fixture", EncryptedPassword: "cipher", Status: domain.AccountStatusActive}
		must(db.SaveVerifiedLuxMedAccount(ctx, account))
		stale := account
		stale.EncryptedPassword = "previous-cipher"
		if err := db.UpdateLuxMedAccountAuth(ctx, stale); !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("stale auth update = %v", err)
		}
		must(db.UpdateLuxMedAccountAuth(ctx, account))
		must(db.DeleteLuxMedAccount(ctx, user.ID))
		if _, err := db.LuxMedAccount(ctx, user.ID); !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("account was not deleted: %v", err)
		}
		updated, err = db.Watch(ctx, watch.ID)
		must(err)
		if updated.Status != domain.WatchStatusPaused {
			t.Fatalf("watch status after account deletion = %v", updated.Status)
		}
		owners, err = db.ActiveWatchTelegramIDs(ctx)
		must(err)
		if len(owners) != 0 {
			t.Fatalf("paused watch owners eligible for renewal = %v", owners)
		}
	})
}

func TestAppointmentDisappearanceAndReappearance(t *testing.T) {
	testStores(t, func(t *testing.T, ctx context.Context, db *DB) {
		must := func(err error) {
			t.Helper()
			if err != nil {
				t.Fatal(err)
			}
		}
		must(db.Ping(ctx))
		user, err := db.UpsertTelegramUser(ctx, domain.User{TelegramUserID: 1})
		must(err)
		watchID, err := db.CreateWatch(ctx, domain.Watch{UserID: user.ID, Name: "test"})
		must(err)
		app := domain.Appointment{Fingerprint: "slot", DateTime: time.Now().UTC().Add(time.Hour)}
		history, _, err := db.UpsertAppointmentHistory(ctx, watchID, app)
		must(err)
		must(db.MarkNotifications(ctx, []domain.AppointmentHistory{history}, 1, domain.NotificationStatusSent, ""))
		updated, created, err := db.UpsertAppointmentHistory(ctx, watchID, app)
		must(err)
		if created || updated.Status != domain.HistoryStatusNotified || updated.LastNotifiedAt == nil {
			t.Fatalf("continuously available appointment: %+v, created=%v", updated, created)
		}
		cutoff := time.Now().UTC().Add(time.Second)
		must(db.MarkMissingAppointments(ctx, watchID, cutoff, cutoff.Add(-time.Hour)))
		updated, err = db.AppointmentHistoryByID(ctx, history.ID)
		must(err)
		if updated.Status != domain.HistoryStatusExpired || updated.LastNotifiedAt == nil {
			t.Fatalf("missing appointment during cooldown: %+v", updated)
		}
		must(db.MarkMissingAppointments(ctx, watchID, cutoff, cutoff))
		updated, created, err = db.UpsertAppointmentHistory(ctx, watchID, app)
		must(err)
		if created || updated.Status != domain.HistoryStatusSeenAgain || updated.LastNotifiedAt != nil {
			t.Fatalf("reappearing appointment after cooldown: %+v, created=%v", updated, created)
		}
		must(db.HideAppointmentHistory(ctx, user.ID, history.ID))
		must(db.MarkMissingAppointments(ctx, watchID, cutoff, cutoff))
		updated, err = db.AppointmentHistoryByID(ctx, history.ID)
		must(err)
		if updated.Status != domain.HistoryStatusHidden {
			t.Fatalf("missing appointment unhid a user-hidden slot: %+v", updated)
		}
	})
}

func TestConcurrentInviteActivation(t *testing.T) {
	testStores(t, func(t *testing.T, ctx context.Context, db *DB) {
		const claimants = 5
		users := make([]domain.User, claimants)
		for i := range users {
			var err error
			users[i], err = db.UpsertTelegramUser(ctx, domain.User{TelegramUserID: int64(i + 1)})
			if err != nil {
				t.Fatal(err)
			}
		}
		expiresAt := time.Now().Add(time.Hour)
		if err := db.CreateInvite(ctx, "test-once", users[0].ID, 1, &expiresAt); err != nil {
			t.Fatal(err)
		}
		if ok, err := db.RedeemInviteForUser(ctx, "test-once", users[claimants-1].ID+1); err != nil || ok {
			t.Fatalf("unknown user claimed invite: %v, %v", ok, err)
		}
		type result struct {
			ok  bool
			err error
		}
		results := make(chan result, claimants)
		for _, user := range users {
			go func() {
				ok, err := db.RedeemInviteForUser(ctx, "test-once", user.ID)
				results <- result{ok, err}
			}()
		}
		var redeemed, active int
		for range claimants {
			result := <-results
			if result.err != nil {
				t.Fatal(result.err)
			}
			if result.ok {
				redeemed++
			}
		}
		for _, initial := range users {
			user, err := db.UserByID(ctx, initial.ID)
			if err != nil {
				t.Fatal(err)
			}
			if user.IsActive() {
				active++
			}
		}
		if redeemed != 1 || active != 1 {
			t.Fatalf("redeemed %d times, activated %d users", redeemed, active)
		}
	})
}

func TestLegacySQLiteMigrationAndDueWatch(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "test.sqlite")
	legacy, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer legacy.Close()
	if _, err := legacy.Exec(`CREATE TABLE users (
		id INTEGER PRIMARY KEY, telegram_user_id INTEGER NOT NULL UNIQUE, telegram_chat_id INTEGER NOT NULL,
		telegram_username TEXT NOT NULL DEFAULT '', display_name TEXT NOT NULL DEFAULT '', locale TEXT NOT NULL,
		status TEXT NOT NULL, role TEXT NOT NULL, created_at TIMESTAMP NOT NULL,
		updated_at TIMESTAMP NOT NULL, last_seen_at TIMESTAMP NOT NULL
	);`); err != nil {
		t.Fatal(err)
	}
	old := time.Now().UTC().Add(-2 * time.Minute)
	if _, err := legacy.Exec(`INSERT INTO users(id, telegram_user_id, telegram_chat_id, locale, status, role, created_at, updated_at, last_seen_at)
		VALUES (1, 10, 20, 'en', 'active', 'user', $1, $1, $1)`, old); err != nil {
		t.Fatal(err)
	}
	db, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	user, err := db.UserByTelegramID(ctx, 10)
	if err != nil || !user.IsActive() || user.PreferredCityID != nil {
		t.Fatalf("legacy user = %+v, %v", user, err)
	}
	watchID, err := db.CreateWatch(ctx, domain.Watch{UserID: user.ID, Name: "fixture", CheckIntervalSeconds: 60})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.Exec(`UPDATE watches SET last_checked_at=$1, updated_at=$1 WHERE id=$2`, old, watchID); err != nil {
		t.Fatal(err)
	}
	due, err := db.DueWatches(ctx, 5)
	if err != nil || len(due) != 1 || due[0].ID != watchID {
		t.Fatalf("legacy watch did not become due: %+v, %v", due, err)
	}
	if err := db.MarkWatchCheckedIfUnchanged(ctx, due[0], true, ""); err != nil {
		t.Fatalf("record result on legacy watch: %v", err)
	}
}

func TestOpenPostgresRejectsInvalidURLWithoutCredentials(t *testing.T) {
	for _, dsn := range []string{
		"not-a-postgres-url:example-dummy-password",
		"postgres://user:example-dummy-password@%zz-host/db",
		"postgres://user:example-dummy-password@localhost/db?connect_timeout=%invalid",
	} {
		db, err := OpenPostgres(context.Background(), dsn)
		if err == nil {
			db.Close()
			t.Fatal("invalid PostgreSQL URL accepted")
		}
		if strings.Contains(err.Error(), "example-dummy-password") {
			t.Fatal("startup error contains database credentials")
		}
	}
}

func TestLegacySQLiteAppointmentTimeMigration(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "test.sqlite")
	db, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	user, err := db.UpsertTelegramUser(ctx, domain.User{TelegramUserID: 1})
	must(err)
	watchID, err := db.CreateWatch(ctx, domain.Watch{UserID: user.ID, Name: "fixture"})
	must(err)
	wall := time.Date(2026, time.July, 20, 10, 15, 0, 0, time.UTC)
	old, _, err := db.UpsertAppointmentHistory(ctx, watchID, domain.Appointment{Fingerprint: "old", DateTime: wall})
	must(err)
	modern, _, err := db.UpsertAppointmentHistory(ctx, watchID, domain.Appointment{Fingerprint: "modern", DateTime: wall})
	must(err)
	// Reproduce the old driver's encoding and migration state without changing the new row.
	_, err = db.sql.ExecContext(ctx, `UPDATE appointment_history SET date_time=$1 WHERE id=$2; PRAGMA user_version = 0`, wall.String(), old.ID)
	must(err)
	for range 2 {
		must(db.Migrate(ctx))
		migrated, err := db.AppointmentHistoryByID(ctx, old.ID)
		must(err)
		want := wall.Add(-2 * time.Hour)
		if !migrated.DateTime.Equal(want) || migrated.Fingerprint != old.Fingerprint {
			t.Fatalf("legacy appointment = %v (%q), want %v (%q)", migrated.DateTime, migrated.Fingerprint, want, old.Fingerprint)
		}
		unchanged, err := db.AppointmentHistoryByID(ctx, modern.ID)
		must(err)
		if !unchanged.DateTime.Equal(wall) {
			t.Fatalf("current appointment = %v, want %v", unchanged.DateTime, wall)
		}
	}
}

func TestAddressFallbackPreservesLegacyNotification(t *testing.T) {
	testStores(t, func(t *testing.T, ctx context.Context, db *DB) {
		user, err := db.UpsertTelegramUser(ctx, domain.User{TelegramUserID: 101, TelegramChatID: 101})
		if err != nil {
			t.Fatal(err)
		}
		watchID, err := db.CreateWatch(ctx, domain.Watch{UserID: user.ID, CityID: 1, ServiceID: 2})
		if err != nil {
			t.Fatal(err)
		}
		app := domain.Appointment{Fingerprint: "legacy", Address: "Łączna 1", DateTime: time.Now().Add(time.Hour)}
		legacy, _, err := db.UpsertAppointmentHistory(ctx, watchID, app)
		if err != nil {
			t.Fatal(err)
		}
		if err := db.MarkNotified(ctx, watchID, legacy.ID, legacy.Fingerprint, 7, domain.NotificationStatusSent, ""); err != nil {
			t.Fatal(err)
		}

		app.LegacyFingerprint = legacy.Fingerprint
		app.Fingerprint, app.Address = "address-two", "Łączna 2"
		other, created, err := db.UpsertAppointmentHistory(ctx, watchID, app)
		if err != nil || !created || other.ID == legacy.ID || other.LastNotifiedAt != nil {
			t.Fatalf("second address did not get an independent history row: %+v (new=%v, err=%v)", other, created, err)
		}

		app.Fingerprint, app.Address = "address-one", " ŁĄCZNA   1 "
		same, created, err := db.UpsertAppointmentHistory(ctx, watchID, app)
		if err != nil || created || same.ID != legacy.ID || same.LastNotifiedAt == nil || same.SeenCount != 2 {
			t.Fatalf("legacy notification was not retained: %+v (new=%v, err=%v)", same, created, err)
		}

		app.Fingerprint, app.Address = "address-two", "Łączna 2"
		again, created, err := db.UpsertAppointmentHistory(ctx, watchID, app)
		if err != nil || created || again.ID != other.ID || again.SeenCount != 2 {
			t.Fatalf("second address was not deduplicated: %+v (new=%v, err=%v)", again, created, err)
		}
	})
}

func TestAvailableNotificationOptions(t *testing.T) {
	testStores(t, func(t *testing.T, ctx context.Context, db *DB) {
		user, err := db.UpsertTelegramUser(ctx, domain.User{TelegramUserID: 101, TelegramChatID: 101})
		if err != nil {
			t.Fatal(err)
		}
		watchID, err := db.CreateWatch(ctx, domain.Watch{UserID: user.ID, CityID: 1, ServiceID: 2})
		if err != nil {
			t.Fatal(err)
		}
		now := time.Now().Truncate(time.Second)
		for _, fixture := range []struct {
			fingerprint string
			offset      time.Duration
			messageID   int
			status      domain.NotificationStatus
		}{
			{"later", 2 * time.Hour, 10, domain.NotificationStatusSent},
			{"past", -time.Hour, 10, domain.NotificationStatusSent},
			{"earliest", time.Hour, 10, domain.NotificationStatusSent},
			{"same time", time.Hour, 10, domain.NotificationStatusSent},
			{"other message", time.Minute, 11, domain.NotificationStatusSent},
			{"failed", time.Minute, 10, domain.NotificationStatusFailed},
		} {
			item, _, err := db.UpsertAppointmentHistory(ctx, watchID, domain.Appointment{
				Fingerprint: fixture.fingerprint, DateTime: now.Add(fixture.offset),
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := db.MarkNotifications(ctx, []domain.AppointmentHistory{item}, fixture.messageID, fixture.status, ""); err != nil {
				t.Fatal(err)
			}
		}
		items, err := db.AvailableNotificationOptions(ctx, user.ID, watchID, 10, now, 5, NotificationOptionsCursor{})
		if err != nil || len(items) != 3 || items[0].Fingerprint != "earliest" ||
			items[1].Fingerprint != "same time" || items[2].Fingerprint != "later" {
			t.Fatalf("available options = %+v, err = %v", items, err)
		}
		first, last := items[0], items[2]
		if err := db.HideAppointmentHistory(ctx, user.ID, first.ID); err != nil {
			t.Fatal(err)
		}
		items, err = db.AvailableNotificationOptions(ctx, user.ID, watchID, 10, now, 5,
			NotificationOptionsCursor{DateTime: first.DateTime, ID: first.ID})
		if err != nil || len(items) != 2 || items[0].Fingerprint != "same time" {
			t.Fatalf("available options after hidden cursor = %+v, err = %v", items, err)
		}
		items, err = db.AvailableNotificationOptions(ctx, user.ID, watchID, 10, now, 5,
			NotificationOptionsCursor{DateTime: last.DateTime, ID: last.ID, Before: true})
		if err != nil || len(items) != 1 || items[0].Fingerprint != "same time" {
			t.Fatalf("available options before cursor = %+v, err = %v", items, err)
		}
		items, err = db.AvailableNotificationOptions(ctx, user.ID+1, watchID, 10, now, 5, NotificationOptionsCursor{})
		if err != nil || len(items) != 0 {
			t.Fatalf("another user could list watch options: %+v, err = %v", items, err)
		}
	})
}

func TestUpcomingHistoryPagesNearestFirst(t *testing.T) {
	testStores(t, func(t *testing.T, ctx context.Context, db *DB) {
		user, err := db.UpsertTelegramUser(ctx, domain.User{TelegramUserID: 31, TelegramChatID: 32, Locale: "pl",
			Status: domain.UserStatusActive, Role: domain.UserRoleUser})
		if err != nil {
			t.Fatal(err)
		}
		watchID, err := db.CreateWatch(ctx, domain.Watch{UserID: user.ID, Name: "Echo", CityID: 1, CityName: "Warszawa",
			ServiceID: 2, ServiceName: "Echo", DoctorMode: domain.DoctorModeAny, FacilityMode: domain.FacilityModeAll,
			NextDays: 14, CheckIntervalSeconds: 120})
		if err != nil {
			t.Fatal(err)
		}
		now := time.Date(2026, 12, 1, 8, 0, 0, 0, time.UTC)
		// Inserted out of order: two past times and three upcoming ones.
		for i, hours := range []int{-48, 72, 24, -2, 48} {
			if _, _, err := db.UpsertAppointmentHistory(ctx, watchID, domain.Appointment{
				Fingerprint: fmt.Sprintf("slot-%d", i), DateTime: now.Add(time.Duration(hours) * time.Hour),
				ServiceID: 2, ServiceName: "Echo", FacilityID: 4, FacilityName: "LuxMed", CityID: 1, CityName: "Warszawa",
			}); err != nil {
				t.Fatal(err)
			}
		}
		var got []int
		ids, err := db.UpcomingHistoryIDs(ctx, user.ID, now, 6)
		if err != nil {
			t.Fatal(err)
		}
		for offset := 0; offset < len(ids); offset += 2 {
			page, err := db.HistoryByIDs(ctx, user.ID, ids[offset:min(offset+2, len(ids))])
			if err != nil {
				t.Fatal(err)
			}
			for _, item := range page {
				got = append(got, int(item.DateTime.Sub(now).Hours()))
			}
		}
		if want := []int{24, 48, 72, -2, -48}; !slices.Equal(got, want) {
			t.Fatalf("hours from now across pages = %v, want %v", got, want)
		}
	})
}

func TestReconnectJournalReplayIsScopedToDeliveryClaim(t *testing.T) {
	testStores(t, func(t *testing.T, ctx context.Context, db *DB) {
		must := func(err error) {
			t.Helper()
			if err != nil {
				t.Fatal(err)
			}
		}
		directory := t.TempDir()
		must(db.SetReconnectJournalDirectory(directory))
		user, err := db.UpsertTelegramUser(ctx, domain.User{TelegramUserID: 42})
		must(err)
		_, err = db.CreateWatch(ctx, domain.Watch{UserID: user.ID})
		must(err)
		must(db.BeginLuxMedReconnect(ctx, user.ID))
		now := time.Now().UTC()
		claim, err := db.ClaimLuxMedReconnectDelivery(ctx, user.ID, now)
		must(err)
		if claim.Attempt != 1 {
			t.Fatalf("initial delivery claim: %+v", claim)
		}

		unavailable, cancel := context.WithCancel(ctx)
		cancel()
		err = db.JournalReconnectRejection(unavailable, user.ID, claim, now.Add(time.Hour), 2)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("simulated primary write failure: %v", err)
		}
		// Recreate the application-side storage handle with the same durable directory.
		restarted := &DB{sql: db.sql, postgres: db.postgres}
		must(restarted.SetReconnectJournalDirectory(directory))
		must(restarted.ReplayReconnectRejection(ctx, user.ID))

		next, err := restarted.ClaimLuxMedReconnectDelivery(ctx, user.ID, now.Add(2*time.Hour))
		must(err)
		if next.Attempt != 2 || next.ID == claim.ID {
			t.Fatalf("replay did not restore the retry: %+v", next)
		}

		err = restarted.JournalReconnectRejection(unavailable, user.ID, next, now.Add(3*time.Hour), 2)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("simulated failure before an episode was closed: %v", err)
		}
		must(db.ResolveLuxMedReconnect(ctx, user.ID))
		must(db.BeginLuxMedReconnect(ctx, user.ID))
		replacement, err := db.ClaimLuxMedReconnectDelivery(ctx, user.ID, now.Add(4*time.Hour))
		must(err)
		if replacement.Attempt != 1 || replacement.ID == claim.ID || replacement.ID == next.ID {
			t.Fatalf("replacement episode reused a delivery claim: %+v", replacement)
		}
		must(restarted.ReplayReconnectRejection(ctx, user.ID))
		afterReplay, err := db.ClaimLuxMedReconnectDelivery(ctx, user.ID, now.Add(5*time.Hour))
		must(err)
		if afterReplay.Attempt != 0 {
			t.Fatalf("old rejection made an unrelated delivery retryable: %+v", afterReplay)
		}
	})
}

func TestUnblockAcceleratesOnlyConfirmedForbiddenDelivery(t *testing.T) {
	testStores(t, func(t *testing.T, ctx context.Context, db *DB) {
		must := func(err error) {
			t.Helper()
			if err != nil {
				t.Fatal(err)
			}
		}
		user, err := db.UpsertTelegramUser(ctx, domain.User{TelegramUserID: 42})
		must(err)
		_, err = db.CreateWatch(ctx, domain.Watch{UserID: user.ID})
		must(err)
		must(db.BeginLuxMedReconnect(ctx, user.ID))
		now := time.Now().UTC()
		first, err := db.ClaimLuxMedReconnectDelivery(ctx, user.ID, now)
		must(err)
		must(db.RecordLuxMedReconnectRejection(ctx, user.ID, first, now.Add(6*time.Hour), 4))
		unblockedAt := now.Add(time.Minute)
		must(db.ResumeLuxMedReconnectDelivery(ctx, user.ID, unblockedAt))
		second, err := db.ClaimLuxMedReconnectDelivery(ctx, user.ID, unblockedAt.Add(time.Second))
		must(err)
		if second.Attempt != 2 {
			t.Fatalf("first forbidden rejection still blocked delivery: %+v", second)
		}
		must(db.ResumeLuxMedReconnectDelivery(ctx, user.ID, unblockedAt))
		unknown, err := db.ClaimLuxMedReconnectDelivery(ctx, user.ID, unblockedAt.Add(time.Second))
		must(err)
		if unknown.Attempt != 0 {
			t.Fatalf("unblock retried a send with unknown outcome: %+v", unknown)
		}
		must(db.RecordLuxMedReconnectRejection(ctx, user.ID, second, now.Add(time.Hour), 0))
		must(db.ResumeLuxMedReconnectDelivery(ctx, user.ID, unblockedAt))
		throttled, err := db.ClaimLuxMedReconnectDelivery(ctx, user.ID, unblockedAt.Add(time.Second))
		must(err)
		if throttled.Attempt != 0 {
			t.Fatalf("unblock bypassed Telegram rate limiting: %+v", throttled)
		}
	})
}

func TestReconnectOnlyRequeuesCurrentSearches(t *testing.T) {
	for _, mode := range []string{"pending password", "verified recovery"} {
		t.Run(mode, func(t *testing.T) {
			testStores(t, func(t *testing.T, ctx context.Context, db *DB) {
				must := func(err error) {
					t.Helper()
					if err != nil {
						t.Fatal(err)
					}
				}
				user, err := db.UpsertTelegramUser(ctx, domain.User{TelegramUserID: 42, Status: domain.UserStatusActive})
				must(err)
				today, _ := luxmed.SearchDateBounds(time.Now())
				past := today.AddDate(0, 0, -1)
				currentID, err := db.CreateWatch(ctx, domain.Watch{UserID: user.ID, CityID: 1, ServiceID: 12, DateTo: &today})
				must(err)
				expiredID, err := db.CreateWatch(ctx, domain.Watch{UserID: user.ID, CityID: 1, ServiceID: 34, DateTo: &past})
				must(err)
				must(db.MarkWatchChecked(ctx, currentID, false, "luxmed_authentication"))
				must(db.MarkWatchChecked(ctx, expiredID, true, ""))
				must(db.MarkWatchChecked(ctx, expiredID, false, "luxmed_authentication"))
				before, err := db.Watch(ctx, expiredID)
				must(err)
				must(db.BeginLuxMedReconnectAfterSearch(ctx, user.ID))
				if mode == "pending password" {
					must(db.SavePendingLuxMedAccount(ctx, domain.LuxMedAccount{UserID: user.ID, Status: domain.AccountStatusActive}))
				} else {
					must(db.ResolveLuxMedReconnect(ctx, user.ID))
				}
				after, err := db.Watch(ctx, expiredID)
				must(err)
				if after.LastCheckedAt == nil || !after.LastCheckedAt.Equal(*before.LastCheckedAt) ||
					after.LastSuccessAt == nil || !after.LastSuccessAt.Equal(*before.LastSuccessAt) ||
					after.DateTo == nil || !after.DateTo.Equal(past) || after.ServiceID != 34 {
					t.Fatalf("expired search lost schedule or filters: %+v", after)
				}
				due, err := db.DueWatches(ctx, 5)
				must(err)
				if len(due) != 1 || due[0].ID != currentID || due[0].LastCheckedAt != nil || due[0].ServiceID != 12 {
					t.Fatalf("wrong searches requeued: %+v", due)
				}
			})
		})
	}
}

func TestReconnectRequiresCurrentActiveSearchInWarsaw(t *testing.T) {
	testStores(t, func(t *testing.T, ctx context.Context, db *DB) {
		must := func(err error) {
			t.Helper()
			if err != nil {
				t.Fatal(err)
			}
		}
		user, err := db.UpsertTelegramUser(ctx, domain.User{TelegramUserID: 42})
		must(err)
		now := time.Now().UTC()
		today, end := luxmed.SearchDateBounds(now)
		past := today.AddDate(0, 0, -1)
		watch := domain.Watch{UserID: user.ID, DateTo: &past}
		watch.ID, err = db.CreateWatch(ctx, watch)
		must(err)
		must(db.BeginLuxMedReconnect(ctx, user.ID))
		required, err := db.LuxMedReconnectRequired(ctx, user.ID)
		if err != nil || required {
			t.Fatalf("expired search opened an episode: required=%v err=%v", required, err)
		}
		watch.DateFrom, watch.DateTo = &end, nil
		must(db.UpdateWatch(ctx, watch))
		must(db.BeginLuxMedReconnectAfterSearch(ctx, user.ID))
		required, err = db.LuxMedReconnectRequired(ctx, user.ID)
		if err != nil || required {
			t.Fatalf("search beyond the horizon opened an episode: required=%v err=%v", required, err)
		}
		watch.DateFrom, watch.DateTo = nil, &today
		must(db.UpdateWatch(ctx, watch))
		must(db.BeginLuxMedReconnect(ctx, user.ID))
		afterMidnight := today.AddDate(0, 0, 1)
		due, err := db.LuxMedReconnectDeliveryDue(ctx, user.ID, afterMidnight)
		if err != nil || due {
			t.Fatalf("delivery stayed due after Warsaw midnight: due=%v err=%v", due, err)
		}
		expired, err := db.ClaimLuxMedReconnectDelivery(ctx, user.ID, afterMidnight)
		if err != nil || expired.Attempt != 0 {
			t.Fatalf("expired search reserved a delivery: %+v %v", expired, err)
		}
		claim, err := db.ClaimLuxMedReconnectDelivery(ctx, user.ID, now)
		if err != nil || claim.Attempt != 1 {
			t.Fatalf("search should remain current until the end of the last day: %+v %v", claim, err)
		}
	})
}

func TestUnblockDoesNotReopenWeeklyAttemptWithUnknownOutcome(t *testing.T) {
	testStores(t, func(t *testing.T, ctx context.Context, db *DB) {
		must := func(err error) {
			t.Helper()
			if err != nil {
				t.Fatal(err)
			}
		}
		user, err := db.UpsertTelegramUser(ctx, domain.User{TelegramUserID: 42})
		must(err)
		_, err = db.CreateWatch(ctx, domain.Watch{UserID: user.ID})
		must(err)
		must(db.BeginLuxMedReconnect(ctx, user.ID))
		now := time.Now().UTC()
		for i := range 4 {
			at := now.Add(time.Duration(i) * 7 * time.Hour)
			claim, err := db.ClaimLuxMedReconnectDelivery(ctx, user.ID, at)
			must(err)
			if claim.Attempt != i+1 {
				t.Fatalf("unexpected claim: %+v", claim)
			}
			must(db.RecordLuxMedReconnectRejection(ctx, user.ID, claim, at.Add(6*time.Hour), 4))
		}
		later := now.Add(8 * 24 * time.Hour)
		weekly, err := db.ClaimLuxMedReconnectDelivery(ctx, user.ID, later)
		must(err)
		if weekly.Attempt != 5 {
			t.Fatalf("expected weekly retry: %+v", weekly)
		}
		// A crash or lost Telegram reply leaves this claim without an outcome.
		restarted := &DB{sql: db.sql, postgres: db.postgres}
		must(restarted.ResumeLuxMedReconnectDelivery(ctx, user.ID, later.Add(time.Second)))
		duplicate, err := restarted.ClaimLuxMedReconnectDelivery(ctx, user.ID, later.Add(time.Minute))
		must(err)
		if duplicate.Attempt != 0 {
			t.Fatalf("unblock made an unknown weekly attempt retryable: %+v", duplicate)
		}
	})
}

func TestReconnectJournalKeepsBothOutcomesDuringDatabaseFailure(t *testing.T) {
	testStores(t, func(t *testing.T, ctx context.Context, db *DB) {
		must := func(err error) {
			t.Helper()
			if err != nil {
				t.Fatal(err)
			}
		}
		journal := t.TempDir()
		must(db.SetReconnectJournalDirectory(journal))
		user, err := db.UpsertTelegramUser(ctx, domain.User{TelegramUserID: 42})
		must(err)
		_, err = db.CreateWatch(ctx, domain.Watch{UserID: user.ID})
		must(err)
		must(db.BeginLuxMedReconnect(ctx, user.ID))
		now := time.Now().UTC()
		claim, err := db.ClaimLuxMedReconnectDelivery(ctx, user.ID, now)
		must(err)
		unavailable, cancel := context.WithCancel(ctx)
		cancel()
		if err := db.JournalReconnectRejection(unavailable, user.ID, claim, now.Add(time.Minute), 0); !errors.Is(err, context.Canceled) {
			t.Fatalf("expected the initial write failure: %v", err)
		}
		newClaim := ReconnectDeliveryClaim{ID: "new-unknown-id"}
		if err := db.JournalUnsentReconnectReservation(unavailable, user.ID, newClaim, now.Add(time.Minute)); !errors.Is(err, context.Canceled) {
			t.Fatalf("expected the later write failure: %v", err)
		}
		restarted := &DB{sql: db.sql, postgres: db.postgres}
		must(restarted.SetReconnectJournalDirectory(journal))
		must(restarted.ReplayReconnectRejection(ctx, user.ID))
		claim, err = restarted.ClaimLuxMedReconnectDelivery(ctx, user.ID, now.Add(2*time.Minute))
		must(err)
		if claim.Attempt != 2 {
			t.Fatalf("later reservation lost the confirmed rejection: %+v", claim)
		}
	})
}

func TestUnsentReservationJournalWaitsForPostgresCommit(t *testing.T) {
	for _, mode := range []string{"file journal", "PostgreSQL only"} {
		t.Run(mode, func(t *testing.T) {
			testStores(t, func(t *testing.T, ctx context.Context, db *DB) {
				if !db.postgres {
					t.Skip("requires concurrent PostgreSQL transactions")
				}
				must := func(err error) {
					t.Helper()
					if err != nil {
						t.Fatal(err)
					}
				}
				var journal string
				if mode == "file journal" {
					journal = t.TempDir()
					must(db.SetReconnectJournalDirectory(journal))
				}
				user, err := db.UpsertTelegramUser(ctx, domain.User{TelegramUserID: 42})
				must(err)
				_, err = db.CreateWatch(ctx, domain.Watch{UserID: user.ID})
				must(err)
				must(db.BeginLuxMedReconnect(ctx, user.ID))
				tx, err := db.sql.BeginTx(ctx, nil)
				must(err)
				defer tx.Rollback()
				claim := ReconnectDeliveryClaim{ID: "late-claim"}
				_, err = tx.ExecContext(ctx, `
			INSERT INTO luxmed_reconnect_delivery (user_id, attempts, claim_id) VALUES ($1, 1, $2)
		`, user.ID, claim.ID)
				must(err)
				now := time.Now().UTC()
				must(db.JournalUnsentReconnectReservation(ctx, user.ID, claim, now.Add(time.Minute)))
				if journal != "" {
					_, err = os.Stat(filepath.Join(journal, reconnectRejectionName(user.ID, claim.ID)))
					must(err)
				} else {
					var pending int
					must(db.sql.QueryRowContext(ctx, "SELECT COUNT(*) FROM luxmed_reconnect_unsent WHERE user_id=$1 AND claim_id=$2", user.ID, claim.ID).Scan(&pending))
					if pending != 1 {
						t.Fatalf("known unsent outcome was not stored before commit: %d", pending)
					}
				}
				must(tx.Commit())

				restarted := &DB{sql: db.sql, postgres: true}
				if journal != "" {
					must(restarted.SetReconnectJournalDirectory(journal))
				}
				must(restarted.ReplayReconnectRejection(ctx, user.ID))
				if journal != "" {
					if _, err := os.Stat(filepath.Join(journal, reconnectRejectionName(user.ID, claim.ID))); !errors.Is(err, os.ErrNotExist) {
						t.Fatalf("confirmed journal was not cleared: %v", err)
					}
				} else {
					var pending int
					must(db.sql.QueryRowContext(ctx, "SELECT COUNT(*) FROM luxmed_reconnect_unsent WHERE user_id=$1", user.ID).Scan(&pending))
					if pending != 0 {
						t.Fatalf("applied outcomes remained queued: %d", pending)
					}
				}
				next, err := restarted.ClaimLuxMedReconnectDelivery(ctx, user.ID, now.Add(2*time.Minute))
				must(err)
				if next.Attempt != 2 || next.ID == claim.ID {
					t.Fatalf("late committed claim remained blocked: %+v", next)
				}
			})
		})
	}
}

func TestReconnectRejectionIsRetriedWithoutDiskJournal(t *testing.T) {
	testStores(t, func(t *testing.T, ctx context.Context, db *DB) {
		db.reconnectDirectory = ""
		must := func(err error) {
			t.Helper()
			if err != nil {
				t.Fatal(err)
			}
		}
		user, err := db.UpsertTelegramUser(ctx, domain.User{TelegramUserID: 42})
		must(err)
		_, err = db.CreateWatch(ctx, domain.Watch{UserID: user.ID})
		must(err)
		must(db.BeginLuxMedReconnect(ctx, user.ID))
		now := time.Now().UTC()
		claim, err := db.ClaimLuxMedReconnectDelivery(ctx, user.ID, now)
		must(err)
		unavailable, cancel := context.WithCancel(ctx)
		cancel()
		retryAt := now.Add(time.Hour)
		if err := db.JournalReconnectRejection(unavailable, user.ID, claim, retryAt, 4); !errors.Is(err, context.Canceled) {
			t.Fatalf("write failure = %v", err)
		}
		if err := db.ReplayReconnectRejection(unavailable, user.ID); !errors.Is(err, context.Canceled) {
			t.Fatalf("replay during outage = %v", err)
		}
		restarted := &DB{sql: db.sql, postgres: db.postgres}
		must(restarted.ReplayReconnectRejection(ctx, user.ID))
		unknown, err := restarted.ClaimLuxMedReconnectDelivery(ctx, user.ID, retryAt.Add(time.Hour))
		must(err)
		if unknown.Attempt != 0 {
			t.Fatalf("restart without a recorded outcome allowed an ambiguous resend: %+v", unknown)
		}
		must(db.ReplayReconnectRejection(ctx, user.ID))
		must(db.ReplayReconnectRejection(ctx, user.ID))
		early, err := db.ClaimLuxMedReconnectDelivery(ctx, user.ID, now.Add(time.Minute))
		must(err)
		if early.Attempt != 0 {
			t.Fatalf("retry ignored its cooldown: %+v", early)
		}
		next, err := db.ClaimLuxMedReconnectDelivery(ctx, user.ID, retryAt.Add(time.Second))
		must(err)
		if next.Attempt != 2 || next.ID == claim.ID {
			t.Fatalf("retry was not restored: %+v", next)
		}
	})
}

func TestWatchesAndReconnectStateStayConsistentDuringRecovery(t *testing.T) {
	testStores(t, func(t *testing.T, ctx context.Context, db *DB) {
		user, err := db.UpsertTelegramUser(ctx, domain.User{TelegramUserID: 42})
		if err != nil {
			t.Fatal(err)
		}
		watchID, err := db.CreateWatch(ctx, domain.Watch{UserID: user.ID})
		if err != nil {
			t.Fatal(err)
		}
		const cycles = 40
		changes := make(chan error, 1)
		go func() {
			for range cycles {
				if err := db.BeginLuxMedReconnect(ctx, user.ID); err != nil {
					changes <- err
					return
				}
				watch, err := db.Watch(ctx, watchID)
				if err != nil {
					changes <- err
					return
				}
				if err := db.MarkWatchCheckedIfUnchanged(ctx, watch, false, "luxmed_authentication"); err != nil {
					changes <- err
					return
				}
				if err := db.ResolveLuxMedReconnect(ctx, user.ID); err != nil {
					changes <- err
					return
				}
			}
			changes <- nil
		}()
		for range cycles * 2 {
			watches, access, err := db.WatchesWithReconnectState(ctx, user.ID)
			if err != nil || len(watches) != 1 || watches[0].ID != watchID {
				t.Errorf("load snapshot: watches=%+v err=%v", watches, err)
				break
			}
			if access.EpisodeID == "" && watches[0].LastError == "luxmed_authentication" {
				t.Error("snapshot mixed a recovered session with a stale sign-in error")
				break
			}
		}
		if err := <-changes; err != nil {
			t.Fatal(err)
		}
	})
}
