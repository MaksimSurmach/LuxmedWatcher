package scheduler

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
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
	response = &luxmed.HTTPError{StatusCode: http.StatusTooManyRequests, RetryAfter: time.Minute}
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

	response = luxmed.ErrAuthentication
	if _, err := runner.CheckBrowserSession(ctx, user); !errors.Is(err, luxmed.ErrAuthentication) {
		t.Fatalf("manual check rejection = %v", err)
	}
	for range 2 {
		if err := runner.WithLuxMedClient(ctx, user, query); !errors.Is(err, luxmed.ErrAuthentication) {
			t.Fatalf("catalog retried rejected credentials: %v", err)
		}
		if err := runner.SaveCredentials(ctx, user, creds); !errors.Is(err, luxmed.ErrAuthentication) {
			t.Fatalf("save retried the rejected password: %v", err)
		}
	}
	account, err := db.LuxMedAccount(ctx, user.ID)
	if err != nil || account.Status != domain.AccountStatusAuthFailed || calls != 3 || client.logins != 1 {
		t.Fatalf("rejected account status=%s catalog=%d login=%d: %v", account.Status, calls, client.logins, err)
	}

	response = nil
	client.search = func(context.Context) ([]domain.Appointment, error) { return nil, nil }
	if err := db.BeginLuxMedReconnect(ctx, user.ID); err != nil {
		t.Fatal(err)
	}
	if err := runner.SaveCredentials(ctx, user, luxmed.Credentials{Login: creds.Login, Password: "replacement"}); err != nil {
		t.Fatal(err)
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
				return db.ForgetCredentials(ctx, user.ID)
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
