package storage

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"LuxmedWatcher/internal/domain"
)

type SQLiteStorage struct {
	dbPath string
	db     *sql.DB
}

func NewSQLiteStorage(dbPath string) *SQLiteStorage {
	return &SQLiteStorage{dbPath: dbPath}
}

func (s *SQLiteStorage) Init() error {
	db, err := sql.Open("sqlite3", s.dbPath)
	if err != nil {
		return err
	}
	s.db = db

	_, err = s.db.Exec(`
	CREATE TABLE IF NOT EXISTS config_store (
		id INTEGER PRIMARY KEY,
		content TEXT NOT NULL
	);
	`)
	if err != nil {
		return fmt.Errorf("create config_store: %w", err)
	}

	_, err = s.db.Exec(`
	CREATE TABLE IF NOT EXISTS appointments_notified (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		doctor_id INTEGER NOT NULL,
		clinic_id INTEGER NOT NULL,
		date_from TEXT NOT NULL,
		date_to TEXT NOT NULL
	);
	`)
	if err != nil {
		return fmt.Errorf("create appointments_notified: %w", err)
	}

	return nil
}

func (s *SQLiteStorage) Close() error {
	if s.db != nil {
		return s.db.Close()
	}
	return nil
}

func (s *SQLiteStorage) IsAlreadyNotified(app domain.Appointment) (bool, error) {
	if s.db == nil {
		return false, errors.New("db not initialized")
	}

	query := `
	SELECT COUNT(*) FROM appointments_notified 
	WHERE doctor_id = ? AND clinic_id = ? AND date_from = ? AND date_to = ?
	`
	dateFrom := app.DateTimeFrom.Format(time.RFC3339)
	dateTo := app.DateTimeTo.Format(time.RFC3339)
	var count int
	err := s.db.QueryRow(query, app.DoctorID, app.ClinicID, dateFrom, dateTo).Scan(&count)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

func (s *SQLiteStorage) MarkAppointmentsNotified(apps []domain.Appointment) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	stmt, err := tx.Prepare(`
	INSERT INTO appointments_notified (doctor_id, clinic_id, date_from, date_to)
	VALUES (?, ?, ?, ?)
	`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, app := range apps {
		_, err := stmt.Exec(
			app.DoctorID,
			app.ClinicID,
			app.DateTimeFrom.Format(time.RFC3339),
			app.DateTimeTo.Format(time.RFC3339),
		)
		if err != nil {
			_ = tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}
