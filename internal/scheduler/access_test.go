package scheduler

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/maksimsurmach/luxmed-watcher/internal/domain"
	"github.com/maksimsurmach/luxmed-watcher/internal/luxmed"
	"github.com/maksimsurmach/luxmed-watcher/internal/storage"
)

type catalogClient struct {
	fakeLuxMed
	cities func(context.Context) error
}

func (c *catalogClient) GetCities(ctx context.Context) ([]domain.City, error) {
	return nil, c.cities(ctx)
}

func TestCatalogManualCheckAndSaveShareAuthenticationAndRetryAfter(t *testing.T) {
	ctx := t.Context()
	runner, db, watch := testRunner(t, nil)
	user, err := db.UserByID(ctx, watch.UserID)
	if err != nil {
		t.Fatal(err)
	}
	runner.UseBrowsers(map[int64]*luxmed.Browser{user.TelegramUserID: {}})
	creds := luxmed.Credentials{Login: "fixture", Password: "synthetic"}
	var calls int
	var response error
	client := &catalogClient{cities: func(context.Context) error {
		calls++
		return response
	}}
	runner.newClient = func(bool) luxmed.Client { return client }
	if err := runner.SaveCredentials(ctx, user, creds); err != nil {
		t.Fatal(err)
	}
	user, err = db.UserByID(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	query := func(client luxmed.Client) error {
		_, err := client.GetCities(ctx)
		return err
	}
	response = errors.Join(
		&luxmed.HTTPError{StatusCode: http.StatusNotFound},
		&luxmed.HTTPError{StatusCode: http.StatusServiceUnavailable, RetryAfter: time.Minute},
	)
	if err := runner.WithLuxMedClient(ctx, user, query); !errors.Is(err, luxmed.ErrUnavailable) {
		t.Fatalf("catalog throttle = %v", err)
	}
	if _, err := runner.CheckBrowserSession(ctx, user); !errors.Is(err, luxmed.ErrUnavailable) {
		t.Fatalf("manual check during Retry-After = %v", err)
	}
	if err := runner.SaveCredentials(ctx, user, creds); !errors.Is(err, luxmed.ErrUnavailable) {
		t.Fatalf("credential save during Retry-After = %v", err)
	}
	if _, err := runner.ManualCheck(ctx, watch); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("scheduled search during Retry-After = %v", err)
	}
	if calls != 2 || client.logins != 1 {
		t.Fatalf("upstream during Retry-After: catalog=%d login=%d", calls, client.logins)
	}
	runner.accounts[user.ID].retryAfter = time.Time{}

	response = luxmed.ErrCredentialsRejected
	if _, err := runner.CheckBrowserSession(ctx, user); !errors.Is(err, luxmed.ErrAuthentication) {
		t.Fatalf("manual check rejection = %v", err)
	}
	for range 2 {
		if err := runner.WithLuxMedClient(ctx, user, query); !errors.Is(err, luxmed.ErrAuthentication) {
			t.Fatalf("catalog retried rejected credentials: %v", err)
		}
	}
	account, err := db.LuxMedAccount(ctx, user.ID)
	if err != nil || account.Status != domain.AccountStatusAuthFailed || calls != 3 || client.logins != 1 {
		t.Fatalf("rejected account status=%s catalog=%d login=%d: %v", account.Status, calls, client.logins, err)
	}

	response = nil
	client.search = func(context.Context) ([]domain.Appointment, error) { return nil, nil }
	if err := runner.SaveCredentials(ctx, user, creds); err != nil {
		t.Fatal(err)
	}
	if client.logins != 2 {
		t.Fatalf("explicit retry did not authenticate the same pair: %d", client.logins)
	}
	user, err = db.UserByID(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, access, err := db.WatchesWithReconnectState(ctx, user.ID)
	if err != nil || access.Status != storage.LuxMedAccessPending || access.EpisodeID != "" {
		t.Fatalf("dictionary access changed protected search status: %+v, %v", access, err)
	}

	if result, err := runner.CheckBrowserSession(ctx, user); err != nil || !result.VerificationPending {
		t.Fatalf("dictionary check should leave the reconnect episode open: %+v, %v", result, err)
	}
	if _, err := runner.ManualCheck(ctx, watch); err != nil {
		t.Fatal(err)
	}
	if required, err := db.LuxMedReconnectRequired(ctx, user.ID); err != nil || required {
		t.Fatalf("successful protected search should resolve the episode: required=%v, err=%v", required, err)
	}
}

func TestCredentialVerificationCannotCommitAfterDeadlineOrChromeChoice(t *testing.T) {
	for _, name := range []string{"deadline", "Chrome choice"} {
		t.Run(name, func(t *testing.T) {
			ctx := t.Context()
			runner, db, watch := testRunner(t, nil)
			user, err := db.UserByID(ctx, watch.UserID)
			if err != nil {
				t.Fatal(err)
			}
			// Legacy credentials must never opt the account into saved-login mode.
			runner.UseBrowsers(map[int64]*luxmed.Browser{user.TelegramUserID: {}})
			client := &catalogClient{cities: func(probe context.Context) error {
				deadline, ok := probe.Deadline()
				if !ok || time.Until(deadline) > 12*time.Second {
					t.Errorf("verification did not receive a bounded deadline")
				}
				if name == "deadline" {
					<-probe.Done()
					// A transport can return a result while cancellation propagates.
					return nil
				}
				return db.ForgetCredentials(ctx, user)
			}}
			runner.newClient = func(bool) luxmed.Client { return client }
			want := storage.ErrCredentialsChanged
			if name == "deadline" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, 500*time.Millisecond)
				defer cancel()
				want = context.DeadlineExceeded
			}
			err = runner.SaveCredentials(ctx, user, luxmed.Credentials{Login: "fixture", Password: "synthetic"})
			if !errors.Is(err, want) {
				t.Fatalf("save = %v, want %v", err, want)
			}
			if _, err := db.LoginAccount(t.Context(), user.ID, true); !errors.Is(err, sql.ErrNoRows) {
				t.Fatalf("expected Chrome mode after failed verification: %v", err)
			}
			current, err := db.Watch(t.Context(), watch.ID)
			if err != nil || current.Status != domain.WatchStatusActive {
				t.Fatalf("original watch status=%s: %v", current.Status, err)
			}
		})
	}
}

