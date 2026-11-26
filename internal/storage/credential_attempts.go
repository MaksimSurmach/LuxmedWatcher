package storage

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/maksimsurmach/luxmed-watcher/internal/domain"
	"github.com/maksimsurmach/luxmed-watcher/internal/security"
)

var ErrCredentialAttemptBusy = errors.New("credential verification is already in progress")

type credentialAttemptKey struct {
	telegramID int64
	id         string
}

type credentialOwner struct {
	mu         sync.Mutex
	generation uint64
	active     *CredentialAttempt
}

// CredentialAttempt identifies a single candidate without retaining its plaintext.
// The receipt remains available briefly after commit for a matching Chrome click.
type CredentialAttempt struct {
	state      *credentialOwner
	generation uint64
	ctx        context.Context
	cancel     context.CancelFunc
	candidate  *credentialCandidate // Protected by state.mu.
}

type credentialCandidate struct {
	owner    domain.User
	previous domain.LuxMedAccount
	sealed   string
}

// ChromeChoice captures the attempt visible to the requesting interface before
// acknowledging the action or waiting for the database.
type ChromeChoice struct {
	telegramID int64
	state      *credentialOwner
	generation uint64
	attempt    *CredentialAttempt
	stale      bool
}

func (db *DB) credentialOwner(telegramID int64) *credentialOwner {
	if state, ok := db.credentialOwners.Load(telegramID); ok {
		return state.(*credentialOwner)
	}
	state, _ := db.credentialOwners.LoadOrStore(telegramID, &credentialOwner{})
	return state.(*credentialOwner)
}

func (db *DB) StartCredentialAttempt(ctx context.Context, telegramID int64, id string) (*CredentialAttempt, error) {
	if id == "" {
		id = rand.Text()
	}
	state := db.credentialOwner(telegramID)
	state.mu.Lock()
	defer state.mu.Unlock()

	lookup := credentialAttemptKey{telegramID: telegramID, id: id}
	if _, exists := db.credentialAttempts.Load(lookup); exists {
		return nil, ErrCredentialsChanged
	}
	if state.active != nil && state.active.ctx.Err() == nil {
		return nil, ErrCredentialAttemptBusy
	}
	probe, cancel := context.WithCancel(ctx)
	state.generation++
	attempt := &CredentialAttempt{state: state, generation: state.generation, ctx: probe, cancel: cancel}
	state.active = attempt
	db.credentialAttempts.Store(lookup, attempt)
	db.expireCredentialAttempt(lookup, attempt)
	return attempt, nil
}

func (attempt *CredentialAttempt) Context() context.Context { return attempt.ctx }

func (db *DB) PrepareCredentialAttempt(
	ctx context.Context, key *security.Cryptor, owner domain.User, creds security.Credentials, attempt *CredentialAttempt,
) error {
	if attempt.state != db.credentialOwner(owner.TelegramUserID) {
		return ErrCredentialsChanged
	}
	previous, err := db.LuxMedAccount(ctx, owner.ID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("read previous login mode: %w", err)
	}
	sealed, err := key.SealCredentials(owner.ID, creds)
	if err != nil {
		return err
	}
	attempt.state.mu.Lock()
	defer attempt.state.mu.Unlock()
	if attempt.candidate != nil {
		return ErrCredentialsChanged
	}
	if err := attempt.ctx.Err(); err != nil {
		return err
	}
	attempt.candidate = &credentialCandidate{owner: owner, previous: previous, sealed: sealed}
	return nil
}

func (db *DB) SaveCredentialAttempt(ctx context.Context, attempt *CredentialAttempt) (string, error) {
	attempt.state.mu.Lock()
	candidate := attempt.candidate
	current := attempt.state.generation == attempt.generation
	attempt.state.mu.Unlock()
	if !current || candidate == nil {
		return "", ErrCredentialsChanged
	}
	return db.saveSealedCredentials(ctx, candidate.owner, candidate.sealed)
}

func (attempt *CredentialAttempt) Finish() {
	attempt.cancel()
	attempt.state.mu.Lock()
	defer attempt.state.mu.Unlock()
	if attempt.state.active == attempt {
		attempt.state.active = nil
	}
}

func (db *DB) CancelActiveCredentialAttempt(telegramID int64) {
	state := db.credentialOwner(telegramID)
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.active != nil {
		state.active.cancel()
	}
}

func (db *DB) SelectBrowserLogin(telegramID int64, id string) *ChromeChoice {
	state := db.credentialOwner(telegramID)
	state.mu.Lock()
	defer state.mu.Unlock()

	attempt := state.active
	generation := state.generation
	var stale bool
	if id != "" {
		lookup := credentialAttemptKey{telegramID: telegramID, id: id}
		canceled := &CredentialAttempt{}
		value, exists := db.credentialAttempts.LoadOrStore(lookup, canceled)
		attempt = value.(*CredentialAttempt)
		if !exists {
			db.expireCredentialAttempt(lookup, canceled)
		}
		if attempt.cancel != nil {
			generation = attempt.generation
		} else {
			// A click may arrive before its password request. Its tombstone
			// blocks that ID, while a different active save keeps its priority.
			attempt = nil
			stale = state.active != nil
		}
	}
	if attempt != nil {
		attempt.cancel()
	}
	return &ChromeChoice{telegramID: telegramID, state: state, generation: generation, attempt: attempt, stale: stale}
}

func (db *DB) expireCredentialAttempt(key credentialAttemptKey, attempt *CredentialAttempt) {
	// Both dashboard requests have a 19-second client budget.
	time.AfterFunc(30*time.Second, func() {
		db.credentialAttempts.CompareAndDelete(key, attempt)
	})
}
