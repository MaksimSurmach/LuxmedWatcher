package storage

import (
	"errors"
	"log"
	"time"

	"LuxmedWatcher/internal/config"
	"LuxmedWatcher/internal/domain"

	"github.com/jmoiron/sqlx"
	_ "github.com/mattn/go-sqlite3"
)

type SQLiteStorage struct {
	db *sqlx.DB
}

func NewSQLiteStorage(dbPath string) (*SQLiteStorage, error) {
	db, err := sqlx.Open("sqlite3", dbPath)
	if err != nil {
		return nil, err
	}

	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(time.Hour)

	s := &SQLiteStorage{db: db}
	if err := s.initSchema(); err != nil {
		log.Printf("failed to initialize schema: %v", err)
		db.Close()
		return nil, err
	}

	return s, nil
}

func (s *SQLiteStorage) Close() error {
	return s.db.Close()
}

func (s *SQLiteStorage) initSchema() error {
	schema := `
	CREATE TABLE IF NOT EXISTS config_store (
		username TEXT NOT NULL,
		password TEXT NOT NULL,
		check_interval INTEGER NOT NULL,
		language TEXT NOT NULL,
	);

	CREATE TABLE IF NOT EXISTS notification_channels (
		id INTEGER autoincrement PRIMARY KEY,
		name TEXT NOT NULL,
		config TEXT NOT NULL
	);

	CREATE TABLE IF NOT EXISTS appointments (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		doctor_id INTEGER NOT NULL,
		clinic_id INTEGER NOT NULL,
		service_id INTEGER NOT NULL,
		city_id INTEGER NOT NULL,
		language_id INTEGER NOT NULL,
	);

	CREATE TABLE IF NOT EXISTS appointments_notified (
        id INTEGER PRIMARY KEY AUTOINCREMENT,
		appointment_search_id INTEGER NOT NULL,
        doctor_id INTEGER NOT NULL,
        clinic_id INTEGER NOT NULL,
        date_from TEXT NOT NULL
    );

	CREATE TABLE IF NOT EXISTS appointment_search_tasks (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		apointment_id INTEGER NOT NULL,
		created_date TEXT NOT NULL,
		last_checked_at TEXT NOT NULL,
		status TEXT NOT NULL,
		notification_channel TEXT NOT NULL,
		notification_destination TEXT NOT NULL
	);

	CREATE TABLE IF NOT EXISTS cities (
		id INTEGER PRIMARY KEY,
		name TEXT NOT NULL
	);

	CREATE TABLE IF NOT EXISTS service_variant_groups (
		id INTEGER PRIMARY KEY,
		name TEXT NOT NULL
	);

	CREATE TABLE IF NOT EXISTS service_variants (
		id INTEGER PRIMARY KEY,
		AcademicTitle TEXT NOT NULL,
		FirstName TEXT NOT NULL,
		LastName TEXT NOT NULL,
		Facilities TEXT NOT NULL,
		group_id INTEGER NOT NULL
	);

	CREATE TABLE IF NOT EXISTS doctors (
		id INTEGER PRIMARY KEY,
		name TEXT NOT NULL
	);

	CREATE TABLE IF NOT EXISTS clinics (
		id INTEGER PRIMARY KEY,
		name TEXT NOT NULL
	);

	CREATE TABLE IF NOT EXISTS languages (
		id INTEGER PRIMARY KEY,
		name TEXT NOT NULL
	);`

	_, err := s.db.Exec(schema)
	return err
}
func (s *SQLiteStorage) IsAppointmentNotified(app domain.Appointment) (bool, error) {
	var count int
	dateFrom := app.DateTimeFrom.Format(time.RFC3339)

	err := s.db.Get(&count, `
        SELECT COUNT(*) FROM appointments_notified
        WHERE doctor_id = ? AND clinic_id = ? AND date_from = ?
    `, app.DoctorID, app.ClinicID, dateFrom)

	if err != nil {
		return false, err
	}
	return count > 0, nil
}

