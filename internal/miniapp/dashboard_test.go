package miniapp

import (
	"context"
	"crypto/ed25519"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/maksimsurmach/luxmed-watcher/internal/domain"
	"github.com/maksimsurmach/luxmed-watcher/internal/i18n"
	"github.com/maksimsurmach/luxmed-watcher/internal/luxmed"
	"github.com/maksimsurmach/luxmed-watcher/internal/security"
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
	var syncedLocales []string
	d.cfg.SyncAppMenu = func(ctx context.Context, userID int64) {
		persisted, err := db.UserByID(ctx, userID)
		if err != nil || userID != owner.ID {
			t.Errorf("unexpected chat menu recipient %d: %v", userID, err)
			return
		}
		syncedLocales = append(syncedLocales, persisted.Locale)
	}
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
	d.cfg.Cryptor, err = security.NewCryptor(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	const secret = "  example password  "
	d.verifyCredentials = func(_ context.Context, creds luxmed.Credentials) error {
		if creds.Login != "patient" || creds.Password != secret {
			return luxmed.ErrAuthentication
		}
		return nil
	}
	validLogin := `{"Login":"patient","Password":"  example password  "}`
	request("credentials", validLogin, origin, "", http.StatusUnauthorized)
	request("credentials", validLogin, "https://other.example", token, http.StatusUnauthorized)
	request("credentials", `{"Login":"  ","Password":"x"}`, origin, token, http.StatusBadRequest)
	request("credentials", validLogin, origin, token, http.StatusNoContent)
	request("credentials", `{"Login":"patient","Password":"wrong"}`, origin, token, http.StatusConflict)
	request("forget-credentials", "{}", origin, otherToken, http.StatusNoContent)
	saved, err := db.LoginAccount(ctx, owner.ID, true)
	if err != nil || saved.Login != "" || saved.EncryptedPassword == "" {
		t.Fatal("expected encrypted credentials only", err)
	}
	creds, err := d.cfg.Cryptor.OpenCredentials(owner.ID, saved.EncryptedPassword)
	if err != nil || creds.Login != "patient" || creds.Password != secret {
		t.Fatal("original credentials were not preserved")
	}
	if _, err := d.cfg.Cryptor.OpenCredentials(other.ID, saved.EncryptedPassword); err == nil {
		t.Fatal("ciphertext accepted for another user")
	}
	for _, tc := range []struct {
		token string
		saved bool
	}{{token, true}, {otherToken, false}} {
		view := request("load", "{}", origin, tc.token, http.StatusOK).Body.String()
		if !strings.Contains(view, fmt.Sprintf(`"SavedLogin":%t`, tc.saved)) || strings.Contains(view, secret) || strings.Contains(view, "patient") || strings.Contains(view, saved.EncryptedPassword) {
			t.Fatal("incorrect or unsafe account view")
		}
	}
	request("forget-credentials", "{}", origin, token, http.StatusNoContent)
	if _, err := db.LuxMedAccount(ctx, owner.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("credentials retained", err)
	}
	if res := request("load", "{}", origin, otherToken, http.StatusOK); !strings.Contains(res.Body.String(), "Other") {
		t.Fatalf("other user's search missing: %s", res.Body)
	}
	request("load", "{}", origin, d.sessions.issue(owner.TelegramUserID, time.Now().Add(-2*time.Hour)), http.StatusUnauthorized)
	request("load", "{}", origin, "", http.StatusUnauthorized)
	request("settings", `{"Locale":"pl","CityID":1}`, origin, token, http.StatusNoContent)
	request("settings", `{"Locale":"fr","CityID":1}`, origin, token, http.StatusBadRequest)
	request("settings", `{"Locale":"uk","CityID":999}`, origin, token, http.StatusNotFound)
	if len(syncedLocales) != 1 || syncedLocales[0] != "pl" {
		t.Fatalf("chat menu sync must use saved preferences, got %v", syncedLocales)
	}
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
	for _, locale := range i18n.SupportedLocales {
		request("settings", fmt.Sprintf(`{"Locale":%q,"CityID":1}`, locale), origin, token, http.StatusNoContent)
		if got := syncedLocales[len(syncedLocales)-1]; got != locale {
			t.Errorf("menu sync received %q, want %q", got, locale)
		}
		res = request("load", "{}", origin, token, http.StatusOK)
		if err := json.Unmarshal(res.Body.Bytes(), &view); err != nil {
			t.Fatal(err)
		}
		if view.Locale != locale || len(view.Watches) != 1 || view.Watches[0].ServiceName != "Konsultacja dermatologiczna" {
			t.Fatalf("locale %s or provider service missing: %s", locale, res.Body)
		}
		history = request("history", "{}", origin, token, http.StatusOK).Body.String()
		if !strings.Contains(history, item.ServiceName) {
			t.Fatalf("locale %s changed the provider service in history: %s", locale, history)
		}
	}
	res = request("procedures", `{"CityID":1,"Query":"дерматолога"}`, origin, token, http.StatusOK)
	if !strings.Contains(res.Body.String(), "Konsultacja dermatologiczna") {
		t.Fatalf("localized search did not return the provider name: %s", res.Body)
	}
	watch, err = db.Watch(ctx, id)
	if err != nil || watch.ServiceName != "Konsultacja dermatologiczna" {
		t.Fatalf("stored Polish label changed: %q, %v", watch.ServiceName, err)
	}
}

func TestDashboardHistoryKeepsPageOrderWhenAvailabilityChanges(t *testing.T) {
	for _, tt := range []struct {
		name   string
		change string
	}{
		{name: "the first time passes", change: "passes"},
		{name: "the provider removes the first time", change: "removed"},
		{name: "the provider returns the first time", change: "returned"},
		{name: "the user hides a pending time", change: "hidden"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				testDashboardHistoryPageOrder(t, tt.change)
			})
		})
	}
}

