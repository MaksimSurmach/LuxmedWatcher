package miniapp

import (
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/maksimsurmach/luxmed-watcher/internal/domain"
	"github.com/maksimsurmach/luxmed-watcher/internal/luxmed"
	"github.com/maksimsurmach/luxmed-watcher/internal/storage"
)

func TestDashboardUsesTelegramIdentityAndExistingUserData(t *testing.T) {
	ctx := t.Context()
	db, err := storage.Open(ctx, filepath.Join(t.TempDir(), "database.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	addUser := func(id int64) domain.User {
		t.Helper()
		user, err := db.UpsertTelegramUser(ctx, domain.User{
			TelegramUserID: id, TelegramChatID: id, Status: domain.UserStatusActive, Role: domain.UserRoleAdmin, Locale: "ru",
		})
		if err != nil {
			t.Fatal(err)
		}
		return user
	}
	owner, other := addUser(24), addUser(42)
	unassigned := addUser(67)
	city := domain.City{ID: 1, Name: "Warszawa"}
	if err := db.UpsertCities(ctx, []domain.City{city}); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertProcedures(ctx, city, []domain.Procedure{{ID: 2, Name: "Konsultacja dermatologiczna"}}); err != nil {
		t.Fatal(err)
	}
	otherWatch, err := db.CreateWatch(ctx, domain.Watch{UserID: other.ID, CityID: 1, ServiceID: 2, Name: "Other"})
	if err != nil {
		t.Fatal(err)
	}

	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	const origin = "https://app.example"
	d := &dashboard{db: db, key: pub, sessions: newSessions(),
		browsers: map[int64]*luxmed.Browser{owner.TelegramUserID: {}, other.TelegramUserID: {}},
		cfg:      DashboardConfig{Origin: origin, BotID: 12, MinimumInterval: 2 * time.Minute}}
	handler := d.handler()
	request := func(action, body, requestOrigin, identity string, want int) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, dashboardPrefix+action, strings.NewReader(body))
		req.Header.Set("Origin", requestOrigin)
		if action == "auth" {
			req.Header.Set("X-Telegram-Init-Data", identity)
		} else {
			req.Header.Set("Authorization", "Bearer "+identity)
		}
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		if res.Code != want {
			t.Fatalf("%s status = %d, want %d. Body %s", action, res.Code, want, res.Body)
		}
		return res
	}

	signed := signTelegram(owner.TelegramUserID, 12, time.Now(), priv)
	request("auth", "{}", origin, signTelegram(unassigned.TelegramUserID, 12, time.Now(), priv), http.StatusForbidden)
	request("auth", "{}", origin, signTelegram(owner.TelegramUserID, 12, time.Now().Add(-6*time.Minute), priv), http.StatusUnauthorized)
	request("auth", "{}", "https://other.example", signed, http.StatusUnauthorized)
	request("load", "{}", origin, signed, http.StatusUnauthorized)
	session := func(proof string) string {
		t.Helper()
		res := request("auth", "{}", origin, proof, http.StatusOK)
		var result struct{ Token string }
		if err := json.Unmarshal(res.Body.Bytes(), &result); err != nil || result.Token == "" {
			t.Fatalf("invalid session response: %s", res.Body)
		}
		return result.Token
	}
	token := session(signed)
	otherToken := session(signTelegram(other.TelegramUserID, 12, time.Now(), priv))
	if res := request("load", "{}", origin, otherToken, http.StatusOK); !strings.Contains(res.Body.String(), "Other") {
		t.Fatalf("other user's search missing: %s", res.Body)
	}
	request("load", "{}", origin, d.sessions.issue(owner.TelegramUserID, time.Now().Add(-2*time.Hour)), http.StatusUnauthorized)
	request("load", "{}", origin, "", http.StatusUnauthorized)
	request("settings", `{"Locale":"pl","CityID":1}`, origin, token, http.StatusNoContent)
	request("watch", `{"CityID":1,"ServiceID":2,"NextDays":14,"CheckMinutes":1}`, origin, token, http.StatusBadRequest)
	request("watch", `{"CityID":1,"ServiceID":2,"NextDays":14,"CheckMinutes":5}`, origin, token, http.StatusNoContent)
	res := request("load", "{}", origin, token, http.StatusOK)
	var view struct {
		Watches []domain.Watch
		Locale  string
		CityID  int
	}
	if err := json.Unmarshal(res.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if view.Locale != "pl" || view.CityID != 1 || len(view.Watches) != 1 || view.Watches[0].Name != "Konsultacja dermatologiczna · Warszawa" {
		t.Fatalf("unexpected profile or watches: %+v", view)
	}
	id := view.Watches[0].ID
	request("status", fmt.Sprintf(`{"ID":%d,"Status":"deleted"}`, otherWatch), origin, token, http.StatusNotFound)
	request("status", fmt.Sprintf(`{"ID":%d,"Status":"paused"}`, id), origin, token, http.StatusNoContent)
	request("watch", fmt.Sprintf(`{"ID":%d,"CityID":1,"ServiceID":2,"NextDays":7,"CheckMinutes":10}`, id), origin, token, http.StatusNoContent)
	watch, err := db.Watch(ctx, id)
	if err != nil || watch.Status != domain.WatchStatusPaused || watch.NextDays != 7 {
		t.Fatalf("editing a paused watch: %+v, %v", watch, err)
	}
	item := domain.Appointment{Fingerprint: "slot", DateTime: time.Now().AddDate(0, 0, 1), ServiceName: "Konsultacja okulistyczna  - bez badania dna oka", RawPayload: "private record"}
	if _, _, err := db.UpsertAppointmentHistory(ctx, id, item); err != nil {
		t.Fatal(err)
	}
	history := request("history", "{}", origin, token, http.StatusOK).Body.String()
	if !strings.Contains(history, "Konsultacja okulistyczna") || strings.Contains(history, "private record") {
		t.Fatalf("unexpected history response: %s", history)
	}
	request("settings", `{"Locale":"uk","CityID":1}`, origin, token, http.StatusNoContent)
	res = request("procedures", `{"CityID":1,"Query":"дерматолога"}`, origin, token, http.StatusOK)
	if !strings.Contains(res.Body.String(), "Консультація дерматолога") {
		t.Fatalf("unexpected translated procedure response: %s", res.Body)
	}
	res = request("load", "{}", origin, token, http.StatusOK)
	if !strings.Contains(res.Body.String(), "Консультація дерматолога") {
		t.Fatalf("stored watch label was not localized: %s", res.Body)
	}
	history = request("history", "{}", origin, token, http.StatusOK).Body.String()
	if !strings.Contains(history, "Консультація офтальмолога без огляду очного дна") {
		t.Fatalf("stored history label was not localized: %s", history)
	}
	watch, err = db.Watch(ctx, id)
	if err != nil || watch.ServiceName != "Konsultacja dermatologiczna" {
		t.Fatalf("stored Polish label changed: %q, %v", watch.ServiceName, err)
	}
}
