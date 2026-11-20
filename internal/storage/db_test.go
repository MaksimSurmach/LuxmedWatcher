package storage

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/maksimsurmach/luxmed-watcher/internal/domain"
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
		user, err := db.UpsertTelegramUser(ctx, domain.User{TelegramUserID: 4_294_967_296, TelegramChatID: 4_294_967_297})
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
		must(db.UpsertLuxMedAccount(ctx, account))
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
	if _, err := legacy.Exec(`UPDATE watches SET last_checked_at=$1 WHERE id=$2`, old, watchID); err != nil {
		t.Fatal(err)
	}
	due, err := db.DueWatches(ctx, 5)
	if err != nil || len(due) != 1 || due[0].ID != watchID {
		t.Fatalf("legacy watch did not become due: %+v, %v", due, err)
	}
}

func TestOpenPostgresRejectsInvalidURLWithoutCredentials(t *testing.T) {
	for _, dsn := range []string{
		"not-a-postgres-url:example-dummy-password",
		"postgres://user:example-dummy-password@%bad-host/db",
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