func TestPasswordSearchResultCannotCrossChromeSwitch(t *testing.T) {
	for _, rejection := range []bool{false, true} {
		name := "successful password search"
		if rejection {
			name = "rejected password search"
		}
		t.Run(name, func(t *testing.T) {
			ctx := t.Context()
			runner, db, watch := testRunner(t, nil)
			user, err := db.UserByID(ctx, watch.UserID)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := db.SaveCredentials(ctx, runner.cryptor, user, luxmed.Credentials{
				Login: "fixture", Password: "synthetic",
			}); err != nil {
				t.Fatal(err)
			}
			user, err = db.UserByID(ctx, user.ID)
			if err != nil {
				t.Fatal(err)
			}
			runner.UseBrowsers(map[int64]*luxmed.Browser{user.TelegramUserID: {}})
			if err := db.BeginLuxMedReconnect(ctx, user.ID); err != nil {
				t.Fatal(err)
			}
			episode, err := db.LuxMedReconnectEpisode(ctx, user.ID)
			if err != nil || episode == "" {
				t.Fatalf("seed reconnect episode: %q, %v", episode, err)
			}
			runner.newClient = func(bool) luxmed.Client {
				return &fakeLuxMed{search: func(context.Context) ([]domain.Appointment, error) {
					if err := db.ForgetCredentials(ctx, user); err != nil {
						t.Fatal(err)
					}
					if rejection {
						return nil, luxmed.ErrAuthentication
					}
					return []domain.Appointment{{Fingerprint: "stale", DateTime: time.Now().Add(time.Hour)}}, nil
				}}
			}

			if _, err := runner.ManualCheck(ctx, watch); !errors.Is(err, ErrInactive) {
				t.Fatalf("old check was accepted: %v", err)
			}
			watches, access, err := db.WatchesWithReconnectState(ctx, user.ID)
			if err != nil || access.Status != storage.LuxMedAccessPending || access.EpisodeID != episode {
				t.Fatalf("old check altered Chrome status or episode: %+v, %v", access, err)
			}
			if watches[0].LastCheckedAt != nil || watches[0].LastError != "" {
				t.Fatalf("old check changed the watch: %+v", watches[0])
			}
			slots, err := db.RecentHistory(ctx, user.ID, 10, 0)
			if err != nil || len(slots) != 0 {
				t.Fatalf("old check persisted slots: %+v, %v", slots, err)
			}
		})
	}
}

