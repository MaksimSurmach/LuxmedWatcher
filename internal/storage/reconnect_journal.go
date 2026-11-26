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
	if err := db.writeReconnectRejection(userID, outcome); err != nil {
		// The primary database can still preserve the outcome if the local journal fails.
		if dbErr := db.RecordLuxMedReconnectRejection(ctx, userID, claim, retryAt, forbiddenLimit); dbErr != nil {
			return errors.Join(fmt.Errorf("write reconnect journal: %w", err), dbErr)
		}
		return nil
	}
	return db.ReplayReconnectRejection(ctx, userID)
}

func (db *DB) ReplayReconnectRejection(ctx context.Context, userID int64) error {
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
	err = db.RecordLuxMedReconnectRejection(
		ctx, userID, outcome.Claim, outcome.RetryAt, outcome.ForbiddenLimit,
	)
	if err != nil {
		return fmt.Errorf("persist reconnect journal: %w", err)
	}
	// The SQL update is idempotent and scoped to the original delivery claim.
	return os.Remove(path)
}

func (db *DB) reconnectRejectionPath(userID int64) string {
	return filepath.Join(db.reconnectDirectory, strconv.FormatInt(userID, 10)+".json")
}

func (db *DB) writeReconnectRejection(userID int64, outcome reconnectRejection) error {
	if db.reconnectDirectory == "" {
		return errors.New("reconnect journal directory is unset")
	}
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