func (s *SQLiteStorage) MarkAppointmentsNotified(apps []domain.Appointment) error {
	tx, err := s.db.Beginx()
	if err != nil {
		return err
	}

	defer func() {
		if err != nil {
			tx.Rollback()
			return
		}
	}()

	for _, app := range apps {
		_, err := tx.Exec(`
            INSERT INTO appointments_notified (appointment_search_id, doctor_id, clinic_id, date_from)
            VALUES (?, ?, ?, ?)
        `, app.DoctorID, app.ClinicID, app.DateTimeFrom.Format(time.RFC3339))
		// add appointment_search_id to struct
		if err != nil {
			return err
		}
	}

	return tx.Commit()
}

func (s *SQLiteStorage) UpdateLastChecked(taskID int, checkedAt time.Time) error {
	_, err := s.db.Exec(`
        UPDATE appointment_search_tasks
        SET last_checked_at = ?
        WHERE id = ?
    `, checkedAt.Format(time.RFC3339), time.Now().Format(time.RFC3339), taskID)

	return err
}

func (s *SQLiteStorage) DeleteAppointmentSearchTask(taskID int) error {
	if s.db == nil {
		return errors.New("db not initialized")
	}

	_, err := s.db.Exec("DELETE FROM appointment_search_tasks WHERE id = ?", taskID)
	return err
}

func (s *SQLiteStorage) IncrementRetryCount(taskID int) error {
	_, err := s.db.Exec(`
        UPDATE appointment_search_tasks
        SET retry_count = retry_count + 1, updated_at = ?
        WHERE id = ?
    `, time.Now().Format(time.RFC3339), taskID)

	return err
}

func (s *SQLiteStorage) GetAppointmentSearchTask(taskID int) (*domain.AppointmentSearchTask, error) {
	var task domain.AppointmentSearchTask
	err := s.db.Get(&task, "SELECT * FROM appointment_search_tasks WHERE id = ?", taskID)
	return &task, err
}

func (s *SQLiteStorage) GetAppointmentSearchTasks() ([]domain.AppointmentSearchTask, error) {
	var tasks []domain.AppointmentSearchTask
	err := s.db.Select(&tasks, "SELECT * FROM appointment_search_tasks")
	return tasks, err
}

func (s *SQLiteStorage) CreateAppointmentSearchTask(task domain.AppointmentSearchTask) (int, error) {
	res, err := s.db.Exec(`
		INSERT INTO appointment_search_tasks (created_at, last_checked_at, status, notification_channel, notification_destination)
		VALUES (?, ?, ?, ?, ?, ?)
	`, time.Now().Format(time.RFC3339), time.Now().Format(time.RFC3339), task.Status, task.NotificationChannel, task.NotificationDestination)
	if err != nil {
		return 0, err
	}

	id, err := res.LastInsertId()
	return int(id), err
}

func (s *SQLiteStorage) SaveConfig(c *config.Config) error {
	_, err := s.db.Exec(`
		INSERT INTO config_store (username, password, check_interval, language)
		VALUES (?, ?, ?, ?)
	`, c.Credentials.Username, c.Credentials.Password, c.Settings.CheckIntervalSec, c.Settings.Language)
	return err
}

func (s *SQLiteStorage) GetConfig() (*config.Config, error) {
	var sc config.SettingsConfig
	var cc config.CredentialsConfig

	err := s.db.Get(&sc, "SELECT * FROM config_store")
	if err != nil {
		return nil, err
	}
	err = s.db.Get(&cc, "SELECT * FROM config_store")
	if err != nil {
		return nil, err
	}
	return &config.Config{
		Credentials: cc,
		Settings:    sc,
	}, nil
}

func (s *SQLiteStorage) SaveAppointment(app domain.Appointment) error {
	_, err := s.db.Exec(`
		INSERT INTO appointments (doctor_id, clinic_id, service_id, city_id, language_id)
		VALUES (?, ?, ?, ?, ?)
	`, app.DoctorID, app.ClinicID, app.ServiceID, app.CityID, app.LanguageID)
	return err
}

