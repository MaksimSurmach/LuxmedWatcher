package storage

import (
	"context"
	"testing"
	"time"

	"github.com/maksimsurmach/luxmed-watcher/internal/domain"
)

func TestInviteRedeemAndWatchHistory(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, t.TempDir()+"/test.sqlite")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

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
	ctx := context.Background()
	db, err := Open(ctx, t.TempDir()+"/test.sqlite")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	err = db.UpsertCities(ctx, []domain.City{
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
}
