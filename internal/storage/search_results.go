package storage

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/maksimsurmach/luxmed-watcher/internal/domain"
)

type SearchHistoryUpdate struct {
	WatchID        int64
	Appointments   []domain.Appointment
	Complete       bool
	CompleteDays   []time.Time
	WatchUpdatedAt time.Time
	StartedAt      time.Time
	RenotifyAt     time.Time
}

// SaveSearchHistory persists results only while the search's sign-in mode is current.
func (db *DB) SaveSearchHistory(
	ctx context.Context,
	owner domain.User,
	update SearchHistoryUpdate,
) ([]domain.AppointmentHistory, error) {
	pending := make([]domain.AppointmentHistory, 0, len(update.Appointments))
	err := db.withCredentialRevision(ctx, owner, func(tx *sql.Tx) error {
		// Serialize with edits/resumes so an old in-flight response cannot refill a changed watch.
		query := "SELECT updated_at FROM watches WHERE id=$1 AND user_id=$2 AND status=$3"
		if db.postgres {
			query += " FOR UPDATE"
		}
		var revision time.Time
		if err := tx.QueryRowContext(ctx, query, update.WatchID, owner.ID, domain.WatchStatusActive).Scan(&revision); err != nil {
			return err
		}
		if !update.WatchUpdatedAt.IsZero() && !revision.Equal(update.WatchUpdatedAt) {
			return errors.New("watch changed during search")
		}
		for _, appointment := range update.Appointments {
			history, _, err := db.upsertAppointmentHistory(ctx, tx, update.WatchID, appointment)
			if err != nil {
				return err
			}
			if history.Status != domain.HistoryStatusHidden && history.LastNotifiedAt == nil {
				pending = append(pending, history)
			}
		}
		if update.Complete {
			if err := db.markMissingAppointments(ctx, tx, update.WatchID, update.StartedAt, update.RenotifyAt, nil, nil); err != nil {
				return err
			}
		} else {
			// The portal reports sorted Warsaw midnights. Merge adjacent days into one update.
			for i := 0; i < len(update.CompleteDays); {
				from := update.CompleteDays[i]
				end := from.AddDate(0, 0, 1)
				for i++; i < len(update.CompleteDays) && update.CompleteDays[i].Equal(end); i++ {
					end = end.AddDate(0, 0, 1)
				}
				if err := db.markMissingAppointments(ctx, tx, update.WatchID, update.StartedAt, update.RenotifyAt, &from, &end); err != nil {
					return err
				}
			}
		}
		// Keep a withdrawn notification only until it is eligible to be sent again.
		_, err := tx.ExecContext(ctx, `DELETE FROM appointment_history WHERE watch_id=$1 AND status=$2
			AND (last_notified_at IS NULL OR `+db.dateTimeSQL("last_notified_at")+` <= `+db.dateTimeSQL("$3")+`)`,
			update.WatchID, domain.HistoryStatusExpired, update.RenotifyAt.UTC())
		return err
	})
	return pending, err
}

// PurgeUnavailableSlots also runs for paused watches, which receive no further search results.
func (db *DB) PurgeUnavailableSlots(ctx context.Context, now, renotifyBefore time.Time) error {
	_, err := db.sql.ExecContext(ctx, `DELETE FROM appointment_history
		WHERE (status=$1 OR `+db.dateTimeSQL("date_time")+` <= `+db.dateTimeSQL("$2")+`)
		  AND (last_notified_at IS NULL OR `+db.dateTimeSQL("last_notified_at")+` <= `+db.dateTimeSQL("$3")+`)`,
		domain.HistoryStatusExpired, now.UTC(), renotifyBefore.UTC())
	return err
}
