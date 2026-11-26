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
	Claim          ReconnectDeliveryClaim
	RetryAt        time.Time
	ForbiddenLimit int
}

var ErrReconnectJournalUnreadable = errors.New("reconnect journal unreadable")

type TelegramUnblock struct {
	UserID   int64
	UpdateID int
}

func (event TelegramUnblock) filename() string {
	return fmt.Sprintf("unblock-%d-%d.json", event.UserID, event.UpdateID)
}

func (db *DB) QueueTelegramUnblock(ctx context.Context, event TelegramUnblock) error {
	_, dbErr := db.sql.ExecContext(ctx, `
		INSERT INTO telegram_unblocks (telegram_user_id, update_id) VALUES ($1, $2)
		ON CONFLICT (telegram_user_id, update_id) DO NOTHING
	`, event.UserID, event.UpdateID)
	if dbErr == nil {
		return nil
	}
	if db.reconnectDirectory != "" {
		err := db.writeReconnectFile(event.filename(), event)
		if err == nil {
			return nil
		}
		return errors.Join(dbErr, err)
	}
	return dbErr
}

func (db *DB) PendingTelegramUnblocks(ctx context.Context) ([]TelegramUnblock, error) {
	events := make(map[TelegramUnblock]struct{})
	dbErr := db.readQueuedTelegramUnblocks(ctx, events)
	var readErr error
	if db.reconnectDirectory != "" {
		var entries []os.DirEntry
		entries, readErr = os.ReadDir(db.reconnectDirectory)
		if errors.Is(readErr, os.ErrNotExist) {
			readErr = nil
		}
		for _, entry := range entries {
			name, isEvent := strings.CutPrefix(entry.Name(), "unblock-")
			user, update, found := strings.Cut(strings.TrimSuffix(name, ".json"), "-")
			if !isEvent || !found || entry.IsDir() {
				continue
			}
			id, idErr := strconv.ParseInt(user, 10, 64)
			seq, seqErr := strconv.Atoi(update)
			event := TelegramUnblock{UserID: id, UpdateID: seq}
			if idErr == nil && seqErr == nil && event.filename() == entry.Name() {
				events[event] = struct{}{}
			}
		}
	}
	pending := make([]TelegramUnblock, 0, len(events))
	for event := range events {
		pending = append(pending, event)
	}
	return pending, errors.Join(dbErr, readErr)
}

func (db *DB) readQueuedTelegramUnblocks(ctx context.Context, events map[TelegramUnblock]struct{}) error {
	rows, err := db.sql.QueryContext(ctx, `SELECT telegram_user_id, update_id FROM telegram_unblocks`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var event TelegramUnblock
		if err := rows.Scan(&event.UserID, &event.UpdateID); err != nil {
			return err
		}
		events[event] = struct{}{}
	}
	return rows.Err()
}

func (db *DB) FinishTelegramUnblock(ctx context.Context, event TelegramUnblock) error {
	_, err := db.sql.ExecContext(ctx, `
		DELETE FROM telegram_unblocks WHERE telegram_user_id=$1 AND update_id=$2
	`, event.UserID, event.UpdateID)
	if err != nil {
		return err
	}
	if db.reconnectDirectory != "" {
		err := os.Remove(filepath.Join(db.reconnectDirectory, event.filename()))
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
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
		journalErr = db.writeReconnectFile(strconv.FormatInt(userID, 10)+".json", outcome)
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
	if outcome.Claim.ID == "" || outcome.RetryAt.IsZero() {
		return fmt.Errorf("%w: missing claim ID or retry time", ErrReconnectJournalUnreadable)
	}
	if err := db.persistReconnectRejection(ctx, userID, outcome); err != nil {
		return fmt.Errorf("persist reconnect journal: %w", err)
	}
	// The SQL update is idempotent and scoped to the original delivery claim.
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("%w: %w", ErrReconnectJournalUnreadable, err)
	}
	return nil
}

func (db *DB) persistReconnectRejection(ctx context.Context, userID int64, outcome reconnectRejection) error {
	return db.RecordLuxMedReconnectRejection(ctx, userID, outcome.Claim, outcome.RetryAt, outcome.ForbiddenLimit)
}

func (db *DB) reconnectRejectionPath(userID int64) string {
	return filepath.Join(db.reconnectDirectory, strconv.FormatInt(userID, 10)+".json")
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
