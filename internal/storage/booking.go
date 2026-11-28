package storage

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/maksimsurmach/luxmed-watcher/internal/domain"
)

const bookingColumns = "id, user_id, watch_id, date_time, service, doctor, place, automatic, status, reminders"

func scanBooking(row scanner) (domain.Booking, error) {
	var b domain.Booking
	err := row.Scan(&b.ID, &b.UserID, &b.WatchID, &b.DateTime, &b.Service, &b.Doctor, &b.Place, &b.Automatic, &b.Status, &b.Reminders)
	return b, err
}

func (db *DB) BookingBySlot(ctx context.Context, userID, slotID int64) (domain.Booking, error) {
	return scanBooking(db.sql.QueryRowContext(ctx, "SELECT "+bookingColumns+" FROM bookings WHERE user_id=$1 AND slot_id=$2", userID, slotID))
}

func (db *DB) BookingByFingerprint(ctx context.Context, userID int64, fingerprint string) (domain.Booking, error) {
	return scanBooking(db.sql.QueryRowContext(ctx, "SELECT "+bookingColumns+" FROM bookings WHERE user_id=$1 AND fingerprint=$2", userID, fingerprint))
}

// ClaimBooking commits the intent before the external request. A restarted or concurrent
// worker can see that a confirmation may already have been sent.
func (db *DB) ClaimBooking(ctx context.Context, user domain.User, watch domain.Watch, slot domain.AppointmentHistory, automatic bool) (domain.Booking, error) {
	var booking domain.Booking
	err := db.withCredentialRevision(ctx, user, func(tx *sql.Tx) error {
		// Serialize mutations with search edits, including on PostgreSQL.
		query := `SELECT updated_at FROM watches WHERE id=$1 AND user_id=$2 AND status='active'
			AND (NOT $3 OR auto_book)`
		if db.postgres {
			query += " FOR UPDATE"
		}
		var revision time.Time
		if err := tx.QueryRowContext(ctx, query, watch.ID, user.ID, automatic).Scan(&revision); err != nil {
			return err
		}
		if !revision.Equal(watch.UpdatedAt) {
			return sql.ErrNoRows
		}
		place := slot.Address
		if place == "" {
			place = slot.FacilityName
		}
		var err error
		booking, err = scanBooking(tx.QueryRowContext(ctx, `
			INSERT INTO bookings(user_id,watch_id,slot_id,fingerprint,date_time,service,doctor,place,automatic,status)
			SELECT $1,$2,$3,$4,$5,$6,$7,$8,$9,'pending' WHERE
			(NOT $9 OR NOT EXISTS (SELECT 1 FROM bookings WHERE watch_id=$2 AND status IN ('pending','unknown')))
			ON CONFLICT(user_id,fingerprint) DO NOTHING RETURNING `+bookingColumns,
			user.ID, watch.ID, slot.ID, slot.Fingerprint, slot.DateTime.UTC(), slot.ServiceName, slot.DoctorName, place, automatic))
		return err
	})
	return booking, err
}

func (db *DB) RejectBooking(ctx context.Context, id int64, taken bool) error {
	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if taken {
		if _, err := tx.ExecContext(ctx, `UPDATE appointment_history SET status='expired' WHERE status NOT IN ('hidden','expired')
			AND watch_id IN (SELECT w.id FROM watches w JOIN bookings b ON w.user_id=b.user_id WHERE b.id=$1)
			AND fingerprint=(SELECT fingerprint FROM bookings WHERE id=$1)`, id); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM bookings WHERE id=$1 AND status='pending'", id); err != nil {
		return err
	}
	return tx.Commit()
}

func (db *DB) FinishBooking(ctx context.Context, booking domain.Booking, providerID string, now time.Time) error {
	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if providerID == "" {
		if err := requireOneRow(tx.ExecContext(ctx, "UPDATE bookings SET status='unknown' WHERE id=$1 AND status='pending'", booking.ID)); err != nil {
			return err
		}
		if err := queueBookingMessage(ctx, tx, booking.ID, "unknown", now); err != nil {
			return err
		}
		return tx.Commit()
	}
	if err := requireOneRow(tx.ExecContext(ctx, `UPDATE bookings SET status='confirmed', provider_id=$2,
		reminders=automatic WHERE id=$1 AND status IN ('pending','unknown')`, booking.ID, providerID)); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE appointment_history SET status='expired'
		WHERE fingerprint=(SELECT fingerprint FROM bookings WHERE id=$1)
		AND watch_id IN (SELECT id FROM watches WHERE user_id=$2) AND status NOT IN ('hidden','expired')`,
		booking.ID, booking.UserID); err != nil {
		return err
	}
	if booking.Automatic {
		if err := setUserWatchStatus(ctx, tx, booking.UserID, booking.WatchID, domain.WatchStatusPaused); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE watches SET auto_book=FALSE,
			last_checked_at=$1, last_success_at=$1, last_error='' WHERE id=$2 AND status='paused'`, now, booking.WatchID); err != nil {
			return err
		}
		if err := queueReminders(ctx, tx, booking, now); err != nil {
			return err
		}
	}
	if err := queueBookingMessage(ctx, tx, booking.ID, "confirmed", now); err != nil {
		return err
	}
	return tx.Commit()
}

func (db *DB) SetBookingReminders(ctx context.Context, userID, id int64, enabled bool, now time.Time) (domain.Booking, error) {
	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		return domain.Booking{}, err
	}
	defer tx.Rollback()
	booking, err := scanBooking(tx.QueryRowContext(ctx, `UPDATE bookings SET reminders=$3 WHERE id=$1 AND user_id=$2
		AND status='confirmed' AND `+db.dateTimeSQL("date_time")+` > `+db.dateTimeSQL("$4")+` RETURNING `+bookingColumns,
		id, userID, enabled, now.UTC()))
	if err != nil {
		return booking, err
	}
	if enabled {
		if err := queueReminders(ctx, tx, booking, now); err != nil {
			return booking, err
		}
	}
	return booking, tx.Commit()
}

