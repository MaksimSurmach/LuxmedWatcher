package storage

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/maksimsurmach/luxmed-watcher/internal/domain"
)

func (db *DB) UpsertTelegramUser(ctx context.Context, user domain.User) (domain.User, error) {
	now := time.Now().UTC()
	if user.Locale == "" {
		user.Locale = "ru"
	}
	if user.Status == "" {
		user.Status = domain.UserStatusPendingInvite
	}
	if user.Role == "" {
		user.Role = domain.UserRoleUser
	}
	_, err := db.sql.ExecContext(ctx, `
		INSERT INTO users (telegram_user_id, telegram_chat_id, telegram_username, display_name, locale, status, role, created_at, updated_at, last_seen_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		ON CONFLICT(telegram_user_id) DO UPDATE SET
			telegram_chat_id=excluded.telegram_chat_id,
			telegram_username=excluded.telegram_username,
			display_name=excluded.display_name,
			updated_at=excluded.updated_at,
			last_seen_at=excluded.last_seen_at
	`, user.TelegramUserID, user.TelegramChatID, user.Username, user.DisplayName, user.Locale, user.Status, user.Role, now, now, now)
	if err != nil {
		return domain.User{}, err
	}
	return db.UserByTelegramID(ctx, user.TelegramUserID)
}

func (db *DB) UserByTelegramID(ctx context.Context, telegramID int64) (domain.User, error) {
	row := db.sql.QueryRowContext(ctx, `
		SELECT id, telegram_user_id, telegram_chat_id, telegram_username, display_name, locale, preferred_city_id, preferred_city_name, status, role, created_at, updated_at, last_seen_at
		FROM users WHERE telegram_user_id=$1
	`, telegramID)
	return scanUser(row)
}

func (db *DB) UserByID(ctx context.Context, id int64) (domain.User, error) {
	row := db.sql.QueryRowContext(ctx, `
		SELECT id, telegram_user_id, telegram_chat_id, telegram_username, display_name, locale, preferred_city_id, preferred_city_name, status, role, created_at, updated_at, last_seen_at
		FROM users WHERE id=$1
	`, id)
	return scanUser(row)
}

func (db *DB) SetUserActive(ctx context.Context, userID int64, role domain.UserRole) error {
	_, err := db.sql.ExecContext(ctx, `UPDATE users SET status=$1, role=$2, updated_at=$3 WHERE id=$4`, domain.UserStatusActive, role, time.Now().UTC(), userID)
	return err
}

func (db *DB) SetUserLocale(ctx context.Context, userID int64, locale string) error {
	_, err := db.sql.ExecContext(ctx, `UPDATE users SET locale=$1, updated_at=$2 WHERE id=$3`, locale, time.Now().UTC(), userID)
	return err
}

func (db *DB) SetUserPreferredCity(ctx context.Context, userID int64, city domain.City) error {
	_, err := db.sql.ExecContext(ctx, `UPDATE users SET preferred_city_id=$1, preferred_city_name=$2, updated_at=$3 WHERE id=$4`, city.ID, city.Name, time.Now().UTC(), userID)
	return err
}

func (db *DB) SetUserPreferences(ctx context.Context, userID int64, locale string, city domain.City) error {
	res, err := db.sql.ExecContext(ctx, `
		UPDATE users SET locale=$1, preferred_city_id=NULLIF($2, 0), preferred_city_name=$3, updated_at=$4
		WHERE id=$5
	`, locale, city.ID, city.Name, time.Now().UTC(), userID)
	return requireOneRow(res, err)
}

func (db *DB) CreateInvite(ctx context.Context, code string, createdBy int64, maxUses int, expiresAt *time.Time) error {
	_, err := db.sql.ExecContext(ctx, `
		INSERT INTO invite_codes (code_hash, created_by_user_id, max_uses, used_count, expires_at, created_at)
		VALUES ($1, $2, $3, 0, $4, $5)
	`, HashInviteCode(code), createdBy, maxUses, nullableTime(expiresAt), time.Now().UTC())
	return err
}

func (db *DB) RedeemInvite(ctx context.Context, code string) (bool, error) {
	return db.redeemInvite(ctx, code, 0)
}

func (db *DB) RedeemInviteForUser(ctx context.Context, code string, userID int64) (bool, error) {
	return db.redeemInvite(ctx, code, userID)
}

func (db *DB) redeemInvite(ctx context.Context, code string, userID int64) (bool, error) {
	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()

	now := time.Now().UTC()
	res, err := tx.ExecContext(ctx, `
		UPDATE invite_codes SET used_count=used_count+1
		WHERE code_hash=$1 AND used_count < max_uses AND disabled_at IS NULL
		AND (expires_at IS NULL OR `+db.dateTimeSQL("expires_at")+` > `+db.dateTimeSQL("$2")+`)
	`, HashInviteCode(code), now)
	if err := requireOneRow(res, err); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	if userID != 0 {
		res, err := tx.ExecContext(ctx, `UPDATE users SET status=$1, updated_at=$2 WHERE id=$3 AND status=$4`,
			domain.UserStatusActive, now, userID, domain.UserStatusPendingInvite)
		if err := requireOneRow(res, err); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return false, nil
			}
			return false, err
		}
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

func requireOneRow(result sql.Result, err error) error {
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err == nil && n == 0 {
		return sql.ErrNoRows
	}
	return err
}

func HashInviteCode(code string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(strings.ToLower(code))))
	return hex.EncodeToString(sum[:])
}

