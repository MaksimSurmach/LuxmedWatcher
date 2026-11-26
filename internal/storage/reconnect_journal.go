package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

type reconnectRejection struct {
	Claim          ReconnectDeliveryClaim
	RetryAt        time.Time
	ForbiddenLimit int
}

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
	outcome := reconnectRejection{Claim: claim, RetryAt: retryAt, ForbiddenLimit: forbiddenLimit}
	var journalErr error
	if db.reconnectDirectory != "" {
		journalErr = db.writeReconnectRejection(userID, outcome)
		if journalErr == nil {
			return db.ReplayReconnectRejection(ctx, userID)
		}
		journalErr = fmt.Errorf("write reconnect journal: %w", journalErr)
	}
	if err := db.persistReconnectRejection(ctx, userID, outcome); err != nil {
		db.pendingRejections.Store(userID, outcome)
		return errors.Join(journalErr, err)
	}
	return nil
}

func (db *DB) ReplayReconnectRejection(ctx context.Context, userID int64) error {
	if pending, ok := db.pendingRejections.Load(userID); ok {
		if err := db.persistReconnectRejection(ctx, userID, pending.(reconnectRejection)); err != nil {
			return fmt.Errorf("persist pending reconnect rejection: %w", err)
		}
		db.pendingRejections.CompareAndDelete(userID, pending)
	}
	if db.reconnectDirectory == "" {
		return nil
	}
	path := db.reconnectRejectionPath(userID)
	payload, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read reconnect journal: %w", err)
	}
	var outcome reconnectRejection
	if err := json.Unmarshal(payload, &outcome); err != nil {
		return fmt.Errorf("decode reconnect journal: %w", err)
	}
	if err := db.persistReconnectRejection(ctx, userID, outcome); err != nil {
		return fmt.Errorf("persist reconnect journal: %w", err)
	}
	// The SQL update is idempotent and scoped to the original delivery claim.
	return os.Remove(path)
}

func (db *DB) persistReconnectRejection(ctx context.Context, userID int64, outcome reconnectRejection) error {
	return db.RecordLuxMedReconnectRejection(ctx, userID, outcome.Claim, outcome.RetryAt, outcome.ForbiddenLimit)
}

func (db *DB) reconnectRejectionPath(userID int64) string {
	return filepath.Join(db.reconnectDirectory, strconv.FormatInt(userID, 10)+".json")
}

func (db *DB) writeReconnectRejection(userID int64, outcome reconnectRejection) error {
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
	err = json.NewEncoder(file).Encode(outcome)
	if err == nil {
		err = file.Sync()
	}
	if err := errors.Join(err, file.Close()); err != nil {
		return err
	}
	if err := os.Rename(file.Name(), db.reconnectRejectionPath(userID)); err != nil {
		return err
	}
	return directory.Sync()
}
