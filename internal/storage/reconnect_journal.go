package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type reconnectRejection struct {
	Claim            ReconnectDeliveryClaim
	RetryAt          time.Time
	ForbiddenLimit   int
	UnconfirmedClaim bool
}

type reconnectJournalKey struct {
	userID  int64
	claimID string
}

var ErrReconnectJournalUnreadable = errors.New("reconnect journal unreadable")

// SetReconnectJournalDirectory selects a persistent local directory before workers start.
func (db *DB) SetReconnectJournalDirectory(directory string) error {
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("create reconnect journal directory: %w", err)
	}
	probe, err := os.CreateTemp(directory, ".write-check-*")
	if err != nil {
		return fmt.Errorf("check reconnect journal directory: %w", err)
	}
	if err := errors.Join(probe.Close(), os.Remove(probe.Name())); err != nil {
		return fmt.Errorf("clean up reconnect journal directory check: %w", err)
	}
	db.reconnectDirectory = directory
	return nil
}

func (db *DB) JournalReconnectRejection(
	ctx context.Context,
	userID int64,
	claim ReconnectDeliveryClaim,
	retryAt time.Time,
	forbiddenLimit int,
) error {
	return db.journalReconnectOutcome(ctx, userID, reconnectRejection{Claim: claim, RetryAt: retryAt, ForbiddenLimit: forbiddenLimit})
}

func (db *DB) JournalUnsentReconnectReservation(ctx context.Context, userID int64, claim ReconnectDeliveryClaim, retryAt time.Time) error {
	return db.journalReconnectOutcome(ctx, userID, reconnectRejection{Claim: claim, RetryAt: retryAt, UnconfirmedClaim: true})
}

func (db *DB) journalReconnectOutcome(ctx context.Context, userID int64, outcome reconnectRejection) error {
	var journalErr error
	if db.reconnectDirectory != "" {
		journalErr = db.writeReconnectFile(reconnectRejectionName(userID, outcome.Claim.ID), outcome)
		if journalErr == nil {
			return db.ReplayReconnectRejection(ctx, userID)
		}
		journalErr = fmt.Errorf("write reconnect journal: %w", journalErr)
	}
	applied, err := db.persistReconnectRejection(ctx, userID, outcome)
	if err != nil || !applied {
		db.pendingRejections.Store(reconnectJournalKey{userID, outcome.Claim.ID}, outcome)
		return errors.Join(journalErr, err)
	}
	return nil
}

func (db *DB) ReplayReconnectRejection(ctx context.Context, userID int64) error {
	var replayErr error
	db.pendingRejections.Range(func(key, value any) bool {
		if key.(reconnectJournalKey).userID != userID {
			return true
		}
		applied, err := db.persistReconnectRejection(ctx, userID, value.(reconnectRejection))
		if err != nil {
			replayErr = fmt.Errorf("persist pending reconnect rejection: %w", err)
			return false
		}
		if applied {
			db.pendingRejections.CompareAndDelete(key, value)
		}
		return true
	})
	if replayErr != nil || db.reconnectDirectory == "" {
		return replayErr
	}
	entries, err := os.ReadDir(db.reconnectDirectory)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("%w: %w", ErrReconnectJournalUnreadable, err)
	}
	prefix := strconv.FormatInt(userID, 10) + "-"
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), prefix) || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		err := db.replayReconnectFile(ctx, userID, filepath.Join(db.reconnectDirectory, entry.Name()))
		if err != nil && !errors.Is(err, ErrReconnectJournalUnreadable) {
			return err
		}
		replayErr = errors.Join(replayErr, err)
	}
	return replayErr
}

func (db *DB) replayReconnectFile(ctx context.Context, userID int64, path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("%w: %w", ErrReconnectJournalUnreadable, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%w: expected a regular file", ErrReconnectJournalUnreadable)
	}
	payload, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("%w: %w", ErrReconnectJournalUnreadable, err)
	}
	var outcome reconnectRejection
	if err := json.Unmarshal(payload, &outcome); err != nil {
		return fmt.Errorf("%w: %w", ErrReconnectJournalUnreadable, err)
	}
	if outcome.Claim.ID == "" || outcome.RetryAt.IsZero() ||
		filepath.Base(path) != reconnectRejectionName(userID, outcome.Claim.ID) {
		return fmt.Errorf("%w: invalid claim ID or retry time", ErrReconnectJournalUnreadable)
	}
	applied, err := db.persistReconnectRejection(ctx, userID, outcome)
	if err != nil {
		return fmt.Errorf("persist reconnect journal: %w", err)
	}
	if !applied {
		return nil
	}
	// The SQL update is idempotent and scoped to the original delivery claim.
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("%w: %w", ErrReconnectJournalUnreadable, err)
	}
	return nil
}

func (db *DB) persistReconnectRejection(ctx context.Context, userID int64, outcome reconnectRejection) (bool, error) {
	applied, err := db.recordLuxMedReconnectRejection(ctx, userID, outcome.Claim, outcome.RetryAt, outcome.ForbiddenLimit)
	if err != nil || applied || !outcome.UnconfirmedClaim {
		return !outcome.UnconfirmedClaim || applied, err
	}
	// A missing row is inconclusive while the original reservation may still commit.
	err = db.sql.QueryRowContext(ctx, `
		SELECT EXISTS (SELECT 1 FROM luxmed_reconnect_delivery WHERE user_id=$1 AND claim_id=$2 AND retry_at IS NOT NULL)
	`, userID, outcome.Claim.ID).Scan(&applied)
	return applied, err
}

func reconnectRejectionName(userID int64, claimID string) string {
	return fmt.Sprintf("%d-%x.json", userID, claimID)
}

func (db *DB) clearPendingReconnectRejections(userID int64) {
	db.pendingRejections.Range(func(key, _ any) bool {
		if key.(reconnectJournalKey).userID == userID {
			db.pendingRejections.Delete(key)
		}
		return true
	})
}

func (db *DB) writeReconnectFile(name string, value any) error {
	if err := os.MkdirAll(db.reconnectDirectory, 0o700); err != nil {
		return err
	}
	directory, err := os.Open(db.reconnectDirectory)
	if err != nil {
		return err
	}
	defer directory.Close()

	file, err := os.CreateTemp(db.reconnectDirectory, ".rejection-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	err = json.NewEncoder(file).Encode(value)
	if err == nil {
		err = file.Sync()
	}
	if err := errors.Join(err, file.Close()); err != nil {
		return err
	}
	if err := os.Rename(file.Name(), filepath.Join(db.reconnectDirectory, name)); err != nil {
		return err
	}
	return directory.Sync()
}
