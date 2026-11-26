package storage

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/maksimsurmach/luxmed-watcher/internal/domain"
)

func (db *DB) LuxMedReconnectRequired(ctx context.Context, userID int64) (bool, error) {
	var required bool
	err := db.sql.QueryRowContext(ctx, `
		SELECT EXISTS (SELECT 1 FROM luxmed_reconnect WHERE user_id=$1)
	`, userID).Scan(&required)
	return required, err
}

func (db *DB) BeginLuxMedReconnect(ctx context.Context, userID int64) error {
	_, err := db.sql.ExecContext(ctx, `
		INSERT INTO luxmed_reconnect (user_id) VALUES ($1)
		ON CONFLICT (user_id) DO NOTHING
	`, userID)
	return err
}

// A claimed delivery can be retried only when the attempt is known to be unsuccessful.
func (db *DB) ClaimLuxMedReconnectDelivery(ctx context.Context, userID int64, now time.Time) (int, error) {
	var attempts int
	err := db.sql.QueryRowContext(ctx, `
		INSERT INTO luxmed_reconnect_delivery (user_id, attempts)
		SELECT user_id, 1 FROM luxmed_reconnect WHERE user_id=$1 AND NOT notified
		ON CONFLICT (user_id) DO UPDATE SET attempts=luxmed_reconnect_delivery.attempts+1, retry_at=NULL
		WHERE `+db.dateTimeSQL("luxmed_reconnect_delivery.retry_at")+` <= `+db.dateTimeSQL("$2")+`
		RETURNING attempts
	`, userID, now.UTC()).Scan(&attempts)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return attempts, err
}

func (db *DB) ScheduleLuxMedReconnectRetry(ctx context.Context, userID int64, retryAt time.Time) error {
	_, err := db.sql.ExecContext(ctx, `
		UPDATE luxmed_reconnect_delivery SET retry_at=$2 WHERE user_id=$1
	`, userID, retryAt.UTC())
	return err
}

func (db *DB) ReserveLuxMedSearch(ctx context.Context, userID int64, started time.Time, interval time.Duration) (bool, error) {
	res, err := db.sql.ExecContext(ctx, `
		INSERT INTO luxmed_search_cooldown (user_id, started_at) VALUES ($1, $2)
		ON CONFLICT (user_id) DO UPDATE SET started_at=excluded.started_at
		WHERE `+db.dateTimeSQL("luxmed_search_cooldown.started_at")+` <= `+db.dateTimeSQL("$3")+`
	`, userID, started.UTC(), started.Add(-interval).UTC())
	if err != nil {
		return false, err
	}
	rows, err := res.RowsAffected()
	return rows > 0, err
}

func (db *DB) ReleaseLuxMedSearch(ctx context.Context, userID int64, started time.Time) error {
	_, err := db.sql.ExecContext(ctx, `
		DELETE FROM luxmed_search_cooldown WHERE user_id=$1 AND started_at=$2
	`, userID, started.UTC())
	return err
}

func (db *DB) MarkLuxMedReconnectNotified(ctx context.Context, userID int64) error {
	_, err := db.sql.ExecContext(ctx, `UPDATE luxmed_reconnect SET notified=TRUE WHERE user_id=$1`, userID)
	return err
}

func (db *DB) ResolveLuxMedReconnect(ctx context.Context, userID int64) error {
	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	result, err := tx.ExecContext(ctx, `DELETE FROM luxmed_reconnect WHERE user_id=$1`, userID)
	if err != nil {
		return err
	}
	removed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if removed > 0 {
		// Make the saved searches eligible again without changing their filters or user-selected status.
		_, err := tx.ExecContext(ctx, `
			UPDATE watches SET last_checked_at=NULL, last_error=''
			WHERE user_id=$1 AND status=$2
		`, userID, domain.WatchStatusActive)
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}
