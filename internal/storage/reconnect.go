package storage

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"time"

	"github.com/maksimsurmach/luxmed-watcher/internal/domain"
)

const reconnectRequiredQuery = `SELECT EXISTS (SELECT 1 FROM luxmed_reconnect WHERE user_id=$1)`

func (db *DB) LuxMedReconnectRequired(ctx context.Context, userID int64) (bool, error) {
	var required bool
	err := db.sql.QueryRowContext(ctx, reconnectRequiredQuery, userID).Scan(&required)
	return required, err
}

func (db *DB) WatchesWithReconnectState(ctx context.Context, userID int64) ([]domain.Watch, bool, error) {
	tx, err := db.sql.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback()

	watches, err := watchesByUser(ctx, tx, userID)
	if err != nil {
		return nil, false, err
	}
	var required bool
	if err := tx.QueryRowContext(ctx, reconnectRequiredQuery, userID).Scan(&required); err != nil {
		return nil, false, err
	}
	if err := tx.Commit(); err != nil {
		return nil, false, err
	}
	return watches, required, nil
}

func (db *DB) BeginLuxMedReconnect(ctx context.Context, userID int64) error {
	_, err := db.sql.ExecContext(ctx, `
		INSERT INTO luxmed_reconnect (user_id)
		SELECT $1 WHERE EXISTS (SELECT 1 FROM watches WHERE user_id=$1 AND status=$2)
		ON CONFLICT (user_id) DO NOTHING
	`, userID, domain.WatchStatusActive)
	return err
}

type ReconnectDeliveryClaim struct {
	ID      string
	Attempt int
}

// A claimed delivery can be retried only when the attempt is known to be unsuccessful.
func (db *DB) ClaimLuxMedReconnectDelivery(ctx context.Context, userID int64, now time.Time) (ReconnectDeliveryClaim, error) {
	claim := ReconnectDeliveryClaim{ID: rand.Text()}
	err := db.sql.QueryRowContext(ctx, `
		INSERT INTO luxmed_reconnect_delivery (user_id, attempts, claim_id)
		SELECT user_id, 1, $3 FROM luxmed_reconnect WHERE user_id=$1 AND NOT notified
		    AND EXISTS (SELECT 1 FROM watches WHERE user_id=$1 AND status=$4)
		ON CONFLICT (user_id) DO UPDATE SET attempts=luxmed_reconnect_delivery.attempts+1, retry_at=NULL, claim_id=excluded.claim_id
		WHERE `+db.dateTimeSQL("luxmed_reconnect_delivery.retry_at")+` <= `+db.dateTimeSQL("$2")+`
		RETURNING attempts
	`, userID, now.UTC(), claim.ID, domain.WatchStatusActive).Scan(&claim.Attempt)
	if errors.Is(err, sql.ErrNoRows) {
		return ReconnectDeliveryClaim{}, nil
	}
	return claim, err
}

// A nonzero forbidden limit records a 403 and suppresses further retries when the episode reaches the limit.
func (db *DB) RecordLuxMedReconnectRejection(
	ctx context.Context,
	userID int64,
	claim ReconnectDeliveryClaim,
	retryAt time.Time,
	forbiddenLimit int,
) error {
	if claim.ID == "" {
		return errors.New("missing reconnect delivery claim ID")
	}
	_, err := db.sql.ExecContext(ctx, `
		UPDATE luxmed_reconnect_delivery
		SET forbidden_failures=forbidden_failures + CASE WHEN $3 > 0 THEN 1 ELSE 0 END,
		    retry_at=CASE WHEN $3 > 0 AND forbidden_failures + 1 >= $3
		                  THEN NULL ELSE COALESCE($2, retry_at) END
		WHERE user_id=$1 AND claim_id=$4 AND retry_at IS NULL
		    AND ($3 = 0 OR forbidden_failures < $3)
	`, userID, retryAt.UTC(), forbiddenLimit, claim.ID)
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

	if err := resolveLuxMedReconnect(ctx, tx, userID); err != nil {
		return err
	}
	return tx.Commit()
}

func resolveLuxMedReconnect(ctx context.Context, tx *sql.Tx, userID int64) error {
	result, err := tx.ExecContext(ctx, `DELETE FROM luxmed_reconnect WHERE user_id=$1`, userID)
	if err != nil {
		return err
	}
	removed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if removed > 0 {
		// Requeue active searches and clear obsolete sign-in errors on paused searches.
		_, err := tx.ExecContext(ctx, `
			UPDATE watches
			SET last_checked_at=CASE WHEN status=$2 THEN NULL ELSE last_checked_at END, last_error=''
			WHERE user_id=$1 AND (status=$2 OR
			    (status=$3 AND last_error IN ('luxmed_authentication', 'luxmed_account_missing')))
		`, userID, domain.WatchStatusActive, domain.WatchStatusPaused)
		if err != nil {
			return err
		}
	}
	return nil
}
