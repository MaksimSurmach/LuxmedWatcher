package storage

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
	"encoding/json"

	"LuxmedWatcher/internal/config"
	"LuxmedWatcher/internal/domain"

	_ "github.com/mattn/go-sqlite3"
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
		user TEXT,
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

func (s *SQLiteStorage) IsAppointmentNotified(app domain.Appointment) (bool, error) {
	if s.db == nil {
		return false, errors.New("db not initialized")
	}

	query := `
	SELECT COUNT(*) FROM appointments_notified 
	WHERE doctor_id = ? AND clinic_id = ? AND date_from = ? and user = ?;
	`
	dateFrom := app.DateTimeFrom.Format(time.RFC3339)
	var count int
	err := s.db.QueryRow(query, app.DoctorID, app.ClinicID, dateFrom).Scan(&count)
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
	INSERT INTO appointments_notified (doctor_id, clinic_id, date_from)
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
		)
		if err != nil {
			_ = tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

func (s *SQLiteStorage) DeleteAppointmentSearchTask(taskID int) error {
	if s.db == nil {
		return errors.New("db not initialized")
	}

	_, err := s.db.Exec("DELETE FROM appointment_search_tasks WHERE id = ?", taskID)
	return err
}

func (s *SQLiteStorage) GetConfig() (*config.Config, error) {
	if s.db == nil {
		return nil, errors.New("db not initialized")
	}

	var content string
	err := s.db.QueryRow("SELECT content FROM config_store WHERE id = 1").Scan(&content)
	if err != nil {
		return nil, err
	}

	cfg := &config.Config{}
	err = json.Unmarshal([]byte(content), cfg)
	if err != nil {
		return nil, err
	}
	return cfg, nil
}

func (s *SQLiteStorage) SaveConfig(cfg *config.Config) error {
	if s.db == nil {
		return errors.New("db not initialized")
	}

	content, err := json.Marshal(cfg)
	if err != nil {
		return err
	}

	_, err = s.db.Exec("INSERT OR REPLACE INTO config_store (id, content) VALUES (1, ?)", content)
	return err
}

func (s *SQLiteStorage) createCityTable() error {
	_, err := s.db.Exec(`
	CREATE TABLE IF NOT EXISTS cities (
		id INTEGER PRIMARY KEY,
		name TEXT NOT NULL
	);
	`)
	return err
}

func (s *SQLiteStorage) createServiceVariantGroupTable() error {
	_, err := s.db.Exec(`
	CREATE TABLE IF NOT EXISTS service_variant_groups (
		id INTEGER PRIMARY KEY,
		name TEXT NOT NULL
	);
	`)
	return err
}