func testDashboardHistoryPageOrder(t *testing.T, change string) {
	t.Helper()
	ctx := t.Context()
	db, err := storage.Open(ctx, filepath.Join(t.TempDir(), "database.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	user, err := db.UpsertTelegramUser(ctx, domain.User{
		TelegramUserID: 24, Status: domain.UserStatusActive, Locale: "pl",
	})
	if err != nil {
		t.Fatal(err)
	}
	addWatch := func() int64 {
		t.Helper()
		id, err := db.CreateWatch(ctx, domain.Watch{UserID: user.ID, CityID: 1, ServiceID: 2})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	firstWatch, otherWatch := addWatch(), addWatch()
	now := time.Now()
	firstSlot := domain.Appointment{Fingerprint: "slot-0", DateTime: now.Add(time.Second)}
	var slotIDs []int64
	for i := range 26 {
		watchID := otherWatch
		slot := domain.Appointment{Fingerprint: fmt.Sprintf("slot-%d", i), DateTime: now.Add(time.Duration(i+1) * time.Hour)}
		if i == 0 {
			watchID = firstWatch
			slot = firstSlot
		}
		item, _, err := db.UpsertAppointmentHistory(ctx, watchID, slot)
		if err != nil {
			t.Fatal(err)
		}
		slotIDs = append(slotIDs, item.ID)
	}

	const origin = "https://app.example"
	d := &dashboard{db: db, sessions: newSessions(), cfg: DashboardConfig{Origin: origin},
		browsers: map[int64]*luxmed.Browser{user.TelegramUserID: {}}}
	handler := d.handler()
	token := d.sessions.issue(user.TelegramUserID, now)
	type page struct {
		Slots []dashboardHistory
		More  bool
		Next  json.RawMessage
	}
	request := func(body string) page {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, dashboardPrefix+"history", strings.NewReader(body))
		req.Header.Set("Origin", origin)
		req.Header.Set("Authorization", "Bearer "+token)
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		var result page
		if err := json.Unmarshal(res.Body.Bytes(), &result); err != nil || res.Code != http.StatusOK {
			t.Fatalf("history status = %d, body = %s, err = %v", res.Code, res.Body, err)
		}
		return result
	}
	markFirstMissing := func() {
		t.Helper()
		if err := db.MarkMissingAppointments(ctx, firstWatch, now.Add(time.Minute), now); err != nil {
			t.Fatal(err)
		}
	}
	wantFirst := slotIDs[0]
	wantNext := slotIDs[24:]
	if change == "returned" {
		markFirstMissing()
		wantFirst = slotIDs[1]
		wantNext = []int64{slotIDs[25], slotIDs[0]}
	}
	first := request("{}")
	if len(first.Slots) != 24 || !first.More || len(first.Next) == 0 || first.Slots[0].ID != wantFirst {
		t.Fatalf("unexpected first page: %+v", first)
	}
	switch change {
	case "removed":
		markFirstMissing()
	case "returned":
		if _, _, err := db.UpsertAppointmentHistory(ctx, firstWatch, firstSlot); err != nil {
			t.Fatal(err)
		}
	case "passes":
		time.Sleep(2 * time.Second)
	case "hidden":
		if err := db.HideAppointmentHistory(ctx, user.ID, slotIDs[24]); err != nil {
			t.Fatal(err)
		}
		wantNext = slotIDs[25:]
	}
	second := request(fmt.Sprintf(`{"After":%s}`, first.Next))
	var got []int64
	for _, slot := range second.Slots {
		got = append(got, slot.ID)
	}
	if !slices.Equal(got, wantNext) || second.More {
		t.Fatalf("next page IDs = %v, want %v, more = %v", got, wantNext, second.More)
	}
	if change == "returned" && second.Slots[1].Status == domain.HistoryStatusExpired {
		t.Fatal("returned time still has expired status")
	}
	if change == "returned" {
		fresh := request("{}")
		if len(fresh.Slots) == 0 || fresh.Slots[0].ID != slotIDs[0] {
			t.Fatalf("new view did not prioritize the returned time: %+v", fresh.Slots)
		}
	}
}

