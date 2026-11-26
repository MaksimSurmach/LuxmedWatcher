package scheduler

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/maksimsurmach/luxmed-watcher/internal/domain"
	"github.com/maksimsurmach/luxmed-watcher/internal/luxmed"
	"github.com/maksimsurmach/luxmed-watcher/internal/storage"
)

// WithLuxMedClient runs a catalog operation under the same account ownership and
// upstream backoff as scheduled searches. The client is only valid in operation.
func (r *Runner) WithLuxMedClient(
	ctx context.Context,
	user domain.User,
	operation func(luxmed.Client) error,
) error {
	state, _ := r.acquireAccount(user.ID, false)
	if state == nil {
		return luxmed.ErrBrowserBusy
	}
	defer r.releaseAccount(state)
	if time.Now().Before(state.retryAfter) {
		return luxmed.ErrUnavailable
	}
	browser := r.browsers[user.TelegramUserID]
	account, err := r.db.LoginAccount(ctx, user.ID, r.browsers != nil)
	switch {
	case errors.Is(err, sql.ErrNoRows) && browser != nil:
		err = operation(browser.Client())
	case errors.Is(err, sql.ErrNoRows):
		return luxmed.ErrAuthentication
	case err == nil:
		var client luxmed.Client
		client, err = r.passwordClient(ctx, account, state)
		if err == nil {
			err = operation(client)
			err = r.recordPasswordAccessFailure(ctx, state, account, err)
		}
	}
	r.noteAccessFailure(state, err)
	return err
}

// SaveCredentials verifies a candidate within the gateway request budget. The
// revision check at commit gives a concurrent Chrome choice or logout priority.
func (r *Runner) SaveCredentials(ctx context.Context, user domain.User, creds luxmed.Credentials) error {
	return r.SaveCredentialsForAttempt(ctx, user, creds, "")
}

func (r *Runner) SaveCredentialsForAttempt(ctx context.Context, user domain.User, creds luxmed.Credentials, id string) error {
	attempt, err := r.db.StartCredentialAttempt(ctx, user, id)
	if errors.Is(err, storage.ErrCredentialAttemptBusy) {
		return luxmed.ErrBrowserBusy
	}
	if err != nil {
		return err
	}
	return r.VerifyCredentialAttempt(user, creds, attempt)
}

func (r *Runner) VerifyCredentialAttempt(user domain.User, creds luxmed.Credentials, attempt *storage.CredentialAttempt) error {
	defer attempt.Finish()
	ctx, cancel := context.WithTimeout(attempt.Context(), 15*time.Second)
	defer cancel()
	state, _ := r.acquireAccount(user.ID, false)
	if state == nil {
		return luxmed.ErrBrowserBusy
	}
	defer r.releaseAccount(state)
	if time.Now().Before(state.retryAfter) {
		return luxmed.ErrUnavailable
	}

	current, err := r.db.UserByID(ctx, user.ID)
	if err != nil {
		return fmt.Errorf("load LuxMed credential owner: %w", err)
	}
	modeChanged := current.CredentialRevision != user.CredentialRevision
	saveCanceled := current.CredentialSaveRevision != user.CredentialSaveRevision
	if modeChanged || saveCanceled || !current.IsActive() {
		return storage.ErrCredentialsChanged
	}
	if err := r.db.PrepareCredentialAttempt(ctx, r.cryptor, user, creds, attempt); err != nil {
		return err
	}
	client := r.newClient(r.rawPayloads)
	// Leave time to commit and return a response after verification.
	probeCtx, stop := context.WithTimeout(ctx, 12*time.Second)
	err = client.Authenticate(probeCtx, creds)
	if err == nil {
		_, err = client.GetCities(probeCtx)
	}
	err = errors.Join(probeCtx.Err(), err)
	stop()
	if err != nil {
		r.noteAccessFailure(state, err)
		return err
	}
	sealed, err := r.db.SaveCredentialAttempt(ctx, attempt)
	if err != nil {
		return fmt.Errorf("save verified LuxMed credentials: %w", err)
	}
	state.useCredentialRevision(user.CredentialRevision + 1)
	state.client = client
	state.encryptedPassword = sealed
	state.authenticatedAt = time.Now()
	state.recoveryPending = false
	state.rejectedReconnectWatch = nil
	state.nextProbeFilter = 0
	return nil
}

func (r *Runner) noteAccessFailure(state *accountCheck, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	state.applyRetryAfter(time.Now(), err)
}

func (r *Runner) recordPasswordAccessFailure(
	ctx context.Context,
	state *accountCheck,
	account domain.LuxMedAccount,
	err error,
) error {
	if !errors.Is(err, luxmed.ErrAuthentication) || errors.Is(err, luxmed.ErrUnavailable) {
		return err
	}
	state.client = nil
	state.recoveryPending = false
	if account.UserID != 0 && errors.Is(err, luxmed.ErrCredentialsRejected) {
		return errors.Join(err, r.recordAuthFailure(ctx, account))
	}
	return err
}
