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
const watchSignInError = `last_error IN ('luxmed_authentication', 'luxmed_account_missing')`

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

const blockedReconnectRetry = 7 * 24 * time.Hour

// On a database error the returned ID identifies the attempted reservation, even if it committed.
func (db *DB) ClaimLuxMedReconnectDelivery(ctx context.Context, userID int64, now time.Time) (ReconnectDeliveryClaim, error) {
	claim := ReconnectDeliveryClaim{ID: rand.Text()}
	err := db.sql.QueryRowContext(ctx, `
		INSERT INTO luxmed_reconnect_delivery (user_id, attempts, claim_id)
		SELECT user_id, 1, $3 FROM luxmed_reconnect WHERE user_id=$1 AND NOT notified
		    AND EXISTS (SELECT 1 FROM watches WHERE user_id=$1 AND status=$4)
		ON CONFLICT (user_id) DO UPDATE SET attempts=luxmed_reconnect_delivery.attempts+1,
		    retry_at=NULL, claim_id=excluded.claim_id, last_failure_forbidden=FALSE
		WHERE `+db.dateTimeSQL("luxmed_reconnect_delivery.retry_at")+` <= `+db.dateTimeSQL("$2")+`
		RETURNING attempts
	`, userID, now.UTC(), claim.ID, domain.WatchStatusActive).Scan(&claim.Attempt)
	if errors.Is(err, sql.ErrNoRows) {
		return ReconnectDeliveryClaim{}, nil
	}
	return claim, err
}

func (db *DB) LuxMedReconnectDeliveryDue(ctx context.Context, userID int64, now time.Time) (bool, error) {
	var due bool
	err := db.sql.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM luxmed_reconnect r
			LEFT JOIN luxmed_reconnect_delivery d ON d.user_id=r.user_id
			WHERE r.user_id=$1 AND NOT r.notified
			    AND (d.user_id IS NULL OR `+db.dateTimeSQL("d.retry_at")+` <= `+db.dateTimeSQL("$2")+`)
			    AND EXISTS (SELECT 1 FROM watches WHERE user_id=$1 AND status=$3)
		)
	`, userID, now.UTC(), domain.WatchStatusActive).Scan(&due)
	return due, err
}

// A nonzero forbidden limit records a 403 and switches to weekly retries at the limit.
func (db *DB) RecordLuxMedReconnectRejection(
	ctx context.Context,
	userID int64,
	claim ReconnectDeliveryClaim,
	retryAt time.Time,
	forbiddenLimit int,
) error {
	_, err := db.recordLuxMedReconnectRejection(ctx, userID, claim, retryAt, forbiddenLimit)
	return err
}

func (db *DB) recordLuxMedReconnectRejection(ctx context.Context, userID int64, claim ReconnectDeliveryClaim, retryAt time.Time, forbiddenLimit int) (bool, error) {
	if claim.ID == "" {
		return false, errors.New("missing reconnect delivery claim ID")
	}
	blockedRetryAt := time.Now().UTC().Add(blockedReconnectRetry)
	if retryAt.After(blockedRetryAt) {
		blockedRetryAt = retryAt.UTC()
	}
	res, err := db.sql.ExecContext(ctx, `
		UPDATE luxmed_reconnect_delivery
		SET forbidden_failures=forbidden_failures + CASE WHEN $3 > 0 AND forbidden_failures < $3 THEN 1 ELSE 0 END,
		    last_failure_forbidden=($3 > 0),
		    retry_at=CASE WHEN $3 > 0 AND forbidden_failures + 1 >= $3
		                  THEN COALESCE($5, retry_at) ELSE COALESCE($2, retry_at) END
		WHERE user_id=$1 AND claim_id=$4 AND retry_at IS NULL
	`, userID, retryAt.UTC(), forbiddenLimit, claim.ID, blockedRetryAt)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n != 0, err
}

// ResumeLuxMedReconnectDelivery accelerates confirmed 403s, including legacy capped attempts.
func (db *DB) ResumeLuxMedReconnectDelivery(ctx context.Context, userID int64, forbiddenLimit int, now time.Time) error {
	if forbiddenLimit <= 0 {
		return errors.New("missing reconnect forbidden limit")
	}
	_, err := db.sql.ExecContext(ctx, `
		UPDATE luxmed_reconnect_delivery
		SET forbidden_failures=0, retry_at=$3, last_failure_forbidden=FALSE
		WHERE user_id=$1 AND (last_failure_forbidden OR (forbidden_failures >= $2 AND retry_at IS NULL))
		    AND EXISTS (SELECT 1 FROM luxmed_reconnect WHERE user_id=$1 AND NOT notified)
	`, userID, forbiddenLimit, now.UTC())
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
	var pending bool
	err := db.sql.QueryRowContext(ctx, reconnectRequiredQuery+`
		OR EXISTS (SELECT 1 FROM watches WHERE user_id=$1 AND status IN ($2, $3) AND `+watchSignInError+`)`,
		userID, domain.WatchStatusActive, domain.WatchStatusPaused).Scan(&pending)
	if err != nil {
		return err
	}
	if !pending {
		db.pendingRejections.Delete(userID)
		return nil
	}
	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if err := resolveLuxMedReconnect(ctx, tx, userID); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	db.pendingRejections.Delete(userID)
	return nil
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
	// Sign-in errors can outlive a search that was paused before an episode opened.
	_, err = tx.ExecContext(ctx, `
		UPDATE watches
		SET last_checked_at=CASE WHEN status=$2 THEN NULL ELSE last_checked_at END, last_error=''
		WHERE user_id=$1 AND status IN ($2, $3) AND (($4 AND status=$2) OR `+watchSignInError+`)
	`, userID, domain.WatchStatusActive, domain.WatchStatusPaused, removed > 0)
	return err
}
