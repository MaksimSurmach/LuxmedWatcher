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
	pending    *CredentialAttempt
	active     *CredentialAttempt
	completed  *CredentialAttempt
}

// CredentialAttempt identifies a single candidate without retaining its plaintext.
// The receipt remains available briefly after commit for a matching Chrome click.
type CredentialAttempt struct {
	state          *credentialOwner
	originRevision int64
	generation     uint64
	ctx            context.Context
	cancel         context.CancelFunc
	candidate      *credentialCandidate // Protected by state.mu.
}

type credentialCandidate struct {
	owner    domain.User
	previous domain.LuxMedAccount
	sealed   string
}

// ChromeChoice captures the attempt visible to the requesting interface before
// acknowledging the action or waiting for the database.
type ChromeChoice struct {
	telegramID    int64
	state         *credentialOwner
	generation    uint64
	attempt       *CredentialAttempt
	stale         bool
	shownRevision *int64
	switchMode    bool
}

func (db *DB) credentialOwner(telegramID int64) *credentialOwner {
	if state, ok := db.credentialOwners.Load(telegramID); ok {
		return state.(*credentialOwner)
	}
	state, _ := db.credentialOwners.LoadOrStore(telegramID, &credentialOwner{})
	return state.(*credentialOwner)
}

func (db *DB) StartCredentialAttempt(ctx context.Context, user domain.User, id string) (*CredentialAttempt, error) {
	if id == "" {
		id = rand.Text()
	}
	telegramID := user.TelegramUserID
	state := db.credentialOwner(telegramID)
	state.mu.Lock()
	defer state.mu.Unlock()

	lookup := credentialAttemptKey{telegramID: telegramID, id: id}
	if _, exists := db.credentialAttempts.Load(lookup); exists {
		return nil, ErrCredentialsChanged
	}
	if state.pending != nil && state.pending.ctx.Err() == nil ||
		state.active != nil && state.active.ctx.Err() == nil {
		return nil, ErrCredentialAttemptBusy
	}
	probe, cancel := context.WithCancel(ctx)
	attempt := &CredentialAttempt{state: state, originRevision: user.CredentialRevision, ctx: probe, cancel: cancel}
	state.pending = attempt
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
	if attempt.state.pending != attempt || attempt.originRevision != owner.CredentialRevision {
		return ErrCredentialsChanged
	}
	if err := attempt.ctx.Err(); err != nil {
		return err
	}
	attempt.state.generation++
	attempt.generation = attempt.state.generation
	attempt.state.pending = nil
	attempt.state.active = attempt
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
	sealed, err := db.saveSealedCredentials(ctx, candidate.owner, candidate.sealed)
	if err == nil {
		attempt.state.mu.Lock()
		if attempt.state.generation == attempt.generation {
			attempt.state.completed = attempt
		}
		attempt.state.mu.Unlock()
	}
	return sealed, err
}

func (attempt *CredentialAttempt) Finish() {
	attempt.cancel()
	attempt.state.mu.Lock()
	defer attempt.state.mu.Unlock()
	if attempt.state.pending == attempt {
		attempt.state.pending = nil
	}
	if attempt.state.active == attempt {
		attempt.state.active = nil
	}
}

func (db *DB) CancelActiveCredentialAttempt(telegramID int64) {
	state := db.credentialOwner(telegramID)
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.pending != nil {
		state.pending.cancel()
	}
	if state.active != nil {
		state.active.cancel()
	}
}

func (db *DB) SelectBrowserLogin(telegramID int64, id string) *ChromeChoice {
	state := db.credentialOwner(telegramID)
	state.mu.Lock()
	defer state.mu.Unlock()

	attempt := state.active
	if id == "" && state.pending != nil {
		state.pending.cancel()
		if attempt == nil {
			attempt = state.pending
		}
	}
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
			if attempt.candidate != nil {
				generation = attempt.generation
			} else {
				stale = state.active != nil || (state.pending != nil && state.pending != attempt)
			}
		} else {
			// A click may arrive before its password request. Its tombstone
			// blocks that ID, while a different active save keeps its priority.
			attempt = nil
			stale = state.pending != nil || state.active != nil
		}
	}
	if attempt != nil {
		attempt.cancel()
	}
	return &ChromeChoice{telegramID: telegramID, state: state, generation: generation, attempt: attempt, stale: stale}
}

// SelectBrowserLoginAtRevision applies a Telegram button to the mode it showed.
// An unversioned legacy button has no authority over an overlapping save.
func (db *DB) SelectBrowserLoginAtRevision(telegramID int64, revision *int64, switchMode bool) *ChromeChoice {
	state := db.credentialOwner(telegramID)
	state.mu.Lock()
	defer state.mu.Unlock()
	choice := &ChromeChoice{
		telegramID: telegramID, state: state, generation: state.generation,
		shownRevision: revision, switchMode: switchMode,
	}
	if revision == nil {
		choice.stale = state.pending != nil || state.active != nil
		return choice
	}
	if state.pending != nil && state.pending.originRevision == *revision {
		state.pending.cancel()
		choice.attempt = state.pending
	}
	for _, attempt := range []*CredentialAttempt{state.active, state.completed} {
		if attempt == nil || attempt.originRevision != *revision {
			continue
		}
		if attempt.candidate != nil && attempt.generation != state.generation {
			continue
		}
		attempt.cancel()
		choice.attempt = attempt
		break
	}
	return choice
}

func (db *DB) expireCredentialAttempt(key credentialAttemptKey, attempt *CredentialAttempt) {
	// Both dashboard requests have a 19-second client budget.
	time.AfterFunc(30*time.Second, func() {
		db.credentialAttempts.CompareAndDelete(key, attempt)
		if attempt.state != nil {
			attempt.state.mu.Lock()
			if attempt.state.completed == attempt {
				attempt.state.completed = nil
			}
			attempt.state.mu.Unlock()
		}
	})
}