func (db *DB) SaveVerifiedLuxMedAccount(ctx context.Context, account domain.LuxMedAccount) error {
	return db.saveLuxMedAccount(ctx, account, true)
}

// SavePendingLuxMedAccount saves encrypted credentials and schedules access verification.
func (db *DB) SavePendingLuxMedAccount(ctx context.Context, account domain.LuxMedAccount) error {
	return db.saveLuxMedAccount(ctx, account, false)
}

func (db *DB) saveLuxMedAccount(ctx context.Context, account domain.LuxMedAccount, verified bool) error {
	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	_, err = tx.ExecContext(ctx, `
		INSERT INTO luxmed_accounts (user_id, login, encrypted_password, encrypted_session_data, session_expires_at, last_login_at, last_login_error, status)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT(user_id) DO UPDATE SET
			login=excluded.login,
			encrypted_password=excluded.encrypted_password,
			encrypted_session_data=excluded.encrypted_session_data,
			session_expires_at=excluded.session_expires_at,
			last_login_at=excluded.last_login_at,
			last_login_error=excluded.last_login_error,
			status=excluded.status
	`, account.UserID, account.Login, account.EncryptedPassword, account.EncryptedSessionData, nullableTime(account.SessionExpiresAt), nullableTime(account.LastLoginAt), account.LastLoginError, account.Status)
	if err != nil {
		return err
	}
	if verified {
		if err := resolveLuxMedReconnect(ctx, tx, account.UserID, true); err != nil {
			return err
		}
	} else {
		if _, err := tx.ExecContext(ctx, `
			UPDATE watches SET last_checked_at=NULL WHERE user_id=$1 AND status=$2
		`, account.UserID, domain.WatchStatusActive); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if verified {
		db.clearPendingReconnectRejections(account.UserID)
	}
	return nil
}

func (db *DB) LuxMedAccount(ctx context.Context, userID int64) (domain.LuxMedAccount, error) {
	row := db.sql.QueryRowContext(ctx, `
		SELECT id, user_id, login, encrypted_password, encrypted_session_data, session_expires_at, last_login_at, last_login_error, status
		FROM luxmed_accounts WHERE user_id=$1
	`, userID)
	var account domain.LuxMedAccount
	var sessionExpiresAt, lastLoginAt sql.NullTime
	err := row.Scan(&account.ID, &account.UserID, &account.Login, &account.EncryptedPassword, &account.EncryptedSessionData, &sessionExpiresAt, &lastLoginAt, &account.LastLoginError, &account.Status)
	if sessionExpiresAt.Valid {
		account.SessionExpiresAt = &sessionExpiresAt.Time
	}
	if lastLoginAt.Valid {
		account.LastLoginAt = &lastLoginAt.Time
	}
	return account, err
}

func (db *DB) UpdateLuxMedAccountAuth(ctx context.Context, account domain.LuxMedAccount) error {
	res, err := db.sql.ExecContext(ctx, `
		UPDATE luxmed_accounts SET last_login_at=$1, last_login_error=$2, status=$3
		WHERE user_id=$4 AND login=$5 AND encrypted_password=$6
	`, nullableTime(account.LastLoginAt), account.LastLoginError, account.Status, account.UserID, account.Login, account.EncryptedPassword)
	return requireOneRow(res, err)
}

func (db *DB) DeleteLuxMedAccount(ctx context.Context, userID int64) error {
	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM luxmed_accounts WHERE user_id=$1`, userID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE watches SET status=$1, updated_at=$2 WHERE user_id=$3 AND status=$4`,
		domain.WatchStatusPaused, time.Now().UTC(), userID, domain.WatchStatusActive); err != nil {
		return err
	}
	return tx.Commit()
}

func (db *DB) CreateWatch(ctx context.Context, watch domain.Watch) (int64, error) {
	now := time.Now().UTC()
	var id int64
	err := db.sql.QueryRowContext(ctx, `
		INSERT INTO watches (
			name, city_id, city_name, service_id, service_name, doctor_id, doctor_name, doctor_mode,
			facility_mode, facility_ids, facility_names, date_from, date_to, next_days, time_windows,
			check_interval_seconds, user_id, status, created_at, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $19)
		RETURNING id
	`, append(watchParameters(watch), watch.UserID, domain.WatchStatusActive, now)...).Scan(&id)
	return id, err
}

func (db *DB) UpdateWatch(ctx context.Context, watch domain.Watch) error {
	res, err := db.sql.ExecContext(ctx, `
		UPDATE watches SET name=$1, city_id=$2, city_name=$3, service_id=$4, service_name=$5, doctor_id=$6,
			doctor_name=$7, doctor_mode=$8, facility_mode=$9, facility_ids=$10, facility_names=$11, date_from=$12,
			date_to=$13, next_days=$14, time_windows=$15, check_interval_seconds=$16, updated_at=$17,
			last_checked_at=NULL, last_success_at=NULL, last_error=''
		WHERE id=$18 AND user_id=$19 AND status <> $20
	`, append(watchParameters(watch), time.Now().UTC(), watch.ID, watch.UserID, domain.WatchStatusDeleted)...)
	return requireOneRow(res, err)
}

func watchParameters(watch domain.Watch) []any {
	facilityIDs, _ := json.Marshal(watch.FacilityIDs)
	facilityNames, _ := json.Marshal(watch.FacilityNames)
	timeWindows, _ := json.Marshal(watch.TimeWindows)
	return []any{watch.Name, watch.CityID, watch.CityName, watch.ServiceID, watch.ServiceName,
		nullableInt(watch.DoctorID), watch.DoctorName, watch.DoctorMode, watch.FacilityMode, string(facilityIDs), string(facilityNames),
		nullableTime(watch.DateFrom), nullableTime(watch.DateTo), watch.NextDays, string(timeWindows), watch.CheckIntervalSeconds}
}

