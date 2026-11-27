package storage

import (
	"context"
	"database/sql"
	"time"

	"github.com/maksimsurmach/luxmed-watcher/internal/domain"
)

type SearchHistoryUpdate struct {
	WatchID      int64
	Appointments []domain.Appointment
	Complete     bool
	StartedAt    time.Time
	RenotifyAt   time.Time
}

// SaveSearchHistory persists results only while the search's sign-in mode is current.
func (db *DB) SaveSearchHistory(
	ctx context.Context,
	owner domain.User,
	update SearchHistoryUpdate,
) ([]domain.AppointmentHistory, error) {
	pending := make([]domain.AppointmentHistory, 0, len(update.Appointments))
	err := db.withCredentialRevision(ctx, owner, func(tx *sql.Tx) error {
		for _, appointment := range update.Appointments {
			history, _, err := upsertAppointmentHistory(ctx, tx, update.WatchID, appointment)
			if err != nil {
				return err
			}
			if history.Status != domain.HistoryStatusHidden && history.LastNotifiedAt == nil {
				pending = append(pending, history)
			}
		}
		if !update.Complete {
			return nil
		}
		return db.markMissingAppointments(ctx, tx, update.WatchID, update.StartedAt, update.RenotifyAt)
	})
	return pending, err
}
