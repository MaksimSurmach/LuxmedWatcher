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
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/maksimsurmach/luxmed-watcher/internal/domain"
	"github.com/maksimsurmach/luxmed-watcher/internal/i18n"
	"github.com/maksimsurmach/luxmed-watcher/internal/luxmed"
	"github.com/maksimsurmach/luxmed-watcher/internal/scheduler"
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
	var unauthorized atomic.Bool
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/session" {
			t.Errorf("unexpected browser route: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if unauthorized.Load() {
			w.WriteHeader(http.StatusUnauthorized)
		}
	}))
	t.Cleanup(worker.Close)
	browser, err := luxmed.NewBrowser(worker.URL, "http://localhost:18769")
	if err != nil {
		t.Fatal(err)
	}
	browsers := map[int64]*luxmed.Browser{owner.TelegramUserID: {}, other.TelegramUserID: browser}
	runner := scheduler.NewRunner(db, nil, nil, 2*time.Minute, time.Hour, false)
	runner.UseBrowsers(browsers)
	d := &dashboard{db: db, key: pub, sessions: newSessions(),
		browsers: browsers, runner: runner, checkAccount: runner.CheckBrowserSession,
		cfg: DashboardConfig{Origin: origin, BotID: 12, MinimumInterval: 2 * time.Minute}}
	failConnect := true
	d.connect = func(_ context.Context, id int64) (string, error) {
		if id != owner.TelegramUserID {
			t.Errorf("unexpected viewer owner: %d", id)
		}
		if failConnect {
			failConnect = false
			return "", luxmed.ErrUnavailable
		}
		return "https://app.example/viewer?ticket=fixture", nil
	}
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
	request := func(action, body, requestOrigin, identity string, want int, attemptID ...string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, dashboardPrefix+action, strings.NewReader(body))
		req.Header.Set("Origin", requestOrigin)
		if action == "auth" {
			req.Header.Set("X-Telegram-Init-Data", identity)
		} else {
			req.Header.Set("Authorization", "Bearer "+identity)
		}
		if action == "connect" {
			req.Header.Set("X-Telegram-Init-Data", signTelegram(owner.TelegramUserID, 12, time.Now(), priv))
		}
		if len(attemptID) != 0 {
			req.Header.Set("X-LuxMed-Login-Attempt", attemptID[0])
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
	// An open dashboard can reconnect after Telegram's launch proof expires.
	oldProof := signTelegram(owner.TelegramUserID, 12, time.Now().Add(-10*time.Minute), priv)
	for _, tc := range []struct {
		auth string
		want int
	}{
		{token, http.StatusNoContent}, {otherToken, http.StatusUnauthorized}, {"", http.StatusUnauthorized},
	} {
		req := httptest.NewRequest(http.MethodPost, dashboardPrefix+"viewer-access", nil)
		req.Header.Set("Origin", origin)
		req.Header.Set("X-Telegram-Init-Data", oldProof)
		if tc.auth != "" {
			req.Header.Set("Authorization", "Bearer "+tc.auth)
		}
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		if res.Code != tc.want {
			t.Fatalf("resume viewer access = %d, want %d", res.Code, tc.want)
		}
	}
	type accountRevisions struct {
		CredentialRevision     *int64
		CredentialSaveRevision *int64
	}
	revisions := func(identity string) string {
		t.Helper()
		view := request("load", "{}", origin, identity, http.StatusOK)
		var snapshot accountRevisions
		if err := json.Unmarshal(view.Body.Bytes(), &snapshot); err != nil ||
			snapshot.CredentialRevision == nil || snapshot.CredentialSaveRevision == nil {
			t.Fatalf("invalid account revision: %s", view.Body)
		}
		return fmt.Sprintf(`{"CredentialRevision":%d,"CredentialSaveRevision":%d}`,
			*snapshot.CredentialRevision, *snapshot.CredentialSaveRevision)
	}
	credentialBody := func(snapshot, password string) string {
		quoted, err := json.Marshal(password)
		if err != nil {
			t.Fatal(err)
		}
		return fmt.Sprintf(`%s,"Login":"patient","Password":%s}`, strings.TrimSuffix(snapshot, "}"), quoted)
	}
	legacyRevision := revisions(token)
	cryptor, err := security.NewCryptor(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	legacyPassword, err := cryptor.Encrypt("legacy fixture")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertLuxMedAccount(ctx, domain.LuxMedAccount{
		UserID: owner.ID, Login: "legacy", EncryptedPassword: legacyPassword,
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.EncryptLegacyLogins(ctx, cryptor); err != nil {
		t.Fatal(err)
	}
	request("connect", legacyRevision, origin, token, http.StatusServiceUnavailable)
	refreshed := revisions(token)
	if refreshed == legacyRevision {
		t.Fatal("failed viewer link did not publish the committed save revision")
	}
	if res := request("connect", refreshed, origin, token, http.StatusOK); !strings.Contains(res.Body.String(), "viewer?ticket=fixture") {
		t.Fatalf("Chrome viewer link is missing: %s", res.Body)
	}
	legacy, err := db.LuxMedAccount(ctx, owner.ID)
	if err != nil || legacy.UseSavedLogin || legacy.EncryptedPassword == "" {
		t.Fatalf("Chrome opening changed the migrated account: %v", err)
	}
	request("forget-credentials", revisions(token), origin, token, http.StatusNoContent)
	if _, err := db.LuxMedAccount(ctx, owner.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("explicit Mini App removal retained migrated credentials: %v", err)
	}
	const secret = "  example password  "
	d.saveLogin = func(ctx context.Context, user domain.User, creds luxmed.Credentials, id string) error {
		if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > 17*time.Second {
			t.Error("credential request does not reserve time for the gateway response")
		}
		if creds.Login != "patient" || creds.Password != secret {
			return luxmed.ErrAuthentication
		}
		if id != "" {
			attempt, err := db.StartCredentialAttempt(ctx, user, id)
			if err != nil {
				return err
			}
			defer attempt.Finish()
			if err := db.PrepareCredentialAttempt(attempt.Context(), cryptor, user, creds, attempt); err != nil {
				return err
			}
			_, err = db.SaveCredentialAttempt(attempt.Context(), attempt)
			return err
		}
		_, err := db.SaveCredentials(ctx, cryptor, user, creds)
		return err
	}
	validLogin := `{"Login":"patient","Password":"  example password  "}`
	request("credentials", validLogin, origin, "", http.StatusUnauthorized)
	request("credentials", validLogin, "https://other.example", token, http.StatusUnauthorized)
	request("credentials", `{"Login":"  ","Password":"x"}`, origin, token, http.StatusBadRequest)
	beforeCommit := revisions(token)
	request("credentials", credentialBody(beforeCommit, secret), origin, token, http.StatusNoContent, "commit-first")
	request("connect", beforeCommit, origin, token, http.StatusPreconditionFailed, "different-attempt")
	request("connect", beforeCommit, origin, token, http.StatusOK, "commit-first")
	beforeClick := revisions(token)
	request("connect", beforeClick, origin, token, http.StatusOK, "click-first")
	request("credentials", credentialBody(beforeClick, secret), origin, token, http.StatusPreconditionFailed, "delayed-other-tab")
	request("credentials", credentialBody(revisions(token), secret), origin, token, http.StatusPreconditionFailed, "click-first")
	request("credentials", validLogin, origin, token, http.StatusPreconditionFailed)
	current, err := db.UserByID(ctx, owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	chatAttempt, err := db.StartCredentialAttempt(ctx, current, "new-chat-attempt")
	if err != nil {
		t.Fatal(err)
	}
	defer chatAttempt.Finish()
	if err := db.PrepareCredentialAttempt(
		chatAttempt.Context(), cryptor, current, luxmed.Credentials{Login: "chat", Password: "fixture"}, chatAttempt,
	); err != nil {
		t.Fatal(err)
	}
	request("connect", beforeClick, origin, token, http.StatusPreconditionFailed)
	request("connect", beforeClick, origin, token, http.StatusPreconditionFailed, "new-chat-attempt")
	request("connect", fmt.Sprintf(`{"CredentialRevision":%d}`, current.CredentialRevision), origin, token, http.StatusPreconditionFailed)
	if err := chatAttempt.Context().Err(); err != nil {
		t.Fatalf("stale Chrome button canceled a newer save at the same mode revision: %v", err)
	}
	request("connect", revisions(token), origin, token, http.StatusOK)
	if !errors.Is(chatAttempt.Context().Err(), context.Canceled) {
		t.Fatal("Mini App did not cancel the active chat request")
	}
	if _, err := db.SaveCredentialAttempt(ctx, chatAttempt); !errors.Is(err, storage.ErrCredentialsChanged) {
		t.Fatalf("late chat login crossed the Mini App Chrome choice: %v", err)
	}
	chatAttempt.Finish()
	staleRevision := revisions(token)
	request("credentials", credentialBody(revisions(token), secret), origin, token, http.StatusNoContent)
	request("connect", revisions(token), origin, token, http.StatusPreconditionFailed)
	current, err = db.UserByID(ctx, owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	replacement, err := db.StartCredentialAttempt(ctx, current, "replacement")
	if err != nil {
		t.Fatal(err)
	}
	defer replacement.Finish()
	if err := db.PrepareCredentialAttempt(replacement.Context(), cryptor, current,
		luxmed.Credentials{Login: "patient", Password: secret}, replacement); err != nil {
		t.Fatal(err)
	}
	request("connect", staleRevision, origin, token, http.StatusPreconditionFailed)
	request("forget-credentials", legacyRevision, origin, token, http.StatusPreconditionFailed)
	if err := replacement.Context().Err(); err != nil {
		t.Fatalf("outdated tab canceled the replacement: %v", err)
	}
	if _, err := db.SaveCredentialAttempt(replacement.Context(), replacement); err != nil {
		t.Fatal(err)
	}
	replacement.Finish()
	request("credentials", credentialBody(revisions(token), "wrong"), origin, token, http.StatusConflict)
	request("forget-credentials", revisions(otherToken), origin, otherToken, http.StatusNoContent)
	saved, err := db.LoginAccount(ctx, owner.ID, true)
	if err != nil || saved.Login != "" || saved.EncryptedPassword == "" {
		t.Fatal("expected encrypted credentials only", err)
	}
	creds, err := cryptor.OpenCredentials(owner.ID, saved.EncryptedPassword)
	if err != nil || creds.Login != "patient" || creds.Password != secret {
		t.Fatal("original credentials were not preserved")
	}
	if _, err := cryptor.OpenCredentials(other.ID, saved.EncryptedPassword); err == nil {
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
	beforeReplacement := revisions(token)
	request("credentials", credentialBody(beforeReplacement, secret), origin, token, http.StatusNoContent, "form-replacement")
	request("forget-credentials", beforeReplacement, origin, token, http.StatusPreconditionFailed)
	request("forget-credentials", beforeReplacement, origin, token, http.StatusPreconditionFailed, "wrong-replacement")
	request("forget-credentials", beforeReplacement, origin, token, http.StatusNoContent, "form-replacement")
	if _, err := db.LuxMedAccount(ctx, owner.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("credentials retained", err)
	}
	request("connect", revisions(token), origin, token, http.StatusOK)
	if res := request("load", "{}", origin, otherToken, http.StatusOK); !strings.Contains(res.Body.String(), "Other") {
		t.Fatalf("other user's search missing: %s", res.Body)
	}
	if err := db.BeginLuxMedReconnect(ctx, other.ID); err != nil {
		t.Fatal(err)
	}
	if res := request("load", "{}", origin, otherToken, http.StatusOK); !strings.Contains(res.Body.String(), `"ReconnectRequired":true`) {
		t.Fatalf("owner's reconnect state missing: %s", res.Body)
	}
	if res := request("load", "{}", origin, token, http.StatusOK); !strings.Contains(res.Body.String(), `"AccountStatus":"unchecked"`) {
		t.Fatalf("reconnect state leaked across users: %s", res.Body)
	}
	var lastEpisodeID string
	for _, scenario := range []struct {
		status  domain.WatchStatus
		episode bool
	}{
		{domain.WatchStatusActive, true},
		{domain.WatchStatusPaused, true},
		{domain.WatchStatusActive, false},
		{domain.WatchStatusPaused, false},
	} {
		status := scenario.status
		if err := db.MarkWatchChecked(ctx, otherWatch, false, "luxmed_authentication"); err != nil {
			t.Fatal(err)
		}
		if scenario.episode {
			if err := db.BeginLuxMedReconnect(ctx, other.ID); err != nil {
				t.Fatal(err)
			}
		}
		if err := db.SetWatchStatus(ctx, otherWatch, status); err != nil {
			t.Fatal(err)
		}
		result := request("account", "{}", origin, otherToken, http.StatusOK)
		if !strings.Contains(result.Body.String(), `"state":"verification_pending","ReconnectRequired":true`) {
			t.Fatalf("account probe bypassed protected verification for %s watch: %s", status, result.Body)
		}
		var check struct{ ReconnectEpisodeID string }
		if err := json.Unmarshal(result.Body.Bytes(), &check); err != nil {
			t.Fatal(err)
		}
		if (check.ReconnectEpisodeID != "") != scenario.episode {
			t.Fatalf("probe episode ID did not match open episode: %s", result.Body)
		}
		if scenario.episode {
			if check.ReconnectEpisodeID == lastEpisodeID {
				t.Fatal("new reconnect episode reused the previous ID")
			}
			lastEpisodeID = check.ReconnectEpisodeID
		}
		result = request("load", "{}", origin, otherToken, http.StatusOK)
		var pending struct {
			Watches            []domain.Watch
			ReconnectRequired  bool
			ReconnectEpisodeID string
			AccountStatus      string
		}
		if err := json.Unmarshal(result.Body.Bytes(), &pending); err != nil || len(pending.Watches) != 1 ||
			pending.Watches[0].LastError != "luxmed_authentication" || pending.ReconnectRequired != scenario.episode ||
			pending.ReconnectEpisodeID != check.ReconnectEpisodeID || pending.AccountStatus != "verification_pending" {
			t.Fatalf("load lost the pending sign-in evidence: %s (%v)", result.Body, err)
		}
		if scenario.episode && status == domain.WatchStatusActive {
			if err := db.BeginLuxMedReconnect(ctx, other.ID); err != nil {
				t.Fatal(err)
			}
			result = request("load", "{}", origin, otherToken, http.StatusOK)
			if !strings.Contains(result.Body.String(), `"AccountStatus":"search_paused"`) ||
				!strings.Contains(result.Body.String(), `"ReconnectEpisodeID":"`+check.ReconnectEpisodeID+`"`) {
				t.Fatalf("fresh rejection did not update the same episode: %s", result.Body)
			}
		}
		// The load endpoint observes protected success without another account check.
		if err := db.ResolveLuxMedReconnect(ctx, other.ID); err != nil {
			t.Fatal(err)
		}
		result = request("load", "{}", origin, otherToken, http.StatusOK)
		if !strings.Contains(result.Body.String(), `"ReconnectRequired":false`) ||
			!strings.Contains(result.Body.String(), `"AccountStatus":"connected"`) {
			t.Fatalf("reload with a %s watch did not report recovery: %s", status, result.Body)
		}
		saved, err := db.Watch(ctx, otherWatch)
		if err != nil || saved.Status != status || saved.LastError != "" || saved.CityID != 1 || saved.ServiceID != 2 ||
			(saved.LastCheckedAt == nil) != (status == domain.WatchStatusActive) {
			t.Fatalf("unexpected watch after reconnect: %+v, %v", saved, err)
		}

		if status == domain.WatchStatusPaused {
			body := fmt.Sprintf(`{"ID":%d,"Status":"active"}`, otherWatch)
			request("status", body, origin, otherToken, http.StatusNoContent)
			saved, err = db.Watch(ctx, otherWatch)
			if err != nil || saved.Status != domain.WatchStatusActive || saved.LastError != "" {
				t.Fatalf("reactivated watch retained obsolete sign-in error: %+v, %v", saved, err)
			}
		}
	}
	unauthorized.Store(true)
	probe := request("account", "{}", origin, otherToken, http.StatusOK)
	if !strings.Contains(probe.Body.String(), `"state":"needs_login","ReconnectRequired":false`) {
		t.Fatalf("standalone 401 should request login: %s", probe.Body)
	}
	if err := db.BeginLuxMedReconnect(ctx, other.ID); err != nil {
		t.Fatal(err)
	}
	d.checkAccount = func(ctx context.Context, user domain.User) (scheduler.BrowserSessionCheck, error) {
		required, err := runner.CheckBrowserSession(ctx, user)
		// Simulate background recovery winning as soon as the probe releases the account.
		if resolveErr := db.ResolveLuxMedReconnect(ctx, user.ID); resolveErr != nil {
			t.Error(resolveErr)
		}
		return required, err
	}
	probe = request("account", "{}", origin, otherToken, http.StatusOK)
	if !strings.Contains(probe.Body.String(), `"state":"needs_login","ReconnectRequired":true`) {
		t.Fatalf("probe lost the episode it observed: %s", probe.Body)
	}
	if load := request("load", "{}", origin, otherToken, http.StatusOK); !strings.Contains(load.Body.String(), `"ReconnectRequired":false`) {
		t.Fatalf("load did not observe the subsequent recovery: %s", load.Body)
	}
	d.checkAccount = runner.CheckBrowserSession
	unauthorized.Store(false)
	if err := db.BeginLuxMedReconnect(ctx, other.ID); err != nil {
		t.Fatal(err)
	}
	episodeID, err := db.LuxMedReconnectEpisode(ctx, other.ID)
	if err != nil || episodeID == "" {
		t.Fatalf("expected an open episode: %q, %v", episodeID, err)
	}
	request("status", fmt.Sprintf(`{"ID":%d,"Status":"deleted"}`, otherWatch), origin, otherToken, http.StatusNoContent)
	for _, endpoint := range []struct{ name, statusField string }{
		{"load", "AccountStatus"},
		{"account", "state"},
	} {
		result := request(endpoint.name, "{}", origin, otherToken, http.StatusOK)
		if !strings.Contains(result.Body.String(), `"`+endpoint.statusField+`":"no_saved_searches"`) ||
			!strings.Contains(result.Body.String(), `"ReconnectEpisodeID":"`+episodeID+`"`) {
			t.Fatalf("%s lost the empty state or episode: %s", endpoint.name, result.Body)
		}
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
	request("status", fmt.Sprintf(`{"ID":%d,"Status":"active"}`, id), origin, token, http.StatusNoContent)
	watch, err = db.Watch(ctx, id)
	if err != nil || watch.Status != domain.WatchStatusActive {
		t.Fatalf("Mini App did not resume the Chrome watch locally: %+v, %v", watch, err)
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
		Days  []historyDay
		Day   string
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
	byDate := request(`{"Day":"first"}`)
	if len(byDate.Days) < 2 || byDate.Day != byDate.Days[0].Date {
		t.Fatalf("missing first day: %+v", byDate)
	}
	total := 0
	for _, day := range byDate.Days {
		total += day.Count
		selected := request(fmt.Sprintf(`{"Day":%q}`, day.Date))
		if len(selected.Slots) != min(24, day.Count) {
			t.Fatalf("day count differs from results: %+v", selected)
		}
		for _, slot := range selected.Slots {
			if slot.DateTime.In(luxmed.Location()).Format(time.DateOnly) != day.Date {
				t.Fatalf("slot on wrong Warsaw day: %+v", slot)
			}
		}
	}
	if total != len(slotIDs) {
		t.Fatalf("calendar count %d != %d", total, len(slotIDs))
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
		wantNext = []int64{slotIDs[25]}
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
	req := httptest.NewRequest(http.MethodPost, dashboardPrefix+"load", nil)
	req.Header.Set("Origin", origin)
	req.Header.Set("Authorization", "Bearer "+token)
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	var summary struct{ AvailableSlots int }
	wantCount := len(slotIDs) - 1
	if change == "returned" {
		wantCount = len(slotIDs)
	}
	if err := json.Unmarshal(res.Body.Bytes(), &summary); res.Code != http.StatusOK || err != nil || summary.AvailableSlots != wantCount {
		t.Fatalf("dashboard available slots = %d, want %d (status %d, error %v)", summary.AvailableSlots, wantCount, res.Code, err)
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
	d.runner = scheduler.NewRunner(db, nil, nil, 0, 0, false)
	d.runner.UseBrowsers(d.browsers)
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
	var failPopular atomic.Bool
	owner := addUser(24, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == luxmed.DoctorsAndFacilitiesURL && r.URL.Query().Get("cityId") == "4" &&
			r.URL.Query().Get("serviceVariantId") == "8" {
			fmt.Fprint(w, `{"doctors":[{"id":20,"firstName":"Fixture","lastName":"Doctor"}],"facilities":[{"id":21,"name":"Fixture clinic"}]}`)
			return
		}
		switch r.URL.RequestURI() {
		case luxmed.ReferralServicesURL:
			fmt.Fprint(w, `{"referrals":[{"id":43210,"serviceVariantId":6,"serviceName":"private referral label","doctor":"private doctor"},{"serviceVariantId":6,"serviceName":"duplicate"},{"serviceVariantId":98,"serviceName":"unknown"},{"id":2,"eReferralId":"private referral id","serviceName":"No procedure ID"}]}`)
		case luxmed.RecentSearchesURL:
			fmt.Fprint(w, `{"serviceVariantId":3,"searchName":"private metadata","searchParameters":[{"serviceVariantId":6,"searchName":"Already referred"},{"serviceVariantId":1,"searchName":"private search label"}]}`)
		case luxmed.PopularServicesURL:
			if failPopular.Load() {
				w.Header().Set("Retry-After", "60")
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
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
			PortalIncomplete: true,
			Saved:            []domain.Service{{ID: 3, Name: "Other user's search"}},
			Popular:          []domain.Service{{ID: 2, Name: "Popular"}},
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
			if picks.PortalIncomplete != tt.want.PortalIncomplete {
				t.Fatalf("portal incomplete = %v, want %v", picks.PortalIncomplete, tt.want.PortalIncomplete)
			}
			if !slices.Equal(picks.Referred, tt.want.Referred) || !slices.Equal(picks.Recent, tt.want.Recent) ||
				!slices.Equal(picks.Saved, tt.want.Saved) || !slices.Equal(picks.Popular, tt.want.Popular) {
				t.Fatalf("picks = %+v, want %+v", picks, tt.want)
			}
		})
	}

	requestPicks := func(t *testing.T, cityID int) procedurePicks {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, dashboardPrefix+"procedure-picks",
			strings.NewReader(fmt.Sprintf(`{"CityID":%d}`, cityID)))
		req.Header.Set("Origin", origin)
		req.Header.Set("Authorization", "Bearer "+d.sessions.issue(owner.TelegramUserID, time.Now()))
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		var picks procedurePicks
		if err := json.Unmarshal(res.Body.Bytes(), &picks); err != nil || res.Code != http.StatusOK {
			t.Fatalf("status = %d, body = %s, err = %v", res.Code, res.Body, err)
		}
		return picks
	}
	t.Run("busy account keeps a saved service without a catalog entry", func(t *testing.T) {
		const uncachedCity = 4
		if err := db.UpsertCities(ctx, []domain.City{{ID: uncachedCity, Name: "Gdańsk"}}); err != nil {
			t.Fatal(err)
		}
		if _, err := db.CreateWatch(ctx, domain.Watch{
			UserID: owner.ID, CityID: uncachedCity, ServiceID: 8, ServiceName: "Saved service",
		}); err != nil {
			t.Fatal(err)
		}
		err := d.runner.WithLuxMedClient(ctx, owner, func(luxmed.Client) error {
			picks := requestPicks(t, uncachedCity)
			want := []domain.Service{{ID: 8, Name: "Saved service"}}
			if !picks.PortalIncomplete || !slices.Equal(picks.Saved, want) {
				t.Fatalf("saved recommendations lost while busy: %+v", picks)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		for _, scenario := range []struct {
			user   domain.User
			action string
			code   int
		}{
			{user: other, action: "providers", code: http.StatusNotFound},
			{user: other, action: "watch", code: http.StatusNotFound},
			{user: owner, action: "providers", code: http.StatusOK},
			{user: owner, action: "watch", code: http.StatusNoContent},
		} {
			req := httptest.NewRequest(http.MethodPost, dashboardPrefix+scenario.action,
				strings.NewReader(`{"CityID":4,"ServiceID":8,"DoctorID":20,"FacilityIDs":[21],"NextDays":7,"CheckMinutes":30}`))
			req.Header.Set("Origin", origin)
			req.Header.Set("Authorization", "Bearer "+d.sessions.issue(scenario.user.TelegramUserID, time.Now()))
			res := httptest.NewRecorder()
			handler.ServeHTTP(res, req)
			if res.Code != scenario.code {
				t.Fatalf("%s for user %d = %d, want %d", scenario.action, scenario.user.ID, res.Code, scenario.code)
			}
			if scenario.code == http.StatusOK && (!strings.Contains(res.Body.String(), "Doctor") ||
				!strings.Contains(res.Body.String(), "Fixture clinic")) {
				t.Fatalf("providers for saved service: %s", res.Body)
			}
		}
	})
	t.Run("late portal failure keeps successful groups and saved services", func(t *testing.T) {
		addWatch(owner, 1, 2)
		failPopular.Store(true)
		picks := requestPicks(t, 1)
		if !picks.PortalIncomplete || len(picks.Referred) != 1 || len(picks.Recent) != 1 ||
			!slices.Equal(picks.Saved, []domain.Service{{ID: 2, Name: "Popular"}}) || len(picks.Popular) != 0 {
			t.Fatalf("partial recommendations lost: %+v", picks)
		}
	})
}

func TestCouponResolvesAvailableRowOutsideVisiblePage(t *testing.T) {
	ctx := t.Context()
	db, err := storage.Open(ctx, filepath.Join(t.TempDir(), "coupons.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	d := dashboard{db: db}
	owner := func(telegramID int64) domain.User {
		t.Helper()
		u, err := db.UpsertTelegramUser(ctx, domain.User{TelegramUserID: telegramID, Status: domain.UserStatusActive})
		if err != nil {
			t.Fatal(err)
		}
		return u
	}
	watch := func(u domain.User) int64 {
		t.Helper()
		id, err := db.CreateWatch(ctx, domain.Watch{UserID: u.ID, CityID: 1, ServiceID: 2})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	user := owner(1)
	first, overlap, foreign := watch(user), watch(user), watch(owner(2))
	tomorrow := time.Now().In(luxmed.Location()).AddDate(0, 0, 1)
	start := time.Date(tomorrow.Year(), tomorrow.Month(), tomorrow.Day(), 9, 0, 0, 0, luxmed.Location())
	put := func(w int64, minute int) int64 {
		t.Helper()
		row, _, err := db.UpsertAppointmentHistory(ctx, w, domain.Appointment{Fingerprint: fmt.Sprint(minute), DateTime: start.Add(time.Duration(minute) * time.Minute)})
		if err != nil {
			t.Fatal(err)
		}
		return row.ID
	}
	for i := range 25 {
		put(first, i)
	}
	last := put(first, 25)
	alias := put(overlap, 25)
	other := put(foreign, 25)
	load := func(id int64) *dashboardHistory {
		t.Helper()
		body := fmt.Sprintf(`{"Day":%q,"SlotID":%d}`, start.Format(time.DateOnly), id)
		r := httptest.NewRequest("POST", dashboardPrefix+"history", strings.NewReader(body))
		w := httptest.NewRecorder()
		d.history(w, r, user)
		var result struct {
			Linked *dashboardHistory
			Slots  []dashboardHistory
		}
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || w.Code != 200 || len(result.Slots) != 24 {
			t.Fatalf("response %d: %v", w.Code, err)
		}
		return result.Linked
	}
	for _, id := range []int64{last, alias} {
		if row := load(id); row == nil || row.ID != id {
			t.Fatalf("missed available coupon %d: %+v", id, row)
		}
	}
	if row := load(other); row != nil {
		t.Fatal("another user's coupon returned")
	}
	if err := db.HideAppointmentHistory(ctx, user.ID, last); err != nil {
		t.Fatal(err)
	}
	if row := load(last); row != nil {
		t.Fatal("hidden coupon returned")
	}
}
