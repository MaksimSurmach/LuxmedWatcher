package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/maksimsurmach/luxmed-watcher/internal/domain"
	"github.com/maksimsurmach/luxmed-watcher/internal/security"
)

type credentialAttemptKey struct {
	telegramID int64
	id         string
}

// CredentialAttempt identifies a single candidate without retaining its plaintext.
// It remains available briefly after commit so an overlapping Chrome click can win.
type CredentialAttempt struct {
	owner    domain.User
	previous domain.LuxMedAccount
	sealed   string
	cancel   context.CancelFunc
}

func (db *DB) BeginCredentialAttempt(
	ctx context.Context, key *security.Cryptor, owner domain.User, creds security.Credentials, id string,
) (*CredentialAttempt, context.Context, error) {
	lookup := credentialAttemptKey{telegramID: owner.TelegramUserID, id: id}
	previous, err := db.LuxMedAccount(ctx, owner.ID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, ctx, fmt.Errorf("read previous login mode: %w", err)
	}
	sealed, err := key.SealCredentials(owner.ID, creds)
	if err != nil {
		return nil, ctx, err
	}
	probe, cancel := context.WithCancel(ctx)
	attempt := &CredentialAttempt{owner: owner, previous: previous, sealed: sealed, cancel: cancel}
	if _, exists := db.credentialAttempts.LoadOrStore(lookup, attempt); exists {
		cancel()
		return nil, ctx, ErrCredentialsChanged
	}
	db.expireCredentialAttempt(lookup, attempt)
	return attempt, probe, nil
}

func (db *DB) SaveCredentialAttempt(ctx context.Context, attempt *CredentialAttempt) (string, error) {
	return db.saveSealedCredentials(ctx, attempt.owner, attempt.sealed)
}

func (attempt *CredentialAttempt) Finish() {
	attempt.cancel()
}

func (db *DB) CancelCredentialAttempt(telegramID int64, id string) *CredentialAttempt {
	if id == "" {
		return nil
	}
	lookup := credentialAttemptKey{telegramID: telegramID, id: id}
	// A click can arrive before the password request reaches the server.
	canceled := &CredentialAttempt{}
	value, exists := db.credentialAttempts.LoadOrStore(lookup, canceled)
	if !exists {
		db.expireCredentialAttempt(lookup, canceled)
		return nil
	}
	attempt := value.(*CredentialAttempt)
	if attempt.cancel != nil {
		attempt.cancel()
		return attempt
	}
	return nil
}

func (db *DB) expireCredentialAttempt(key credentialAttemptKey, attempt *CredentialAttempt) {
	// Both dashboard requests have a 19-second client budget.
	time.AfterFunc(30*time.Second, func() {
		db.credentialAttempts.CompareAndDelete(key, attempt)
	})
}