func TestSearchAccessRejectionAllowsExplicitPasswordRetry(t *testing.T) {
	ctx := t.Context()
	runner, db, watch := testRunner(t, nil)
	user, err := db.UserByID(ctx, watch.UserID)
	if err != nil {
		t.Fatal(err)
	}
	creds := luxmed.Credentials{Login: "fixture", Password: "synthetic"}
	client := &catalogClient{cities: func(context.Context) error { return nil }}
	client.search = func(context.Context) ([]domain.Appointment, error) { return nil, luxmed.ErrAuthentication }
	runner.newClient = func(bool) luxmed.Client { return client }
	if err := runner.SaveCredentials(ctx, user, creds); err != nil {
		t.Fatal(err)
	}
	if _, err := runner.ManualCheck(ctx, watch); !errors.Is(err, luxmed.ErrAuthentication) {
		t.Fatalf("protected endpoint rejection = %v", err)
	}
	account, err := db.LuxMedAccount(ctx, user.ID)
	if err != nil || account.Status != domain.AccountStatusActive {
		t.Fatalf("search rejection mislabeled the password: %+v, %v", account.Status, err)
	}
	user, err = db.UserByID(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.SaveCredentials(ctx, user, creds); err != nil {
		t.Fatal(err)
	}
	client.search = func(context.Context) ([]domain.Appointment, error) { return nil, nil }
	if _, err := runner.ManualCheck(ctx, watch); err != nil {
		t.Fatal(err)
	}
	watches, access, err := db.WatchesWithReconnectState(ctx, user.ID)
	if err != nil || access.Status != storage.LuxMedAccessConnected ||
		access.EpisodeID != "" || watches[0].Status != domain.WatchStatusActive {
		t.Fatalf("retry did not restore search access: %+v, %+v, %v", watches, access, err)
	}
}

func TestPausedSearchLoginBackoffExpiresWithoutReplacingCredentials(t *testing.T) {
	ctx := t.Context()
	runner, db, watch := testRunner(t, nil)
	watch.CityID, watch.ServiceID = 1, 2
	if err := db.UpdateWatch(ctx, watch); err != nil {
		t.Fatal(err)
	}
	if err := db.SetWatchStatus(ctx, watch.ID, domain.WatchStatusPaused); err != nil {
		t.Fatal(err)
	}
	if err := db.MarkWatchChecked(ctx, watch.ID, false, "luxmed_authentication"); err != nil {
		t.Fatal(err)
	}
	user, err := db.UserByID(ctx, watch.UserID)
	if err != nil {
		t.Fatal(err)
	}
	account, err := db.LuxMedAccount(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	account.Status = domain.AccountStatusActive
	if err := db.UpdateLuxMedAccountAuth(ctx, account); err != nil {
		t.Fatal(err)
	}
	client := &catalogClient{
		fakeLuxMed: fakeLuxMed{authErr: errors.Join(luxmed.ErrUnavailable, &luxmed.HTTPError{
			StatusCode: http.StatusForbidden, RetryAfter: time.Minute,
		})},
		cities: func(context.Context) error { return nil },
	}
	client.search = func(context.Context) ([]domain.Appointment, error) { return nil, nil }
	runner.newClient = func(bool) luxmed.Client { return client }
	if err := runner.renewBrowserSession(ctx, user.TelegramUserID, nil); !errors.Is(err, luxmed.ErrUnavailable) {
		t.Fatalf("paused search login = %v", err)
	}
	client.authErr = nil
	creds := luxmed.Credentials{Login: "fixture", Password: "synthetic"}
	if err := runner.SaveCredentials(ctx, user, creds); !errors.Is(err, luxmed.ErrUnavailable) || client.logins != 1 {
		t.Fatalf("save bypassed the rejected login's Retry-After: err=%v logins=%d", err, client.logins)
	}
	account, err = db.LuxMedAccount(ctx, user.ID)
	if err != nil || account.Status != domain.AccountStatusActive {
		t.Fatalf("temporary response invalidated saved credentials: %s, %v", account.Status, err)
	}
	runner.accounts[user.ID].retryAfter = time.Time{}
	runner.accounts[user.ID].next = time.Time{}
	if err := db.SetWatchStatus(ctx, watch.ID, domain.WatchStatusActive); err != nil {
		t.Fatal(err)
	}
	if _, err := runner.ManualCheck(ctx, watch); err != nil || client.logins != 2 {
		t.Fatalf("search did not resume after backoff: err=%v logins=%d", err, client.logins)
	}

}

func TestOpeningChromeKeepsInFlightChromeSearch(t *testing.T) {
	ctx := t.Context()
	runner, db, watch := testRunner(t, nil)
	// The legacy account exists but is not selected when a Chrome worker is assigned.
	watch.CityID, watch.ServiceID = 1, 2
	if err := db.UpdateWatch(ctx, watch); err != nil {
		t.Fatal(err)
	}
	user, err := db.UserByID(ctx, watch.UserID)
	if err != nil {
		t.Fatal(err)
	}
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path != luxmed.TermsURL {
			t.Errorf("unexpected Chrome request: %s", req.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if err := db.ConfirmBrowserLogin(req.Context(), user); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		fmt.Fprint(w, `{"termsForService":{"termsForDays":[]}}`)
	}))
	t.Cleanup(worker.Close)
	browser, err := luxmed.NewBrowser(worker.URL, "http://localhost:18769")
	if err != nil {
		t.Fatal(err)
	}
	runner.UseBrowsers(map[int64]*luxmed.Browser{user.TelegramUserID: browser})
	runner.minimumInterval = time.Minute

	if _, err := runner.ManualCheck(ctx, watch); err != nil {
		t.Fatalf("Chrome search was interrupted: %v", err)
	}
	current, err := db.Watch(ctx, watch.ID)
	if err != nil || current.LastSuccessAt == nil {
		t.Fatalf("Chrome search result was discarded: %+v, %v", current, err)
	}
	if _, err := runner.ManualCheck(ctx, watch); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("Chrome search cooldown was lost: %v", err)
	}
}