func TestDashboardProcedurePicksUseOwnSearchesAndBrowser(t *testing.T) {
	ctx := t.Context()
	db, err := storage.Open(ctx, filepath.Join(t.TempDir(), "database.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	const origin = "https://app.example"
	d := &dashboard{db: db, sessions: newSessions(), cfg: DashboardConfig{Origin: origin},
		browsers: make(map[int64]*luxmed.Browser)}
	addUser := func(id int64, serve http.HandlerFunc) domain.User {
		t.Helper()
		user, err := db.UpsertTelegramUser(ctx, domain.User{TelegramUserID: id, Status: domain.UserStatusActive, Locale: "pl"})
		if err != nil {
			t.Fatal(err)
		}
		worker := httptest.NewServer(serve)
		t.Cleanup(worker.Close)
		d.browsers[id], err = luxmed.NewBrowser(worker.URL, origin)
		if err != nil {
			t.Fatal(err)
		}
		return user
	}
	owner := addUser(24, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.RequestURI() {
		case luxmed.ReferralServicesURL:
			fmt.Fprint(w, `{"referrals":[{"id":43210,"serviceVariantId":6,"serviceName":"private referral label","doctor":"private doctor"},{"serviceVariantId":6,"serviceName":"duplicate"},{"serviceVariantId":98,"serviceName":"unknown"},{"id":2,"eReferralId":"private referral id","serviceName":"No procedure ID"}]}`)
		case luxmed.RecentSearchesURL:
			fmt.Fprint(w, `{"serviceVariantId":3,"searchName":"private metadata","searchParameters":[{"serviceVariantId":6,"searchName":"Already referred"},{"serviceVariantId":1,"searchName":"private search label"}]}`)
		case luxmed.PopularServicesURL:
			fmt.Fprint(w, `[{"serviceId":1,"serviceName":"Recent"},{"serviceId":2,"serviceName":"Upstream popular label"},{"serviceId":2,"serviceName":"Duplicate"},{"serviceId":99,"serviceName":"Unknown in this city"}]`)
		case luxmed.ServiceVariantsGroupsURL:
			fmt.Fprint(w, `[{"id":2,"name":"Popular"}]`)
		default:
			http.NotFound(w, r)
		}
	})
	other := addUser(42, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == luxmed.PopularServicesURL {
			fmt.Fprint(w, `[{"serviceId":2,"serviceName":"Popular"}]`)
			return
		}
		http.NotFound(w, r)
	})
	cities := []domain.City{{ID: 1, Name: "Warszawa"}, {ID: 2, Name: "Łódź"}, {ID: 3, Name: "Kraków"}}
	if err := db.UpsertCities(ctx, cities); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertProcedures(ctx, cities[0], []domain.Procedure{
		{ID: 1, Name: "Recent"}, {ID: 2, Name: "Popular"}, {ID: 3, Name: "Other user's search"},
		{ID: 4, Name: "Deleted search"}, {ID: 6, Name: "Referred"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertProcedures(ctx, cities[1], []domain.Procedure{{ID: 99, Name: "Other city"}}); err != nil {
		t.Fatal(err)
	}
	addWatch := func(user domain.User, cityID, serviceID int) int64 {
		t.Helper()
		id, err := db.CreateWatch(ctx, domain.Watch{UserID: user.ID, CityID: cityID, ServiceID: serviceID})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	addWatch(owner, 1, 1)
	addWatch(owner, 1, 1)
	addWatch(owner, 2, 99)
	addWatch(other, 1, 3)
	if err := db.SetWatchStatus(ctx, addWatch(owner, 1, 4), domain.WatchStatusDeleted); err != nil {
		t.Fatal(err)
	}
	handler := d.handler()
	tests := []struct {
		name   string
		user   domain.User
		cityID int
		want   procedurePicks
	}{
		{"uses only own referrals and portal searches", owner, 1, procedurePicks{
			Referred: []domain.Service{{ID: 6, Name: "Referred"}},
			Recent:   []domain.Service{{ID: 1, Name: "Recent"}},
			Popular:  []domain.Service{{ID: 2, Name: "Popular"}},
		}},
		{"preserves saved searches and popular results if private sources fail", other, 1, procedurePicks{
			Saved:   []domain.Service{{ID: 3, Name: "Other user's search"}},
			Popular: []domain.Service{{ID: 2, Name: "Popular"}},
		}},
		{"loads a city catalog on first use", owner, 3, procedurePicks{Popular: []domain.Service{{ID: 2, Name: "Popular"}}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, dashboardPrefix+"procedure-picks", strings.NewReader(fmt.Sprintf(`{"CityID":%d}`, tt.cityID)))
			req.Header.Set("Origin", origin)
			req.Header.Set("Authorization", "Bearer "+d.sessions.issue(tt.user.TelegramUserID, time.Now()))
			res := httptest.NewRecorder()
			handler.ServeHTTP(res, req)
			var picks procedurePicks
			if err := json.Unmarshal(res.Body.Bytes(), &picks); err != nil || res.Code != http.StatusOK {
				t.Fatalf("status = %d, body = %s, err = %v", res.Code, res.Body, err)
			}
			if res.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("personal suggestions must not be cached")
			}
			if strings.Contains(res.Body.String(), "private") || strings.Contains(res.Body.String(), "43210") {
				t.Fatal("personal metadata was included in recommendations")
			}
			if !slices.Equal(picks.Referred, tt.want.Referred) || !slices.Equal(picks.Recent, tt.want.Recent) ||
				!slices.Equal(picks.Saved, tt.want.Saved) || !slices.Equal(picks.Popular, tt.want.Popular) {
				t.Fatalf("picks = %+v, want %+v", picks, tt.want)
			}
		})
	}
}
