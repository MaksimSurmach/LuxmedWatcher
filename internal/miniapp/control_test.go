package miniapp

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/maksimsurmach/luxmed-watcher/internal/domain"
	"github.com/maksimsurmach/luxmed-watcher/internal/luxmed"
	"github.com/maksimsurmach/luxmed-watcher/internal/scheduler"
	"github.com/maksimsurmach/luxmed-watcher/internal/storage"
)

func TestControlScopesReadsAndChangesToAssignedUser(t *testing.T) {
	ctx := t.Context()
	db, err := storage.Open(ctx, filepath.Join(t.TempDir(), "control.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	addUser := func(id int64, status domain.UserStatus) domain.User {
		t.Helper()
		user, err := db.UpsertTelegramUser(ctx, domain.User{TelegramUserID: id, Status: status, Locale: "pl"})
		if err != nil {
			t.Fatal(err)
		}
		return user
	}
	owner := addUser(10, domain.UserStatusActive)
	other := addUser(20, domain.UserStatusActive)
	blocked := addUser(30, domain.UserStatusBlocked)
	city := domain.City{ID: 1, Name: "Warszawa"}
	if err := db.UpsertCities(ctx, []domain.City{city}); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertProcedures(ctx, city, []domain.Procedure{{ID: 2, Name: "Konsultacja dermatologiczna"}}); err != nil {
		t.Fatal(err)
	}
	createWatch := func(user domain.User, name string) int64 {
		t.Helper()
		id, err := db.CreateWatch(ctx, domain.Watch{UserID: user.ID, CityID: 1, ServiceID: 2, Name: name})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	ownID, otherID := createWatch(owner, "owner-watch"), createWatch(other, "other-watch")
	key := strings.Repeat("9a", 32)
	browsers := map[int64]*luxmed.Browser{10: {}, 20: {}, 30: {}}
	runner := scheduler.NewRunner(db, nil, nil, 0, 0, false)
	runner.UseBrowsers(browsers)
	handler := Control(DashboardConfig{MinimumInterval: 2 * time.Minute}, db, browsers, owner.TelegramUserID, key, runner)
	req := func(method, path, token, body string, want int) string {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s %s: got %d, want %d", method, path, w.Code, want)
		}
		return w.Body.String()
	}
	req("GET", "/v1/workspace", "", "", http.StatusUnauthorized)
	req("GET", "/v1/workspace", strings.Repeat("6b", 32), "", http.StatusUnauthorized)
	handlerForOwner := handler
	handler = Control(DashboardConfig{}, db, browsers, blocked.TelegramUserID, key, runner)
	req("GET", "/v1/workspace", key, "", http.StatusForbidden)
	handler = handlerForOwner
	view := req("GET", "/v1/workspace", key, "", http.StatusOK)
	if !strings.Contains(view, "owner-watch") || strings.Contains(view, "other-watch") {
		t.Fatal("workspace scope is incorrect")
	}
	req("POST", "/v1/preferences", key, `{"Locale":"uk","CityID":1}`, http.StatusNoContent)
	req("POST", "/v1/searches", key, `{"CityID":1,"ServiceID":2,"NextDays":7,"CheckMinutes":1}`, http.StatusBadRequest)
	req("POST", "/v1/searches", key, `{"CityID":1,"ServiceID":2,"NextDays":7,"CheckMinutes":5}`, http.StatusNoContent)
	if view := req("GET", "/v1/workspace", key, "", http.StatusOK); !strings.Contains(view, "Konsultacja dermatologiczna") {
		t.Fatal("saved search with the provider procedure name is missing")
	}
	status := func(id int64, value string, want int) {
		t.Helper()
		req("POST", "/v1/searches/status", key, fmt.Sprintf(`{"ID":%d,"Status":%q}`, id, value), want)
	}
	status(otherID, "paused", http.StatusNotFound)
	status(ownID, "deleted", http.StatusBadRequest)
	status(ownID, "paused", http.StatusNoContent)
	watch, err := db.Watch(ctx, ownID)
	if err != nil || watch.Status != domain.WatchStatusPaused {
		t.Fatalf("paused watch: %v, %v", watch.Status, err)
	}
	status(otherID, "active", http.StatusNotFound)
	status(ownID, "active", http.StatusNoContent)
	watch, err = db.Watch(ctx, ownID)
	if err != nil || watch.Status != domain.WatchStatusActive {
		t.Fatalf("status API did not resume the Chrome watch locally: %v, %v", watch.Status, err)
	}
	user, err := db.UserByTelegramID(ctx, owner.TelegramUserID)
	if err != nil || user.Locale != "uk" || user.PreferredCityID == nil || *user.PreferredCityID != 1 {
		t.Fatalf("updated preferences: %+v, %v", user, err)
	}
	req("POST", "/v1/connect", key, "{}", http.StatusNotFound)
}
