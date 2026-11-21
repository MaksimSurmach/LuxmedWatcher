package miniapp

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/maksimsurmach/luxmed-watcher/internal/domain"
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
	d := &dashboard{db: db, key: pub, cfg: DashboardConfig{Origin: origin, BotID: 12, OwnerID: 24, MinimumInterval: 2 * time.Minute}}
	handler := d.handler()
	sign := func(id int64) string {
		user := fmt.Sprintf(`{"id":%d}`, id)
		when := strconv.FormatInt(time.Now().Unix(), 10)
		signature := ed25519.Sign(priv, []byte("12:WebAppData\nauth_date="+when+"\nuser="+user))
		return url.Values{"auth_date": {when}, "user": {user}, "signature": {base64.RawURLEncoding.EncodeToString(signature)}}.Encode()
	}
	request := func(action, body, requestOrigin, identity string, want int) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, dashboardPrefix+action, strings.NewReader(body))
		req.Header.Set("Origin", requestOrigin)
		req.Header.Set("X-Telegram-Init-Data", identity)
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		if res.Code != want {
			t.Fatalf("%s status = %d, want %d. Body %s", action, res.Code, want, res.Body)
		}
		return res
	}

	signed := sign(owner.TelegramUserID)
	request("load", "{}", origin, sign(other.TelegramUserID), http.StatusUnauthorized)
	request("load", "{}", "https://other.example", signed, http.StatusUnauthorized)
	request("load", "{}", origin, "", http.StatusUnauthorized)
	request("settings", `{"Locale":"pl","CityID":1}`, origin, signed, http.StatusNoContent)
	request("watch", `{"CityID":1,"ServiceID":2,"NextDays":14,"CheckMinutes":1}`, origin, signed, http.StatusBadRequest)
	request("watch", `{"CityID":1,"ServiceID":2,"NextDays":14,"CheckMinutes":5}`, origin, signed, http.StatusNoContent)
	res := request("load", "{}", origin, signed, http.StatusOK)
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
	request("status", fmt.Sprintf(`{"ID":%d,"Status":"deleted"}`, otherWatch), origin, signed, http.StatusNotFound)
	request("status", fmt.Sprintf(`{"ID":%d,"Status":"paused"}`, id), origin, signed, http.StatusNoContent)
	request("watch", fmt.Sprintf(`{"ID":%d,"CityID":1,"ServiceID":2,"NextDays":7,"CheckMinutes":10}`, id), origin, signed, http.StatusNoContent)
	watch, err := db.Watch(ctx, id)
	if err != nil || watch.Status != domain.WatchStatusPaused || watch.NextDays != 7 {
		t.Fatalf("editing a paused watch: %+v, %v", watch, err)
	}
	item := domain.Appointment{Fingerprint: "slot", DateTime: time.Now().AddDate(0, 0, 1), ServiceName: "Konsultacja okulistyczna  - bez badania dna oka", RawPayload: "private record"}
	if _, _, err := db.UpsertAppointmentHistory(ctx, id, item); err != nil {
		t.Fatal(err)
	}
	history := request("history", "{}", origin, signed, http.StatusOK).Body.String()
	if !strings.Contains(history, "Konsultacja okulistyczna") || strings.Contains(history, "private record") {
		t.Fatalf("unexpected history response: %s", history)
	}
	request("settings", `{"Locale":"uk","CityID":1}`, origin, signed, http.StatusNoContent)
	res = request("procedures", `{"CityID":1,"Query":"дерматолога"}`, origin, signed, http.StatusOK)
	if !strings.Contains(res.Body.String(), "Консультація дерматолога") {
		t.Fatalf("unexpected translated procedure response: %s", res.Body)
	}
	res = request("load", "{}", origin, signed, http.StatusOK)
	if !strings.Contains(res.Body.String(), "Консультація дерматолога") {
		t.Fatalf("stored watch label was not localized: %s", res.Body)
	}
	history = request("history", "{}", origin, signed, http.StatusOK).Body.String()
	if !strings.Contains(history, "Консультація офтальмолога без огляду очного дна") {
		t.Fatalf("stored history label was not localized: %s", history)
	}
	watch, err = db.Watch(ctx, id)
	if err != nil || watch.ServiceName != "Konsultacja dermatologiczna" {
		t.Fatalf("stored Polish label changed: %q, %v", watch.ServiceName, err)
	}
}