func queueReminders(ctx context.Context, tx *sql.Tx, booking domain.Booking, now time.Time) error {
	for _, reminder := range []struct {
		kind   string
		before time.Duration
	}{{"day", 24 * time.Hour}, {"two_hours", 2 * time.Hour}} {
		if due := booking.DateTime.Add(-reminder.before); due.After(now) {
			if err := queueBookingMessage(ctx, tx, booking.ID, reminder.kind, due); err != nil {
				return err
			}
		}
	}
	return nil
}

func queueBookingMessage(ctx context.Context, tx *sql.Tx, id int64, kind string, due time.Time) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO booking_messages(booking_id,kind,due_at) VALUES($1,$2,$3)
		ON CONFLICT(booking_id,kind) DO NOTHING`, id, kind, due.UTC())
	return err
}

func (db *DB) UserBookings(ctx context.Context, userID int64, now time.Time) ([]domain.Booking, error) {
	rows, err := db.sql.QueryContext(ctx, `SELECT `+bookingColumns+` FROM bookings WHERE user_id=$1
		AND `+db.dateTimeSQL("date_time")+` > `+db.dateTimeSQL("$2")+` ORDER BY date_time, id`, userID, now.UTC())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var bookings []domain.Booking
	for rows.Next() {
		booking, err := scanBooking(rows)
		if err != nil {
			return nil, err
		}
		bookings = append(bookings, booking)
	}
	return bookings, rows.Err()
}

func (db *DB) SetWatchAutoBook(ctx context.Context, userID, id int64, enabled bool) error {
	return requireOneRow(db.sql.ExecContext(ctx, `UPDATE watches SET auto_book=$3,updated_at=$4
		WHERE user_id=$1 AND id=$2 AND status <> 'deleted'`, userID, id, enabled, time.Now().UTC()))
}

type BookingMessage struct {
	Booking domain.Booking
	Kind    string
	Lease   time.Time
}

func (db *DB) ClaimBookingMessage(ctx context.Context, now time.Time) (BookingMessage, error) {
	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		return BookingMessage{}, err
	}
	defer tx.Rollback()
	message := BookingMessage{Lease: now.Add(time.Minute).UTC().Truncate(time.Microsecond)}
	var id int64
	due := db.dateTimeSQL("due_at") + " <= " + db.dateTimeSQL("$1")
	available := `sent=FALSE AND (claim_until IS NULL OR ` + db.dateTimeSQL("claim_until") + ` <= ` + db.dateTimeSQL("$1") + `) AND ` + due
	err = tx.QueryRowContext(ctx, `UPDATE booking_messages SET claim_until=$2
		WHERE (booking_id,kind)=(SELECT booking_id,kind FROM booking_messages JOIN bookings b ON b.id=booking_id
			WHERE `+available+` AND `+db.dateTimeSQL("b.date_time")+` > `+db.dateTimeSQL("$1")+`
			AND (kind IN ('confirmed','unknown') OR b.reminders)
			ORDER BY due_at,booking_id,kind LIMIT 1) AND `+available+`
		RETURNING booking_id,kind`, now.UTC(), message.Lease).Scan(&id, &message.Kind)
	if err != nil {
		return BookingMessage{}, err
	}
	message.Booking, err = scanBooking(tx.QueryRowContext(ctx, "SELECT "+bookingColumns+" FROM bookings WHERE id=$1", id))
	if err != nil {
		return BookingMessage{}, err
	}
	return message, tx.Commit()
}

func (db *DB) MarkBookingMessageSent(ctx context.Context, m BookingMessage) error {
	return requireOneRow(db.sql.ExecContext(ctx, `UPDATE booking_messages SET sent=TRUE
		WHERE booking_id=$1 AND kind=$2 AND claim_until=$3`, m.Booking.ID, m.Kind, m.Lease))
}

func (db *DB) AutoBookingHeld(ctx context.Context, watchID int64) (bool, error) {
	var held bool
	err := db.sql.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM bookings WHERE watch_id=$1 AND status IN ('pending','unknown'))", watchID).Scan(&held)
	return held, err
}

func (db *DB) BookableWatchSlot(ctx context.Context, watchID int64, slot domain.Appointment, now time.Time) (domain.AppointmentHistory, error) {
	return scanHistory(db.sql.QueryRowContext(ctx, historySelect()+` WHERE watch_id=$1
		AND fingerprint IN ($2,$3) AND status NOT IN ('hidden','expired') AND `+db.dateTimeSQL("date_time")+
		` > `+db.dateTimeSQL("$4")+` ORDER BY CASE WHEN fingerprint=$2 THEN 0 ELSE 1 END LIMIT 1`,
		watchID, slot.Fingerprint, slot.LegacyFingerprint, now.UTC()))
}

func bookingFingerprints(ctx context.Context, tx *sql.Tx, userID int64, now time.Time) (map[string]bool, error) {
	rows, err := tx.QueryContext(ctx, `SELECT fingerprint FROM bookings WHERE user_id=$1 AND date_time > $2 AND status='confirmed'`, userID, now.UTC())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	found := make(map[string]bool)
	for rows.Next() {
		var fingerprint string
		if err := rows.Scan(&fingerprint); err != nil {
			return nil, err
		}
		found[fingerprint] = true
	}
	return found, rows.Err()
}