func (db *DB) WatchesByUser(ctx context.Context, userID int64) ([]domain.Watch, error) {
	return watchesByUser(ctx, db.sql, userID)
}

type watchQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func watchesByUser(ctx context.Context, db watchQueryer, userID int64) ([]domain.Watch, error) {
	rows, err := db.QueryContext(ctx, watchSelect()+` WHERE user_id=$1 AND status <> $2 ORDER BY created_at DESC`, userID, domain.WatchStatusDeleted)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanWatches(rows)
}

func (db *DB) ActiveWatchTelegramIDs(ctx context.Context) ([]int64, error) {
	return db.activeWatchTelegramIDs(ctx, false)
}

func (db *DB) ReconnectWatchTelegramIDs(ctx context.Context) ([]int64, error) {
	return db.activeWatchTelegramIDs(ctx, true)
}

func (db *DB) activeWatchTelegramIDs(ctx context.Context, reconnectOnly bool) ([]int64, error) {
	rows, err := db.sql.QueryContext(ctx, `
		SELECT u.telegram_user_id
		FROM users u
		WHERE u.status=$1 AND u.role <> ''
			AND (($3 AND (
				EXISTS (SELECT 1 FROM luxmed_reconnect r WHERE r.user_id=u.id)
				OR (EXISTS (SELECT 1 FROM luxmed_accounts a WHERE a.user_id=u.id AND a.status=$4)
					AND EXISTS (SELECT 1 FROM watches w WHERE w.user_id=u.id AND w.status IN ($2, $5) AND `+watchSignInError+`))
			))
				OR (NOT $3 AND EXISTS (SELECT 1 FROM watches w WHERE w.user_id=u.id AND w.status=$2)))
		ORDER BY u.telegram_user_id
	`, domain.UserStatusActive, domain.WatchStatusActive, reconnectOnly, domain.AccountStatusActive, domain.WatchStatusPaused)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (db *DB) DueWatches(ctx context.Context, limit int) ([]domain.Watch, error) {
	due := db.dateTimeSQL("last_checked_at") + ` <= ` + db.dateTimeSQL("$2") + ` - check_interval_seconds / 86400.0`
	if db.postgres {
		due = `last_checked_at + check_interval_seconds * interval '1 second' <= $2`
	}
	query := watchSelect() + `
		WHERE status=$1 AND (last_checked_at IS NULL OR ` + due + `)
		AND user_id IN (SELECT id FROM users WHERE status=$3)
		ORDER BY COALESCE(last_checked_at, created_at), id`
	args := []any{domain.WatchStatusActive, time.Now().UTC(), domain.UserStatusActive}
	if limit > 0 {
		query += ` LIMIT $4`
		args = append(args, limit)
	}
	rows, err := db.sql.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanWatches(rows)
}

func (db *DB) dateTimeSQL(value string) string {
	if db.postgres {
		return value
	}
	// Legacy SQLite rows use Go's Time.String format, which julianday cannot read directly.
	offset := `instr(substr(` + value + `, 12), ' ') + 12`
	legacy := `substr(` + value + `, 1, ` + offset + ` - 2) || substr(` + value + `, ` + offset + `, 3) || ':' || substr(` + value + `, ` + offset + ` + 3, 2)`
	return `COALESCE(julianday(` + value + `), julianday(` + legacy + `))`
}

func (db *DB) Watch(ctx context.Context, id int64) (domain.Watch, error) {
	row := db.sql.QueryRowContext(ctx, watchSelect()+` WHERE id=$1`, id)
	return scanWatch(row)
}

func (db *DB) SetWatchStatus(ctx context.Context, id int64, status domain.WatchStatus) error {
	_, err := db.sql.ExecContext(ctx, `UPDATE watches SET status=$1, updated_at=$2 WHERE id=$3`, status, time.Now().UTC(), id)
	return err
}

func (db *DB) SetUserWatchStatus(ctx context.Context, userID, id int64, status domain.WatchStatus) error {
	res, err := db.sql.ExecContext(ctx, `
		UPDATE watches SET status=$1, updated_at=$2 WHERE id=$3 AND user_id=$4 AND status <> $5
	`, status, time.Now().UTC(), id, userID, domain.WatchStatusDeleted)
	return requireOneRow(res, err)
}

func (db *DB) MarkWatchChecked(ctx context.Context, id int64, success bool, errText string) error {
	_, err := db.sql.ExecContext(ctx, markWatchCheckedQuery, time.Now().UTC(), success, errText, id)
	return err
}

const markWatchCheckedQuery = `
	UPDATE watches
	SET last_checked_at=$1, last_success_at=CASE WHEN $2 THEN $1 ELSE last_success_at END,
	    last_error=CASE WHEN $2 THEN '' ELSE $3 END, updated_at=$1
	WHERE id=$4
`

func (db *DB) MarkWatchCheckedIfUnchanged(ctx context.Context, watch domain.Watch, success bool, errText string) error {
	query := markWatchCheckedQuery + ` AND status=$5 AND updated_at=$6`
	args := []any{time.Now().UTC(), success, errText, watch.ID, domain.WatchStatusActive, watch.UpdatedAt}
	if !db.postgres {
		// Older SQLite rows stored UTC timestamps in Go's default format.
		query = markWatchCheckedQuery + ` AND status=$5 AND updated_at IN ($6, $7)`
		args = append(args, watch.UpdatedAt.UTC().String())
	}
	return requireOneRow(db.sql.ExecContext(ctx, query, args...))
}

func (db *DB) UpsertAppointmentHistory(ctx context.Context, watchID int64, app domain.Appointment) (domain.AppointmentHistory, bool, error) {
	if app.LegacyFingerprint != "" {
		var legacyAddress string
		err := db.sql.QueryRowContext(ctx, `
			SELECT address FROM appointment_history WHERE watch_id=$1 AND fingerprint=$2
		`, watchID, app.LegacyFingerprint).Scan(&legacyAddress)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return domain.AppointmentHistory{}, false, err
		}
		// Keep the original row and its delivery history for this address only.
		if err == nil && domain.NormalizeAppointmentAddress(legacyAddress) == domain.NormalizeAppointmentAddress(app.Address) {
			app.Fingerprint = app.LegacyFingerprint
		}
	}
	now := time.Now().UTC()
	row := db.sql.QueryRowContext(ctx, `
		INSERT INTO appointment_history (
			watch_id, external_id, fingerprint, date_time, service_id, service_name, doctor_id, doctor_name,
			facility_id, facility_name, address, city_id, city_name, booking_url, first_seen_at, last_seen_at, seen_count, status, raw_payload
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $15, 1, $16, $17)
		ON CONFLICT(watch_id, fingerprint) DO UPDATE SET
			last_seen_at=excluded.last_seen_at,
			seen_count=appointment_history.seen_count+1,
			status=CASE WHEN appointment_history.status=$18 THEN appointment_history.status
				WHEN appointment_history.last_notified_at IS NOT NULL THEN $19 ELSE $20 END
		RETURNING `+historyColumns,
		watchID, app.ExternalID, app.Fingerprint, app.DateTime, app.ServiceID, app.ServiceName, app.DoctorID, app.DoctorName,
		app.FacilityID, app.FacilityName, app.Address, app.CityID, app.CityName, app.BookingURL, now, domain.HistoryStatusNew,
		app.RawPayload, domain.HistoryStatusHidden, domain.HistoryStatusNotified, domain.HistoryStatusSeenAgain)
	history, err := scanHistory(row)
	return history, history.SeenCount == 1, err
}