func (s *SQLiteStorage) GetAppointments() ([]domain.Appointment, error) {
	var apps []domain.Appointment
	err := s.db.Select(&apps, "SELECT * FROM appointments")
	return apps, err
}

func (s *SQLiteStorage) SaveCity(city domain.City) error {
	_, err := s.db.Exec(`
		INSERT INTO cities (id, name)
		VALUES (?, ?)
	`, city.ID, city.Name)
	return err
}

func (s *SQLiteStorage) GetCities() ([]domain.City, error) {
	var cities []domain.City
	err := s.db.Select(&cities, "SELECT * FROM cities")
	return cities, err
}

func (s *SQLiteStorage) GetCityName(cityID int) (string, error) {
	var name string
	err := s.db.Get(&name, "SELECT name FROM cities WHERE id = ?", cityID)
	return name, err
}

func (s *SQLiteStorage) SaveServiceVariantGroup(group domain.ServiceVariantGroup) error {
	_, err := s.db.Exec(`
		INSERT INTO service_variant_groups (id, name)
		VALUES (?, ?)
	`, group.ID, group.Name)
	return err
}

func (s *SQLiteStorage) GetServiceVariantGroups() ([]domain.ServiceVariantGroup, error) {
	var groups []domain.ServiceVariantGroup
	err := s.db.Select(&groups, "SELECT * FROM service_variant_groups")
	return groups, err
}

func (s *SQLiteStorage) GetServiceName(serviceID int) (string, error) {
	var name string
	err := s.db.Get(&name, "SELECT name FROM service_variants WHERE id = ?", serviceID)
	return name, err
}

func (s *SQLiteStorage) SaveDoctor(doctor domain.Doctor) error {
	_, err := s.db.Exec(`
		INSERT INTO doctors (id, FirstName, LastName, AcademicTitle, Facilities)
		VALUES (?, ?)
	`, doctor.ID, doctor.FirstName, doctor.LastName, doctor.AcademicTitle, doctor.Facilities)
	return err
}

func (s *SQLiteStorage) GetDoctors() ([]domain.Doctor, error) {
	var doctors []domain.Doctor
	err := s.db.Select(&doctors, "SELECT * FROM doctors")
	return doctors, err
}

func (s *SQLiteStorage) GetDoctorName(doctorID int) (string, error) {
	var name string
	err := s.db.Get(&name, "SELECT name FROM doctors WHERE id = ?", doctorID)
	return name, err
}

func (s *SQLiteStorage) SaveClinic(clinic domain.Facilities) error {
	_, err := s.db.Exec(`
		INSERT INTO clinics (id, name)
		VALUES (?, ?)
	`, clinic.ID, clinic.Name)
	return err
}

func (s *SQLiteStorage) GetClinics() ([]domain.Facilities, error) {
	var clinics []domain.Facilities
	err := s.db.Select(&clinics, "SELECT * FROM clinics")
	return clinics, err
}

func (s *SQLiteStorage) GetClinicName(clinicID int) (string, error) {
	var name string
	err := s.db.Get(&name, "SELECT name FROM clinics WHERE id = ?", clinicID)
	return name, err
}

func (s *SQLiteStorage) SaveLanguage(language domain.Languages) error {
	_, err := s.db.Exec(`
		INSERT INTO languages (id, name)
		VALUES (?, ?)
	`, language.ID, language)
	return err
}

func (s *SQLiteStorage) GetLanguages() ([]domain.Languages, error) {
	var languages []domain.Languages
	err := s.db.Select(&languages, "SELECT * FROM languages")
	return languages, err
}

func (s *SQLiteStorage) GetLanguageName(languageID int) (string, error) {
	var name string
	err := s.db.Get(&name, "SELECT name FROM languages WHERE id = ?", languageID)
	return name, err
}
