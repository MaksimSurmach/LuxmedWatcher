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

func (db *DB) SelectBrowserLogin(telegramID int64, id string, revision *int64) *ChromeChoice {
	if id == "" {
		return db.selectBrowserLoginAtRevision(telegramID, revision, false, false)
	}
	state := db.credentialOwner(telegramID)
	state.mu.Lock()
	defer state.mu.Unlock()

	choice := &ChromeChoice{telegramID: telegramID, state: state, generation: state.generation, shownRevision: revision}
	lookup := credentialAttemptKey{telegramID: telegramID, id: id}
	canceled := &CredentialAttempt{}
	value, exists := db.credentialAttempts.LoadOrStore(lookup, canceled)
	if !exists {
		db.expireCredentialAttempt(lookup, canceled)
	}
	attempt := value.(*CredentialAttempt)
	if attempt.cancel == nil {
		// The click can arrive before its own password request.
		choice.stale = state.pending != nil || state.active != nil
		return choice
	}
	if revision == nil || attempt.originRevision != *revision ||
		state.pending != nil && state.pending != attempt || state.active != nil && state.active != attempt ||
		attempt.candidate != nil && attempt.generation != state.generation {
		choice.stale = true
		return choice
	}
	attempt.cancel()
	choice.attempt = attempt
	return choice
}

// SelectBrowserLoginAtRevision applies a Telegram button to the mode it showed.
func (db *DB) SelectBrowserLoginAtRevision(telegramID int64, revision *int64, switchMode bool) *ChromeChoice {
	return db.selectBrowserLoginAtRevision(telegramID, revision, switchMode, true)
}

func (db *DB) selectBrowserLoginAtRevision(telegramID int64, revision *int64, switchMode, matchCompleted bool) *ChromeChoice {
	state := db.credentialOwner(telegramID)
	state.mu.Lock()
	defer state.mu.Unlock()
	choice := &ChromeChoice{
		telegramID: telegramID, state: state, generation: state.generation,
		shownRevision: revision, switchMode: switchMode,
	}
	if revision == nil {
		choice.stale = switchMode || state.pending != nil || state.active != nil
		return choice
	}
	if pending := state.pending; pending != nil {
		if pending.originRevision != *revision {
			choice.stale = true
			return choice
		}
		pending.cancel()
		choice.attempt = pending
	}
	// An unadmitted request cannot hide a receipt from the same revision.
	attempt := state.active
	if attempt == nil && matchCompleted {
		attempt = state.completed
	}
	if attempt != nil && attempt.originRevision == *revision && attempt.generation == state.generation {
		attempt.cancel()
		choice.attempt = attempt
	} else if choice.attempt == nil {
		choice.stale = state.active != nil
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
