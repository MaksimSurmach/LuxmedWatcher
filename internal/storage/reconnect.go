package storage

import (
	"context"

	"github.com/maksimsurmach/luxmed-watcher/internal/domain"
)

func (db *DB) LuxMedReconnectRequired(ctx context.Context, userID int64) (bool, error) {
	var required bool
	err := db.sql.QueryRowContext(ctx, `
		SELECT EXISTS (SELECT 1 FROM luxmed_reconnect WHERE user_id=$1)
	`, userID).Scan(&required)
	return required, err
}

func (db *DB) BeginLuxMedReconnect(ctx context.Context, userID int64) (notified bool, err error) {
	err = db.sql.QueryRowContext(ctx, `
		INSERT INTO luxmed_reconnect (user_id) VALUES ($1)
		ON CONFLICT (user_id) DO UPDATE SET user_id=excluded.user_id
		RETURNING notified
	`, userID).Scan(&notified)
	return notified, err
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