func TestIncompleteLoginCanRecoverDuringScheduledSearch(t *testing.T) {
	ctx := t.Context()
	runner, db, watch := testRunner(t, nil)
	client := &fakeLuxMed{
		authErr: luxmed.ErrResponse,
		search:  func(context.Context) ([]domain.Appointment, error) { return nil, nil },
	}
	runner.newClient = func(bool) luxmed.Client { return client }
	if err := runner.CheckWatch(ctx, watch); !errors.Is(err, luxmed.ErrResponse) {
		t.Fatalf("incomplete login returned %v", err)
	}
	account, err := db.LuxMedAccount(ctx, watch.UserID)
	if err != nil || account.Status == domain.AccountStatusAuthFailed {
		t.Fatalf("incomplete login disabled saved credentials: status=%v err=%v", account.Status, err)
	}
	client.authErr = nil
	if err := runner.CheckWatch(ctx, watch); err != nil || client.logins != 2 {
		t.Fatalf("search did not recover with the saved password: attempts=%d err=%v", client.logins, err)
	}
}

func TestRejectedPasswordSubmissionKeepsAcceptedChromeChoice(t *testing.T) {
	for _, busy := range []bool{true, false} {
		name := "stale revision"
		if busy {
			name = "runner busy"
		}
		t.Run(name, func(t *testing.T) {
			ctx := t.Context()
			runner, db, watch := testRunner(t, nil)
			user, err := db.UserByID(ctx, watch.UserID)
			if err != nil {
				t.Fatal(err)
			}
			choice := db.SelectBrowserLoginAtRevision(user.TelegramUserID, &user.CredentialRevision, false)
			submitted := user
			expected := storage.ErrCredentialsChanged
			if busy {
				state, _ := runner.acquireAccount(user.ID, false)
				if state == nil {
					t.Fatal("could not acquire test account")
				}
				defer runner.releaseAccount(state)
				expected = luxmed.ErrBrowserBusy
			} else {
				submitted.CredentialSaveRevision++
			}
			creds := luxmed.Credentials{Login: "fixture", Password: "synthetic"}
			if err := runner.SaveCredentialsForAttempt(ctx, submitted, creds, "rejected"); !errors.Is(err, expected) {
				t.Fatalf("submission returned %v, want %v", err, expected)
			}
			if err := db.ChooseBrowserLogin(ctx, user, choice); err != nil {
				t.Fatalf("a rejected submission invalidated the accepted Chrome click: %v", err)
			}
			current, err := db.UserByID(ctx, user.ID)
			if err != nil {
				t.Fatal(err)
			}
			retry := db.SelectBrowserLogin(user.TelegramUserID, "rejected", &user.CredentialRevision, false)
			if err := db.ChooseBrowserLogin(ctx, current, retry); err != nil {
				t.Fatalf("a rejected submission blocked reopening the current Chrome mode: %v", err)
			}
		})
	}
}
