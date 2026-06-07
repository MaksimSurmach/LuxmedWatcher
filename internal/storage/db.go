package storage

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/maksimsurmach/luxmed-watcher/internal/domain"
	_ "modernc.org/sqlite"
)

type DB struct {
	sql *sql.DB
}

func Open(ctx context.Context, path string) (*DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path+"?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	store := &DB{sql: db}
	if err := store.Migrate(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return store, nil
}

func (db *DB) Close() error {
	return db.sql.Close()
}

func (db *DB) Migrate(ctx context.Context) error {
	if _, err := db.sql.ExecContext(ctx, schema); err != nil {
		return err
	}
	for _, stmt := range compatibilityMigrations {
		_, _ = db.sql.ExecContext(ctx, stmt)
	}
	return nil
}

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
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
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
		FROM users WHERE telegram_user_id=?
	`, telegramID)
	return scanUser(row)
}

func (db *DB) UserByID(ctx context.Context, id int64) (domain.User, error) {
	row := db.sql.QueryRowContext(ctx, `
		SELECT id, telegram_user_id, telegram_chat_id, telegram_username, display_name, locale, preferred_city_id, preferred_city_name, status, role, created_at, updated_at, last_seen_at
		FROM users WHERE id=?
	`, id)
	return scanUser(row)
}

func (db *DB) SetUserActive(ctx context.Context, userID int64, role domain.UserRole) error {
	_, err := db.sql.ExecContext(ctx, `UPDATE users SET status=?, role=?, updated_at=? WHERE id=?`, domain.UserStatusActive, role, time.Now().UTC(), userID)
	return err
}

func (db *DB) SetUserLocale(ctx context.Context, userID int64, locale string) error {
	_, err := db.sql.ExecContext(ctx, `UPDATE users SET locale=?, updated_at=? WHERE id=?`, locale, time.Now().UTC(), userID)
	return err
}

func (db *DB) SetUserPreferredCity(ctx context.Context, userID int64, city domain.City) error {
	_, err := db.sql.ExecContext(ctx, `UPDATE users SET preferred_city_id=?, preferred_city_name=?, updated_at=? WHERE id=?`, city.ID, city.Name, time.Now().UTC(), userID)
	return err
}

func (db *DB) CreateInvite(ctx context.Context, code string, createdBy int64, maxUses int, expiresAt *time.Time) error {
	_, err := db.sql.ExecContext(ctx, `
		INSERT INTO invite_codes (code_hash, created_by_user_id, max_uses, used_count, expires_at, created_at)
		VALUES (?, ?, ?, 0, ?, ?)
	`, HashInviteCode(code), createdBy, maxUses, nullableTime(expiresAt), time.Now().UTC())
	return err
}

func (db *DB) RedeemInvite(ctx context.Context, code string) (bool, error) {
	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()

	var id int64
	var maxUses, usedCount int
	var expiresAt sql.NullTime
	var disabledAt sql.NullTime
	err = tx.QueryRowContext(ctx, `
		SELECT id, max_uses, used_count, expires_at, disabled_at
		FROM invite_codes WHERE code_hash=?
	`, HashInviteCode(code)).Scan(&id, &maxUses, &usedCount, &expiresAt, &disabledAt)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if disabledAt.Valid || usedCount >= maxUses || (expiresAt.Valid && time.Now().After(expiresAt.Time)) {
		return false, nil
	}
	if _, err := tx.ExecContext(ctx, `UPDATE invite_codes SET used_count=used_count+1 WHERE id=?`, id); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

func HashInviteCode(code string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(strings.ToLower(code))))
	return hex.EncodeToString(sum[:])
}

func (db *DB) UpsertLuxMedAccount(ctx context.Context, account domain.LuxMedAccount) error {
	_, err := db.sql.ExecContext(ctx, `
		INSERT INTO luxmed_accounts (user_id, login, encrypted_password, encrypted_session_data, session_expires_at, last_login_at, last_login_error, status)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(user_id) DO UPDATE SET
			login=excluded.login,
			encrypted_password=excluded.encrypted_password,
			encrypted_session_data=excluded.encrypted_session_data,
			session_expires_at=excluded.session_expires_at,
			last_login_at=excluded.last_login_at,
			last_login_error=excluded.last_login_error,
			status=excluded.status
	`, account.UserID, account.Login, account.EncryptedPassword, account.EncryptedSessionData, nullableTime(account.SessionExpiresAt), nullableTime(account.LastLoginAt), account.LastLoginError, account.Status)
	return err
}

func (db *DB) LuxMedAccount(ctx context.Context, userID int64) (domain.LuxMedAccount, error) {
	row := db.sql.QueryRowContext(ctx, `
		SELECT id, user_id, login, encrypted_password, encrypted_session_data, session_expires_at, last_login_at, last_login_error, status
		FROM luxmed_accounts WHERE user_id=?
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

func (db *DB) CreateWatch(ctx context.Context, watch domain.Watch) (int64, error) {
	now := time.Now().UTC()
	facilityIDs, _ := json.Marshal(watch.FacilityIDs)
	facilityNames, _ := json.Marshal(watch.FacilityNames)
	timeWindows, _ := json.Marshal(watch.TimeWindows)
	res, err := db.sql.ExecContext(ctx, `
		INSERT INTO watches (
			user_id, name, status, city_id, city_name, service_id, service_name, doctor_id, doctor_name, doctor_mode,
			facility_mode, facility_ids, facility_names, date_from, date_to, next_days, time_windows,
			check_interval_seconds, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, watch.UserID, watch.Name, domain.WatchStatusActive, watch.CityID, watch.CityName, watch.ServiceID, watch.ServiceName,
		nullableInt(watch.DoctorID), watch.DoctorName, watch.DoctorMode, watch.FacilityMode, string(facilityIDs), string(facilityNames),
		nullableTime(watch.DateFrom), nullableTime(watch.DateTo), watch.NextDays, string(timeWindows), watch.CheckIntervalSeconds, now, now)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (db *DB) WatchesByUser(ctx context.Context, userID int64) ([]domain.Watch, error) {
	rows, err := db.sql.QueryContext(ctx, watchSelect()+` WHERE user_id=? AND status <> ? ORDER BY created_at DESC`, userID, domain.WatchStatusDeleted)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanWatches(rows)
}

func (db *DB) DueWatches(ctx context.Context, limit int) ([]domain.Watch, error) {
	rows, err := db.sql.QueryContext(ctx, watchSelect()+`
		WHERE status=? AND (
			last_checked_at IS NULL OR datetime(last_checked_at, '+' || check_interval_seconds || ' seconds') <= datetime('now')
		)
		ORDER BY COALESCE(last_checked_at, created_at) ASC LIMIT ?
	`, domain.WatchStatusActive, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanWatches(rows)
}

func (db *DB) Watch(ctx context.Context, id int64) (domain.Watch, error) {
	row := db.sql.QueryRowContext(ctx, watchSelect()+` WHERE id=?`, id)
	return scanWatch(row)
}

func (db *DB) SetWatchStatus(ctx context.Context, id int64, status domain.WatchStatus) error {
	_, err := db.sql.ExecContext(ctx, `UPDATE watches SET status=?, updated_at=? WHERE id=?`, status, time.Now().UTC(), id)
	return err
}

func (db *DB) MarkWatchChecked(ctx context.Context, id int64, success bool, errText string) error {
	now := time.Now().UTC()
	if success {
		_, err := db.sql.ExecContext(ctx, `UPDATE watches SET last_checked_at=?, last_success_at=?, last_error='', updated_at=? WHERE id=?`, now, now, now, id)
		return err
	}
	_, err := db.sql.ExecContext(ctx, `UPDATE watches SET last_checked_at=?, last_error=?, updated_at=? WHERE id=?`, now, errText, now, id)
	return err
}

func (db *DB) UpsertAppointmentHistory(ctx context.Context, watchID int64, app domain.Appointment) (domain.AppointmentHistory, bool, error) {
	now := time.Now().UTC()
	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		return domain.AppointmentHistory{}, false, err
	}
	defer tx.Rollback()

	var id int64
	err = tx.QueryRowContext(ctx, `SELECT id FROM appointment_history WHERE watch_id=? AND fingerprint=?`, watchID, app.Fingerprint).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		_, err = tx.ExecContext(ctx, `
			INSERT INTO appointment_history (
				watch_id, external_id, fingerprint, date_time, service_id, service_name, doctor_id, doctor_name,
				facility_id, facility_name, address, city_id, city_name, booking_url, first_seen_at, last_seen_at, seen_count, status, raw_payload
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1, ?, ?)
		`, watchID, app.ExternalID, app.Fingerprint, app.DateTime, app.ServiceID, app.ServiceName, app.DoctorID, app.DoctorName,
			app.FacilityID, app.FacilityName, app.Address, app.CityID, app.CityName, app.BookingURL, now, now, domain.HistoryStatusNew, app.RawPayload)
		if err != nil {
			return domain.AppointmentHistory{}, false, err
		}
		id, err = lastInsertID(ctx, tx)
		if err != nil {
			return domain.AppointmentHistory{}, false, err
		}
		if err := tx.Commit(); err != nil {
			return domain.AppointmentHistory{}, false, err
		}
		history, err := db.AppointmentHistoryByID(ctx, id)
		return history, true, err
	}
	if err != nil {
		return domain.AppointmentHistory{}, false, err
	}
	_, err = tx.ExecContext(ctx, `
		UPDATE appointment_history SET last_seen_at=?, seen_count=seen_count+1, status=? WHERE id=?
	`, now, domain.HistoryStatusSeenAgain, id)
	if err != nil {
		return domain.AppointmentHistory{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return domain.AppointmentHistory{}, false, err
	}
	history, err := db.AppointmentHistoryByID(ctx, id)
	return history, false, err
}

func (db *DB) AppointmentHistoryByID(ctx context.Context, id int64) (domain.AppointmentHistory, error) {
	row := db.sql.QueryRowContext(ctx, historySelect()+` WHERE id=?`, id)
	return scanHistory(row)
}

func (db *DB) RecentHistory(ctx context.Context, userID int64, limit, offset int) ([]domain.AppointmentHistory, error) {
	rows, err := db.sql.QueryContext(ctx, historySelect()+`
		WHERE watch_id IN (SELECT id FROM watches WHERE user_id=?) AND status <> ?
		ORDER BY date_time DESC LIMIT ? OFFSET ?
	`, userID, domain.HistoryStatusHidden, limit, offset)
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

func (db *DB) CountHistoryForWatch(ctx context.Context, watchID int64) (int, error) {
	var count int
	err := db.sql.QueryRowContext(ctx, `SELECT COUNT(*) FROM appointment_history WHERE watch_id=?`, watchID).Scan(&count)
	return count, err
}

func (db *DB) MarkNotified(ctx context.Context, watchID int64, historyID int64, fingerprint string, messageID int, status domain.NotificationStatus, errText string) error {
	now := time.Now().UTC()
	_, err := db.sql.ExecContext(ctx, `
		INSERT INTO notification_history (watch_id, appointment_history_id, appointment_fingerprint, sent_at, message_id, status, error)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`, watchID, historyID, fingerprint, now, messageID, status, errText)
	if err != nil {
		return err
	}
	if status == domain.NotificationStatusSent {
		_, err = db.sql.ExecContext(ctx, `UPDATE appointment_history SET last_notified_at=?, status=? WHERE id=?`, now, domain.HistoryStatusNotified, historyID)
	}
	return err
}

func (db *DB) LastNotificationAt(ctx context.Context, watchID int64, fingerprint string) (*time.Time, error) {
	var sentAt sql.NullTime
	err := db.sql.QueryRowContext(ctx, `
		SELECT MAX(sent_at) FROM notification_history WHERE watch_id=? AND appointment_fingerprint=? AND status=?
	`, watchID, fingerprint, domain.NotificationStatusSent).Scan(&sentAt)
	if err != nil {
		return nil, err
	}
	if !sentAt.Valid {
		return nil, nil
	}
	return &sentAt.Time, nil
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
			INSERT INTO luxmed_cities (id, name, updated_at) VALUES (?, ?, ?)
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
		query += ` LIMIT ? OFFSET ?`
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
	err := db.sql.QueryRowContext(ctx, `SELECT id, name FROM luxmed_cities WHERE id=?`, id).Scan(&city.ID, &city.Name)
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
			VALUES (?, ?, ?, ?, ?, ?)
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
		WHERE city_id=? AND is_recent=1
		ORDER BY updated_at DESC, name
		LIMIT ?
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
		WHERE city_id=?`
	args := []any{cityID}

	if query != "" {
		sqlQuery += ` AND lower(name) LIKE ?`
		args = append(args, "%"+query+"%")
	}

	sqlQuery += ` ORDER BY name`

	if limit > 0 {
		sqlQuery += ` LIMIT ? OFFSET ?`
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
		WHERE city_id=? AND name <> ''
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
		WHERE city_id=? AND substr(name, 1, 1)=?
		ORDER BY name`
	args := []any{cityID, letter}

	if limit > 0 {
		sqlQuery += ` LIMIT ? OFFSET ?`
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
		WHERE city_id=? AND id=?
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
			VALUES (?, ?, ?, ?, ?, ?, ?)
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
		WHERE city_id=? AND procedure_id=?
		ORDER BY name`
	args := []any{cityID, procedureID}
	if limit > 0 {
		query += ` LIMIT ?`
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
		WHERE city_id=? AND procedure_id=?
		ORDER BY name`
	args := []any{cityID, procedureID}

	if limit > 0 {
		query += ` LIMIT ? OFFSET ?`
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
		WHERE fav.user_id=? AND fav.city_id=? AND fav.procedure_id=?
		ORDER BY fav.last_used_at DESC`
	args := []any{userID, cityID, procedureID}
	if limit > 0 {
		query += ` LIMIT ?`
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
		WHERE fav.user_id=? AND fav.city_id=? AND fav.procedure_id=?
		ORDER BY fav.last_used_at DESC`
	args := []any{userID, cityID, procedureID}

	if limit > 0 {
		query += ` LIMIT ? OFFSET ?`
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
		VALUES (?, ?, ?, ?, ?, ?)
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

func historySelect() string {
	return `SELECT id, watch_id, external_id, fingerprint, date_time, service_id, service_name, doctor_id, doctor_name,
		facility_id, facility_name, address, city_id, city_name, booking_url, first_seen_at, last_seen_at, last_notified_at, seen_count, status, raw_payload
		FROM appointment_history`
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

func lastInsertID(ctx context.Context, tx *sql.Tx) (int64, error) {
	var id int64
	err := tx.QueryRowContext(ctx, `SELECT last_insert_rowid()`).Scan(&id)
	return id, err
}

var schema = fmt.Sprintf(`
CREATE TABLE IF NOT EXISTS users (
	id INTEGER PRIMARY KEY,
	telegram_user_id INTEGER NOT NULL UNIQUE,
	telegram_chat_id INTEGER NOT NULL,
	telegram_username TEXT NOT NULL DEFAULT '',
	display_name TEXT NOT NULL DEFAULT '',
	locale TEXT NOT NULL,
	preferred_city_id INTEGER,
	preferred_city_name TEXT NOT NULL DEFAULT '',
	status TEXT NOT NULL,
	role TEXT NOT NULL,
	created_at TIMESTAMP NOT NULL,
	updated_at TIMESTAMP NOT NULL,
	last_seen_at TIMESTAMP NOT NULL
);

CREATE TABLE IF NOT EXISTS invite_codes (
	id INTEGER PRIMARY KEY,
	code_hash TEXT NOT NULL UNIQUE,
	created_by_user_id INTEGER NOT NULL,
	max_uses INTEGER NOT NULL,
	used_count INTEGER NOT NULL DEFAULT 0,
	expires_at TIMESTAMP,
	disabled_at TIMESTAMP,
	created_at TIMESTAMP NOT NULL
);

CREATE TABLE IF NOT EXISTS luxmed_accounts (
	id INTEGER PRIMARY KEY,
	user_id INTEGER NOT NULL UNIQUE REFERENCES users(id) ON DELETE CASCADE,
	login TEXT NOT NULL,
	encrypted_password TEXT NOT NULL,
	encrypted_session_data TEXT NOT NULL DEFAULT '',
	session_expires_at TIMESTAMP,
	last_login_at TIMESTAMP,
	last_login_error TEXT NOT NULL DEFAULT '',
	status TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS watches (
	id INTEGER PRIMARY KEY,
	user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	name TEXT NOT NULL,
	status TEXT NOT NULL CHECK (status IN ('%s','%s','%s')),
	city_id INTEGER NOT NULL,
	city_name TEXT NOT NULL,
	service_id INTEGER NOT NULL,
	service_name TEXT NOT NULL,
	doctor_id INTEGER,
	doctor_name TEXT NOT NULL DEFAULT '',
	doctor_mode TEXT NOT NULL,
	facility_mode TEXT NOT NULL,
	facility_ids TEXT NOT NULL DEFAULT '[]',
	facility_names TEXT NOT NULL DEFAULT '[]',
	date_from TIMESTAMP,
	date_to TIMESTAMP,
	next_days INTEGER NOT NULL DEFAULT 14,
	time_windows TEXT NOT NULL DEFAULT '[]',
	check_interval_seconds INTEGER NOT NULL,
	last_checked_at TIMESTAMP,
	last_success_at TIMESTAMP,
	last_error TEXT NOT NULL DEFAULT '',
	created_at TIMESTAMP NOT NULL,
	updated_at TIMESTAMP NOT NULL
);

CREATE TABLE IF NOT EXISTS appointment_history (
	id INTEGER PRIMARY KEY,
	watch_id INTEGER NOT NULL REFERENCES watches(id) ON DELETE CASCADE,
	external_id TEXT NOT NULL DEFAULT '',
	fingerprint TEXT NOT NULL,
	date_time TIMESTAMP NOT NULL,
	service_id INTEGER NOT NULL,
	service_name TEXT NOT NULL,
	doctor_id INTEGER NOT NULL DEFAULT 0,
	doctor_name TEXT NOT NULL DEFAULT '',
	facility_id INTEGER NOT NULL DEFAULT 0,
	facility_name TEXT NOT NULL DEFAULT '',
	address TEXT NOT NULL DEFAULT '',
	city_id INTEGER NOT NULL DEFAULT 0,
	city_name TEXT NOT NULL DEFAULT '',
	booking_url TEXT NOT NULL DEFAULT '',
	first_seen_at TIMESTAMP NOT NULL,
	last_seen_at TIMESTAMP NOT NULL,
	last_notified_at TIMESTAMP,
	seen_count INTEGER NOT NULL DEFAULT 1,
	status TEXT NOT NULL,
	raw_payload TEXT NOT NULL DEFAULT '',
	UNIQUE(watch_id, fingerprint)
);

CREATE TABLE IF NOT EXISTS notification_history (
	id INTEGER PRIMARY KEY,
	watch_id INTEGER NOT NULL REFERENCES watches(id) ON DELETE CASCADE,
	appointment_history_id INTEGER NOT NULL REFERENCES appointment_history(id) ON DELETE CASCADE,
	appointment_fingerprint TEXT NOT NULL,
	sent_at TIMESTAMP NOT NULL,
	message_id INTEGER NOT NULL DEFAULT 0,
	status TEXT NOT NULL,
	error TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS luxmed_cities (
	id INTEGER PRIMARY KEY,
	name TEXT NOT NULL,
	updated_at TIMESTAMP NOT NULL
);

CREATE TABLE IF NOT EXISTS luxmed_procedures (
	id INTEGER NOT NULL,
	city_id INTEGER NOT NULL,
	city_name TEXT NOT NULL,
	name TEXT NOT NULL,
	is_recent BOOLEAN NOT NULL DEFAULT 0,
	updated_at TIMESTAMP NOT NULL,
	PRIMARY KEY (id, city_id)
);

CREATE TABLE IF NOT EXISTS luxmed_facilities (
	id INTEGER NOT NULL,
	city_id INTEGER NOT NULL,
	city_name TEXT NOT NULL,
	procedure_id INTEGER NOT NULL,
	name TEXT NOT NULL,
	address TEXT NOT NULL DEFAULT '',
	updated_at TIMESTAMP NOT NULL,
	PRIMARY KEY (id, city_id, procedure_id)
);

CREATE TABLE IF NOT EXISTS favorite_facilities (
	user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	city_id INTEGER NOT NULL,
	procedure_id INTEGER NOT NULL,
	facility_id INTEGER NOT NULL,
	facility_name TEXT NOT NULL,
	last_used_at TIMESTAMP NOT NULL,
	PRIMARY KEY (user_id, city_id, procedure_id, facility_id)
);

CREATE INDEX IF NOT EXISTS idx_watches_due ON watches(status, last_checked_at);
CREATE INDEX IF NOT EXISTS idx_history_watch ON appointment_history(watch_id, date_time);
CREATE INDEX IF NOT EXISTS idx_notifications_fingerprint ON notification_history(watch_id, appointment_fingerprint, sent_at);
CREATE INDEX IF NOT EXISTS idx_luxmed_procedures_search ON luxmed_procedures(city_id, name);
CREATE INDEX IF NOT EXISTS idx_luxmed_facilities_lookup ON luxmed_facilities(city_id, procedure_id, name);
`, domain.WatchStatusActive, domain.WatchStatusPaused, domain.WatchStatusDeleted)

var compatibilityMigrations = []string{
	`ALTER TABLE users ADD COLUMN preferred_city_id INTEGER`,
	`ALTER TABLE users ADD COLUMN preferred_city_name TEXT NOT NULL DEFAULT ''`,
}