func (db *DB) MarkMissingAppointments(ctx context.Context, watchID int64, seenSince, notifiedBefore time.Time) error {
	_, err := db.sql.ExecContext(ctx, `
		UPDATE appointment_history SET status=$1,
			last_notified_at=CASE WHEN `+db.dateTimeSQL("last_notified_at")+` <= `+db.dateTimeSQL("$2")+` THEN NULL ELSE last_notified_at END
		WHERE watch_id=$3 AND `+db.dateTimeSQL("last_seen_at")+` < `+db.dateTimeSQL("$4")+` AND status <> $5
	`, domain.HistoryStatusExpired, notifiedBefore.UTC(), watchID, seenSince.UTC(), domain.HistoryStatusHidden)
	return err
}

func (db *DB) AppointmentHistoryByID(ctx context.Context, id int64) (domain.AppointmentHistory, error) {
	row := db.sql.QueryRowContext(ctx, historySelect()+` WHERE id=$1`, id)
	return scanHistory(row)
}

func (db *DB) RecentHistory(ctx context.Context, userID int64, limit, offset int) ([]domain.AppointmentHistory, error) {
	return db.WatchHistory(ctx, userID, 0, limit, offset)
}

// UpcomingHistoryIDs captures the order for a history view. Future available slots
// come first, followed by past and unavailable slots.
func (db *DB) UpcomingHistoryIDs(ctx context.Context, userID int64, asOf time.Time, limit int) ([]int64, error) {
	when := db.dateTimeSQL("date_time")
	rows, err := db.sql.QueryContext(ctx, `
		SELECT id FROM (
			SELECT id, `+when+` AS slot_time,
				CASE WHEN `+when+` > `+db.dateTimeSQL("$2")+` AND status <> $3 THEN 0 ELSE 1 END AS unavailable
			FROM appointment_history
			WHERE watch_id IN (SELECT id FROM watches WHERE user_id=$1) AND status <> $4
		) ranked
		ORDER BY unavailable, CASE WHEN unavailable = 0 THEN slot_time END ASC, slot_time DESC, id ASC
		LIMIT $5
	`, userID, asOf.UTC(), domain.HistoryStatusExpired, domain.HistoryStatusHidden, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// HistoryByIDs loads current slot data in the requested order, omitting hidden or removed slots.
func (db *DB) HistoryByIDs(ctx context.Context, userID int64, ids []int64) ([]domain.AppointmentHistory, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	args := []any{userID, domain.HistoryStatusHidden}
	params := make([]string, 0, len(ids))
	for _, id := range ids {
		args = append(args, id)
		params = append(params, "$"+strconv.Itoa(len(args)))
	}
	rows, err := db.sql.QueryContext(ctx, historySelect()+`
		WHERE watch_id IN (SELECT id FROM watches WHERE user_id=$1) AND status <> $2
			AND id IN (`+strings.Join(params, ",")+`)
	`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byID := make(map[int64]domain.AppointmentHistory, len(ids))
	for rows.Next() {
		item, err := scanHistory(rows)
		if err != nil {
			return nil, err
		}
		byID[item.ID] = item
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	items := make([]domain.AppointmentHistory, 0, len(byID))
	for _, id := range ids {
		if item, ok := byID[id]; ok {
			items = append(items, item)
		}
	}
	return items, nil
}

func (db *DB) WatchHistory(ctx context.Context, userID, watchID int64, limit, offset int) ([]domain.AppointmentHistory, error) {
	return db.queryHistory(ctx, historySelect()+`
		WHERE watch_id IN (SELECT id FROM watches WHERE user_id=$1 AND (id=$2 OR $2=0)) AND status <> $3
		ORDER BY `+db.dateTimeSQL("date_time")+` DESC, id DESC LIMIT $4 OFFSET $5
	`, userID, watchID, domain.HistoryStatusHidden, limit, offset)
}

type NotificationOptionsCursor struct {
	DateTime  time.Time
	ID        int64
	Before    bool
	Inclusive bool
}

func (db *DB) AvailableNotificationOptions(
	ctx context.Context, userID, watchID, messageID int64, now time.Time, limit int, cursor NotificationOptionsCursor,
) ([]domain.AppointmentHistory, error) {
	date := db.dateTimeSQL("date_time")
	query := historySelect() + `
		WHERE watch_id IN (SELECT id FROM watches WHERE user_id=$1 AND id=$2 AND status <> $3)
			AND status NOT IN ($4, $5) AND ` + date + ` > ` + db.dateTimeSQL("$6") + `
			AND EXISTS (
				SELECT 1 FROM notification_history AS n
				WHERE n.watch_id=$2 AND n.appointment_fingerprint=appointment_history.fingerprint
					AND n.appointment_history_id=appointment_history.id AND n.message_id=$7 AND n.status=$8
			)
	`
	args := []any{userID, watchID, domain.WatchStatusDeleted, domain.HistoryStatusHidden, domain.HistoryStatusExpired,
		now.UTC(), messageID, domain.NotificationStatusSent, limit}
	order := " ASC"
	if cursor.ID > 0 {
		comparison := ">"
		if cursor.Before {
			comparison, order = "<", " DESC"
		}
		if cursor.Inclusive {
			comparison += "="
		}
		query += " AND (" + date + ", id) " + comparison + " (" + db.dateTimeSQL("$10") + ", $11)"
		args = append(args, cursor.DateTime.UTC(), cursor.ID)
	}
	query += " ORDER BY " + date + order + ", id" + order + " LIMIT $9"
	return db.queryHistory(ctx, query, args...)
}

func (db *DB) queryHistory(ctx context.Context, query string, args ...any) ([]domain.AppointmentHistory, error) {
	rows, err := db.sql.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []domain.AppointmentHistory
	for rows.Next() {
		item, err := scanHistory(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (db *DB) HideAppointmentHistory(ctx context.Context, userID, historyID int64) error {
	res, err := db.sql.ExecContext(ctx, `
		UPDATE appointment_history SET status=$1
		WHERE id=$2 AND watch_id IN (SELECT id FROM watches WHERE user_id=$3)
	`, domain.HistoryStatusHidden, historyID, userID)
	return requireOneRow(res, err)
}

func (db *DB) CountHistoryForWatch(ctx context.Context, watchID int64) (int, error) {
	var count int
	err := db.sql.QueryRowContext(ctx, `SELECT COUNT(*) FROM appointment_history WHERE watch_id=$1`, watchID).Scan(&count)
	return count, err
}

func (db *DB) MarkNotified(ctx context.Context, watchID int64, historyID int64, fingerprint string, messageID int, status domain.NotificationStatus, errText string) error {
	return db.MarkNotifications(ctx, []domain.AppointmentHistory{{
		ID: historyID, WatchID: watchID, Appointment: domain.Appointment{Fingerprint: fingerprint},
	}}, messageID, status, errText)
}

func (db *DB) MarkNotifications(ctx context.Context, histories []domain.AppointmentHistory, messageID int, status domain.NotificationStatus, errText string) error {
	now := time.Now().UTC()
	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, h := range histories {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO notification_history (watch_id, appointment_history_id, appointment_fingerprint, sent_at, message_id, status, error)
			VALUES ($1, $2, $3, $4, $5, $6, $7)
		`, h.WatchID, h.ID, h.Fingerprint, now, messageID, status, errText); err != nil {
			return err
		}
		if status == domain.NotificationStatusSent {
			if _, err := tx.ExecContext(ctx, `UPDATE appointment_history SET last_notified_at=$1,
				status=CASE WHEN status=$2 THEN status ELSE $3 END WHERE id=$4`,
				now, domain.HistoryStatusHidden, domain.HistoryStatusNotified, h.ID); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

func (db *DB) LastNotificationAt(ctx context.Context, watchID int64, fingerprint string) (*time.Time, error) {
	var sentAt time.Time
	err := db.sql.QueryRowContext(ctx, `
		SELECT sent_at FROM notification_history WHERE watch_id=$1 AND appointment_fingerprint=$2 AND status=$3
		ORDER BY `+db.dateTimeSQL("sent_at")+` DESC LIMIT 1
	`, watchID, fingerprint, domain.NotificationStatusSent).Scan(&sentAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &sentAt, nil
}

func (db *DB) PurgeHistory(ctx context.Context, before time.Time) error {
	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	cutoff := db.dateTimeSQL("$1")
	if _, err := tx.ExecContext(ctx, `DELETE FROM appointment_history
		WHERE `+db.dateTimeSQL("date_time")+` < `+cutoff+` AND `+db.dateTimeSQL("last_seen_at")+` < `+cutoff+`
		AND NOT EXISTS (SELECT 1 FROM notification_history n WHERE n.watch_id=appointment_history.watch_id
			AND n.appointment_fingerprint=appointment_history.fingerprint AND `+db.dateTimeSQL("n.sent_at")+` >= `+cutoff+`)
	`, before.UTC()); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM notification_history WHERE `+db.dateTimeSQL("sent_at")+` < `+cutoff, before.UTC()); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM invite_codes WHERE `+db.dateTimeSQL("expires_at")+` < `+cutoff, before.UTC()); err != nil {
		return err
	}
	return tx.Commit()
}

func (db *DB) UpsertCities(ctx context.Context, cities []domain.City) error {
	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().UTC()
	for _, city := range cities {
		if city.ID == 0 || city.Name == "" {
			continue
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO luxmed_cities (id, name, updated_at) VALUES ($1, $2, $3)
			ON CONFLICT(id) DO UPDATE SET name=excluded.name, updated_at=excluded.updated_at
		`, city.ID, city.Name, now); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (db *DB) Cities(ctx context.Context, limit int) ([]domain.City, error) {
	return db.CitiesPage(ctx, limit, 0)
}

func (db *DB) CitiesPage(ctx context.Context, limit int, offset int) ([]domain.City, error) {
	query := `SELECT id, name FROM luxmed_cities ORDER BY name`
	args := []any{}
	if limit > 0 {
		query += ` LIMIT $1 OFFSET $2`
		args = append(args, limit, offset)
	}
	rows, err := db.sql.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var cities []domain.City
	for rows.Next() {
		var city domain.City
		if err := rows.Scan(&city.ID, &city.Name); err != nil {
			return nil, err
		}
		cities = append(cities, city)
	}
	return cities, rows.Err()
}

func (db *DB) SearchCities(ctx context.Context, query string, limit int) ([]domain.City, error) {
	query = normalizeCitySearch(query)
	rows, err := db.sql.QueryContext(ctx, `SELECT id, name FROM luxmed_cities ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var cities []domain.City
	for rows.Next() {
		var city domain.City
		if err := rows.Scan(&city.ID, &city.Name); err != nil {
			return nil, err
		}
		if query == "" || strings.Contains(normalizeCitySearch(city.Name), query) {
			cities = append(cities, city)
		}
		if limit > 0 && len(cities) >= limit {
			break
		}
	}
	return cities, rows.Err()
}

func normalizeCitySearch(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	replacer := strings.NewReplacer(
		"ą", "a",
		"ć", "c",
		"ę", "e",
		"ł", "l",
		"ń", "n",
		"ó", "o",
		"ś", "s",
		"ż", "z",
		"ź", "z",
	)
	return replacer.Replace(value)
}

func (db *DB) City(ctx context.Context, id int) (domain.City, error) {
	var city domain.City
	err := db.sql.QueryRowContext(ctx, `SELECT id, name FROM luxmed_cities WHERE id=$1`, id).Scan(&city.ID, &city.Name)
	return city, err
}

func (db *DB) UpsertProcedures(ctx context.Context, city domain.City, procedures []domain.Procedure) error {
	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().UTC()
	for _, proc := range procedures {
		if proc.ID == 0 || proc.Name == "" {
			continue
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO luxmed_procedures (id, city_id, city_name, name, is_recent, updated_at)
			VALUES ($1, $2, $3, $4, $5, $6)
			ON CONFLICT(id, city_id) DO UPDATE SET
				city_name=excluded.city_name,
				name=excluded.name,
				is_recent=luxmed_procedures.is_recent OR excluded.is_recent,
				updated_at=excluded.updated_at
		`, proc.ID, city.ID, city.Name, proc.Name, proc.IsRecent, now); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (db *DB) RecentProcedures(ctx context.Context, cityID int, limit int) ([]domain.Procedure, error) {
	rows, err := db.sql.QueryContext(ctx, `
		SELECT id, city_id, city_name, name, is_recent, updated_at
		FROM luxmed_procedures
		WHERE city_id=$1 AND is_recent=TRUE
		ORDER BY updated_at DESC, name
		LIMIT $2
	`, cityID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanProcedures(rows)
}

func (db *DB) SearchProcedures(ctx context.Context, cityID int, query string, limit int) ([]domain.Procedure, error) {
	return db.SearchProceduresPage(ctx, cityID, query, limit, 0)
}

func (db *DB) SearchProceduresPage(ctx context.Context, cityID int, query string, limit int, offset int) ([]domain.Procedure, error) {
	query = strings.TrimSpace(strings.ToLower(query))
	sqlQuery := `
		SELECT id, city_id, city_name, name, is_recent, updated_at
		FROM luxmed_procedures
		WHERE city_id=$1 AND ($2='' OR lower(name) LIKE '%' || $2 || '%')`
	args := []any{cityID, query}

	sqlQuery += ` ORDER BY name`

	if limit > 0 {
		sqlQuery += ` LIMIT $3 OFFSET $4`
		args = append(args, limit, offset)
	}

	rows, err := db.sql.QueryContext(ctx, sqlQuery, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return scanProcedures(rows)
}

func (db *DB) ProcedureLetters(ctx context.Context, cityID int) ([]string, error) {
	rows, err := db.sql.QueryContext(ctx, `
		SELECT DISTINCT substr(name, 1, 1)
		FROM luxmed_procedures
		WHERE city_id=$1 AND name <> ''
		ORDER BY substr(name, 1, 1)
	`, cityID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var letters []string
	for rows.Next() {
		var letter string
		if err := rows.Scan(&letter); err != nil {
			return nil, err
		}
		if strings.TrimSpace(letter) != "" {
			letters = append(letters, letter)
		}
	}

	return letters, rows.Err()
}

func (db *DB) ProceduresByLetter(ctx context.Context, cityID int, letter string, limit int, offset int) ([]domain.Procedure, error) {
	letter = strings.TrimSpace(letter)
	if letter == "" {
		return nil, nil
	}

	sqlQuery := `
		SELECT id, city_id, city_name, name, is_recent, updated_at
		FROM luxmed_procedures
		WHERE city_id=$1 AND substr(name, 1, 1)=$2
		ORDER BY name`
	args := []any{cityID, letter}

	if limit > 0 {
		sqlQuery += ` LIMIT $3 OFFSET $4`
		args = append(args, limit, offset)
	}

	rows, err := db.sql.QueryContext(ctx, sqlQuery, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return scanProcedures(rows)
}

func (db *DB) Procedure(ctx context.Context, cityID int, procedureID int) (domain.Procedure, error) {
	row := db.sql.QueryRowContext(ctx, `
		SELECT id, city_id, city_name, name, is_recent, updated_at
		FROM luxmed_procedures
		WHERE city_id=$1 AND id=$2
	`, cityID, procedureID)
	var proc domain.Procedure
	err := row.Scan(&proc.ID, &proc.CityID, &proc.CityName, &proc.Name, &proc.IsRecent, &proc.UpdatedAt)
	return proc, err
}

func (db *DB) UpsertFacilities(ctx context.Context, city domain.City, procedureID int, facilities []domain.Facility) error {
	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().UTC()
	for _, facility := range facilities {
		if facility.ID == 0 || facility.Name == "" {
			continue
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO luxmed_facilities (id, city_id, city_name, procedure_id, name, address, updated_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7)
			ON CONFLICT(id, city_id, procedure_id) DO UPDATE SET
				city_name=excluded.city_name,
				name=excluded.name,
				address=excluded.address,
				updated_at=excluded.updated_at
		`, facility.ID, city.ID, city.Name, procedureID, facility.Name, facility.Address, now); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (db *DB) Facilities(ctx context.Context, cityID int, procedureID int, limit int) ([]domain.Facility, error) {
	query := `
		SELECT id, name, address
		FROM luxmed_facilities
		WHERE city_id=$1 AND procedure_id=$2
		ORDER BY name`
	args := []any{cityID, procedureID}
	if limit > 0 {
		query += ` LIMIT $3`
		args = append(args, limit)
	}
	rows, err := db.sql.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var facilities []domain.Facility
	for rows.Next() {
		var facility domain.Facility
		if err := rows.Scan(&facility.ID, &facility.Name, &facility.Address); err != nil {
			return nil, err
		}
		facilities = append(facilities, facility)
	}
	return facilities, rows.Err()
}

func (db *DB) FacilitiesPage(ctx context.Context, cityID int, procedureID int, limit int, offset int) ([]domain.Facility, error) {
	query := `
		SELECT id, name, address
		FROM luxmed_facilities
		WHERE city_id=$1 AND procedure_id=$2
		ORDER BY name`
	args := []any{cityID, procedureID}

	if limit > 0 {
		query += ` LIMIT $3 OFFSET $4`
		args = append(args, limit, offset)
	}

	rows, err := db.sql.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var facilities []domain.Facility
	for rows.Next() {
		var facility domain.Facility
		if err := rows.Scan(&facility.ID, &facility.Name, &facility.Address); err != nil {
			return nil, err
		}
		facilities = append(facilities, facility)
	}

	return facilities, rows.Err()
}

func (db *DB) FavoriteFacilities(ctx context.Context, userID int64, cityID int, procedureID int, limit int) ([]domain.Facility, error) {
	query := `
		SELECT f.id, f.name, f.address
		FROM favorite_facilities fav
		JOIN luxmed_facilities f ON f.id=fav.facility_id AND f.city_id=fav.city_id AND f.procedure_id=fav.procedure_id
		WHERE fav.user_id=$1 AND fav.city_id=$2 AND fav.procedure_id=$3
		ORDER BY fav.last_used_at DESC`
	args := []any{userID, cityID, procedureID}
	if limit > 0 {
		query += ` LIMIT $4`
		args = append(args, limit)
	}
	rows, err := db.sql.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var facilities []domain.Facility
	for rows.Next() {
		var facility domain.Facility
		if err := rows.Scan(&facility.ID, &facility.Name, &facility.Address); err != nil {
			return nil, err
		}
		facilities = append(facilities, facility)
	}
	return facilities, rows.Err()
}

func (db *DB) FavoriteFacilitiesPage(ctx context.Context, userID int64, cityID int, procedureID int, limit int, offset int) ([]domain.Facility, error) {
	query := `
		SELECT f.id, f.name, f.address
		FROM favorite_facilities fav
		JOIN luxmed_facilities f ON f.id=fav.facility_id AND f.city_id=fav.city_id AND f.procedure_id=fav.procedure_id
		WHERE fav.user_id=$1 AND fav.city_id=$2 AND fav.procedure_id=$3
		ORDER BY fav.last_used_at DESC`
	args := []any{userID, cityID, procedureID}

	if limit > 0 {
		query += ` LIMIT $4 OFFSET $5`
		args = append(args, limit, offset)
	}

	rows, err := db.sql.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var facilities []domain.Facility
	for rows.Next() {
		var facility domain.Facility
		if err := rows.Scan(&facility.ID, &facility.Name, &facility.Address); err != nil {
			return nil, err
		}
		facilities = append(facilities, facility)
	}

	return facilities, rows.Err()
}

func (db *DB) SaveFavoriteFacility(ctx context.Context, userID int64, cityID int, procedureID int, facility domain.Facility) error {
	_, err := db.sql.ExecContext(ctx, `
		INSERT INTO favorite_facilities (user_id, city_id, procedure_id, facility_id, facility_name, last_used_at)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT(user_id, city_id, procedure_id, facility_id) DO UPDATE SET
			facility_name=excluded.facility_name,
			last_used_at=excluded.last_used_at
	`, userID, cityID, procedureID, facility.ID, facility.Name, time.Now().UTC())
	return err
}

func scanProcedures(rows *sql.Rows) ([]domain.Procedure, error) {
	var procedures []domain.Procedure
	for rows.Next() {
		var proc domain.Procedure
		if err := rows.Scan(&proc.ID, &proc.CityID, &proc.CityName, &proc.Name, &proc.IsRecent, &proc.UpdatedAt); err != nil {
			return nil, err
		}
		procedures = append(procedures, proc)
	}
	return procedures, rows.Err()
}

type scanner interface {
	Scan(dest ...any) error
}

func scanUser(row scanner) (domain.User, error) {
	var u domain.User
	var preferredCityID sql.NullInt64
	err := row.Scan(&u.ID, &u.TelegramUserID, &u.TelegramChatID, &u.Username, &u.DisplayName, &u.Locale, &preferredCityID, &u.PreferredCityName, &u.Status, &u.Role, &u.CreatedAt, &u.UpdatedAt, &u.LastSeenAt)
	if preferredCityID.Valid {
		id := int(preferredCityID.Int64)
		u.PreferredCityID = &id
	}
	return u, err
}

func watchSelect() string {
	return `SELECT id, user_id, name, status, city_id, city_name, service_id, service_name, doctor_id, doctor_name, doctor_mode,
		facility_mode, facility_ids, facility_names, date_from, date_to, next_days, time_windows,
		check_interval_seconds, last_checked_at, last_success_at, last_error, created_at, updated_at FROM watches`
}

func scanWatches(rows *sql.Rows) ([]domain.Watch, error) {
	var watches []domain.Watch
	for rows.Next() {
		watch, err := scanWatch(rows)
		if err != nil {
			return nil, err
		}
		watches = append(watches, watch)
	}
	return watches, rows.Err()
}

func scanWatch(row scanner) (domain.Watch, error) {
	var watch domain.Watch
	var doctorID sql.NullInt64
	var facilityIDs, facilityNames, timeWindows string
	var dateFrom, dateTo, lastChecked, lastSuccess sql.NullTime
	err := row.Scan(&watch.ID, &watch.UserID, &watch.Name, &watch.Status, &watch.CityID, &watch.CityName, &watch.ServiceID, &watch.ServiceName,
		&doctorID, &watch.DoctorName, &watch.DoctorMode, &watch.FacilityMode, &facilityIDs, &facilityNames, &dateFrom, &dateTo, &watch.NextDays,
		&timeWindows, &watch.CheckIntervalSeconds, &lastChecked, &lastSuccess, &watch.LastError, &watch.CreatedAt, &watch.UpdatedAt)
	if err != nil {
		return domain.Watch{}, err
	}
	if doctorID.Valid {
		id := int(doctorID.Int64)
		watch.DoctorID = &id
	}
	if dateFrom.Valid {
		watch.DateFrom = &dateFrom.Time
	}
	if dateTo.Valid {
		watch.DateTo = &dateTo.Time
	}
	if lastChecked.Valid {
		watch.LastCheckedAt = &lastChecked.Time
	}
	if lastSuccess.Valid {
		watch.LastSuccessAt = &lastSuccess.Time
	}
	_ = json.Unmarshal([]byte(facilityIDs), &watch.FacilityIDs)
	_ = json.Unmarshal([]byte(facilityNames), &watch.FacilityNames)
	_ = json.Unmarshal([]byte(timeWindows), &watch.TimeWindows)
	return watch, nil
}

const historyColumns = `id, watch_id, external_id, fingerprint, date_time, service_id, service_name, doctor_id, doctor_name,
		facility_id, facility_name, address, city_id, city_name, booking_url, first_seen_at, last_seen_at, last_notified_at, seen_count, status, raw_payload`

func historySelect() string {
	return `SELECT ` + historyColumns + ` FROM appointment_history`
}

func scanHistory(row scanner) (domain.AppointmentHistory, error) {
	var h domain.AppointmentHistory
	var lastNotified sql.NullTime
	err := row.Scan(&h.ID, &h.WatchID, &h.ExternalID, &h.Fingerprint, &h.DateTime, &h.ServiceID, &h.ServiceName, &h.DoctorID, &h.DoctorName,
		&h.FacilityID, &h.FacilityName, &h.Address, &h.CityID, &h.CityName, &h.BookingURL, &h.FirstSeenAt, &h.LastSeenAt, &lastNotified, &h.SeenCount, &h.Status, &h.RawPayload)
	if lastNotified.Valid {
		h.LastNotifiedAt = &lastNotified.Time
	}
	return h, err
}

func nullableTime(value *time.Time) any {
	if value == nil {
		return nil
	}
	return *value
}

func nullableInt(value *int) any {
	if value == nil {
		return nil
	}
	return *value
}
