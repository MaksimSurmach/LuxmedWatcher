package storage

import (
	"log"
	"time"

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
		key TEXT NOT NULL,
		value TEXT NOT NULL
	);

	CREATE TABLE IF NOT EXISTS notification_channels (
		id INTEGER autoincrement PRIMARY KEY,
		channel_type TEXT NOT NULL,
		name TEXT NOT NULL,
		config TEXT NOT NULL
	);

	CREATE TABLE IF NOT EXISTS appointments (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name TEXT NOT NULL,
		doctor_id INTEGER NOT NULL,
		clinic_id INTEGER NOT NULL,
		service_variant_id INTEGER NOT NULL,
		clinic_id INTEGER NOT NULL,
		city_id INTEGER NOT NULL,
		language_id INTEGER NOT NULL,
		created_at TEXT NOT NULL
	);

	CREATE TABLE IF NOT EXISTS appointments_notified (
		appointment_search_id INTEGER NOT NULL,
		apointment_id INTEGER NOT NULL,
        doctor_id INTEGER NOT NULL,
        clinic_id INTEGER NOT NULL,
        date_from TEXT NOT NULL
    );

	CREATE TABLE IF NOT EXISTS appointment_search_tasks (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		apointment_id INTEGER NOT NULL,
		notification_channel_id INTEGER NOT NULL,
		search_days INTEGER NOT NULL,
		created_at TEXT NOT NULL,
		last_checked_at TEXT NOT NULL,
		status TEXT NOT NULL,
		is_active BOOLEAN NOT NULL
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

// SaveConfigParam saves a configuration parameter to the database
func (s *SQLiteStorage) SaveConfigParam(key, value string) error {
	_, err := s.db.Exec(`
		INSERT INTO config_store (key, value)
		VALUES (?, ?)
		`, key, value)
	return err
}

// GetConfigParam returns a configuration parameter by its key
func (s *SQLiteStorage) GetConfigParam(key string) (string, error) {
	var value string
	err := s.db.Get(&value, "SELECT value FROM config_store WHERE key = ?", key)
	return value, err
}

// Appointments
// SaveAppointmentRecord saves an appointment record to the database
func (s *SQLiteStorage) SaveAppointmentRecord(appoint *domain.AppointmentRecord) (int, error) {
	_, err := s.db.Exec(`
		INSERT INTO appointments (name, doctor_id, clinic_id, service_variant_id, city_id, language_id, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`, appoint.Name, appoint.DoctorID, appoint.ClinicID, appoint.ServiceVariantID, appoint.CityID, appoint.LanguageID, time.Now().Format(time.RFC3339))

	if err != nil {
		return 0, err
	}

	var id int
	err = s.db.Get(&id, "SELECT last_insert_rowid()")
	return id, err
}

// GetAppointmentRecords returns all appointment records
func (s *SQLiteStorage) GetAppointmentRecords() ([]*domain.AppointmentRecord, error) {
	var appointments []*domain.AppointmentRecord
	err := s.db.Select(&appointments, "SELECT * FROM appointments")
	return appointments, err
}

// GetAppointmentRecord returns an appointment record by its ID
func (s *SQLiteStorage) GetAppointmentRecord(id int) (*domain.AppointmentRecord, error) {
	var appointment *domain.AppointmentRecord
	err := s.db.Get(&appointment, "SELECT * FROM appointments WHERE id = ?", id)
	return appointment, err
}

// DeleteAppointmentRecord deletes an appointment record by its ID
func (s *SQLiteStorage) DeleteAppointmentRecord(id int) error {
	_, err := s.db.Exec("DELETE FROM appointments WHERE id = ?", id)
	return err
}

// AppointmentSearchTask
// DeleteAppointmentSearchTask deletes an appointment search task by its ID
func (s *SQLiteStorage) DeleteAppointmentSearchTask(taskID int) error {
	_, err := s.db.Exec("DELETE FROM appointment_search_tasks WHERE id = ?", taskID)
	return err
}

// GetAppointmentSearchTask returns an appointment search task by its ID
func (s *SQLiteStorage) GetAppointmentSearchTask(taskID int) (*domain.AppointmentSearchTask, error) {
	var task domain.AppointmentSearchTask
	err := s.db.Get(&task, "SELECT * FROM appointment_search_tasks WHERE id = ?", taskID)
	return &task, err
}

// GetAppointmentSearchTasks returns all appointment search tasks
func (s *SQLiteStorage) GetAppointmentSearchTasks() ([]*domain.AppointmentSearchTask, error) {
	var tasks []*domain.AppointmentSearchTask
	err := s.db.Select(&tasks, "SELECT * FROM appointment_search_tasks")
	return tasks, err
}

// SaveAppointmentSearchTask saves an appointment search task to the database
func (s *SQLiteStorage) SaveAppointmentSearchTask(task *domain.AppointmentSearchTask) error {
	_, err := s.db.Exec(`
		INSERT INTO appointment_search_tasks (created_at, last_checked_at, status, notification_channel, apointment_id, search_days, is_active)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`, time.Now().Format(time.RFC3339), task.LastCheckedAt, task.Status, task.NotificationChannelID, task.AppointmentID, task.SearchDays, task.IsActive)
	return err
}

// GetActiveAppointmentSearchTasks returns all active appointment search tasks
func (s *SQLiteStorage) GetActiveAppointmentSearchTasks() ([]*domain.AppointmentSearchTask, error) {
	var tasks []*domain.AppointmentSearchTask
	err := s.db.Select(&tasks, "SELECT * FROM appointment_search_tasks WHERE is_active = 1")
	return tasks, err
}

// UpdateLastCheckedTask updates the last checked time for an appointment search task
func (s *SQLiteStorage) UpdateLastCheckedTask(taskID int, lastChecked time.Time) error {
	_, err := s.db.Exec("UPDATE appointment_search_tasks SET last_checked_at = ? WHERE id = ?", lastChecked.Format(time.RFC3339), taskID)
	return err
}

// NotificationChannels
// SaveNotificationChannel saves a notification channel to the database
func (s *SQLiteStorage) SaveNotificationChannel(channel *domain.NotificationChannels) error {
	_, err := s.db.Exec(`
		INSERT INTO notification_channels (name, channel_type, config)
		VALUES (?, ?, ?)
		`, channel.Name, channel.ChannelType, channel.Config)
	return err
}

// GetNotificationChannels returns all notification channels
func (s *SQLiteStorage) GetNotificationChannels() ([]*domain.NotificationChannels, error) {
	var channels []*domain.NotificationChannels
	err := s.db.Select(&channels, "SELECT * FROM notification_channels")
	return channels, err
}

// GetNotificationChannel returns a notification channel by its ID
func (s *SQLiteStorage) GetNotificationChannel(channelID int) (*domain.NotificationChannels, error) {
	var channel *domain.NotificationChannels
	err := s.db.Get(&channel, "SELECT * FROM notification_channels WHERE id = ?", channelID)
	return channel, err
}

// DeleteNotificationChannel deletes a notification channel by its ID
func (s *SQLiteStorage) DeleteNotificationChannel(channelID int) error {
	_, err := s.db.Exec("DELETE FROM notification_channels WHERE id = ?", channelID)
	return err
}

// SaveAppointmentNotified saves an appointment notified record to the database
func (s *SQLiteStorage) SaveAppointmentNotified(searchID int, appointmentID int, doctorID int, clinicID int, dateFrom time.Time) error {
	_, err := s.db.Exec(`
		INSERT INTO appointments_notified (appointment_search_id, appointment_id, doctor_id, clinic_id, date_from)
		VALUES (?, ?, ?, ?, ?)
	`, searchID, appointmentID, doctorID, clinicID, dateFrom.Format(time.RFC3339))
	return err
}

// GetAppointmentNotified returns an appointment notified record by its ID
func (s *SQLiteStorage) IsAppointmentNotified(searchID int, appointmentID int, doctorID int, clinicID int, dateFrom time.Time) (bool, error) {
	var count int
	err := s.db.Get(&count, "SELECT COUNT(*) FROM appointments_notified WHERE appointment_search_id = ? AND appointment_id = ? AND doctor_id = ? AND clinic_id = ? AND date_from = ?", searchID, appointmentID, doctorID, clinicID, dateFrom.Format(time.RFC3339))
	return count > 0, err
}

// Reference data

// Cities CRUD
// SaveCity saves a city to the database
func (s *SQLiteStorage) SaveCity(city *domain.City) error {
	_, err := s.db.Exec(`
		INSERT INTO cities (id, name)
		VALUES (?, ?)
	`, city.ID, city.Name)
	return err
}

// GetCities returns all cities from the database
func (s *SQLiteStorage) GetCities() ([]*domain.City, error) {
	var cities []*domain.City
	err := s.db.Select(&cities, "SELECT * FROM cities")
	return cities, err
}

// GetCity returns a city by its ID
func (s *SQLiteStorage) GetCity(cityID int) (*domain.City, error) {
	var city *domain.City
	err := s.db.Get(&city, "SELECT * FROM cities WHERE id = ?", cityID)
	return city, err
}

// DeleteCity deletes a city by its ID
func (s *SQLiteStorage) DeleteCity(cityID int) error {
	_, err := s.db.Exec("DELETE FROM cities WHERE id = ?", cityID)
	return err
}

// Services CRUD
// GetService returns services with id
func (s *SQLiteStorage) GetService(serviceID int) (*domain.ServiceVariantGroup, error) {
	var name *domain.ServiceVariantGroup
	err := s.db.Get(&name, "SELECT name FROM service_variant_groups WHERE id = ?", serviceID)
	return name, err
}

// GetServices returns all services
func (s *SQLiteStorage) GetServices() ([]*domain.ServiceVariantGroup, error) {
	var services []*domain.ServiceVariantGroup
	err := s.db.Select(&services, "SELECT * FROM service_variant_groups")
	return services, err
}

// SaveService saves a service to the database
func (s *SQLiteStorage) SaveService(service *domain.ServiceVariantGroup) error {
	_, err := s.db.Exec(`
		INSERT INTO service_variant_groups (id, name)
		VALUES (?, ?)
		`, service.ID, service.Name)
	return err
}

// DeleteService remove service form db by id
func (s *SQLiteStorage) DeleteService(id int) error {
	_, err := s.db.Exec("DELETE FROM service_variant_groups WHERE id = ?", id)
	return err
}

// Doctors
// GetDoctors reset all doctors
func (s *SQLiteStorage) GetDoctors() ([]*domain.Doctor, error) {
	var doctors []*domain.Doctor
	err := s.db.Select(&doctors, "SELECT * FROM doctors")
	return doctors, err
}

// GetDoctor get doctor by id
func (s *SQLiteStorage) GetDoctor(doctorID int) (*domain.Doctor, error) {
	var doc *domain.Doctor
	err := s.db.Get(&doc, "SELECT name FROM doctors WHERE id = ?", doctorID)
	return doc, err
}

// SaveDoctor save doctor
func (s *SQLiteStorage) SaveDoctor(doctor *domain.Doctor) error {
	_, err := s.db.Exec(`
		INSERT INTO doctors (id, FirstName, LastName, AcademicTitle, Facilities)
		VALUES (?, ?)
	`, doctor.ID, doctor.FirstName, doctor.LastName, doctor.AcademicTitle, doctor.Facilities)
	return err
}

// DeleteDoctor delete doctor by id
func (s *SQLiteStorage) DeleteDoctor(doctorID int) error {
	_, err := s.db.Exec("DELETE FROM doctors WHERE id = ?", doctorID)
	return err
}

// Clinics
// GetClinics
func (s *SQLiteStorage) GetClinics() ([]*domain.Facilities, error) {
	var clinics []*domain.Facilities
	err := s.db.Select(&clinics, "SELECT * FROM clinics")
	return clinics, err
}

// GetClinic get clinic by id
func (s *SQLiteStorage) GetClinic(clinicID int) (*domain.Facilities, error) {
	var clinic *domain.Facilities
	err := s.db.Get(&clinic, "SELECT name FROM clinics WHERE id = ?", clinicID)
	return clinic, err
}

// SaveClinic save clinic record
func (s *SQLiteStorage) SaveClinic(clinic *domain.Facilities) error {
	_, err := s.db.Exec(`
		INSERT INTO clinics (id, name)
		VALUES (?, ?)
	`, clinic.ID, clinic.Name)
	return err
}

// DeleteClinic delete clinic from db
func (s *SQLiteStorage) DeleteClinic(clinicID int) error {
	_, err := s.db.Exec("DELETE FROM clinics WHERE id = ?", clinicID)
	return err
}

// Languages
// GetLanguages get all languages
func (s *SQLiteStorage) GetLanguages() ([]*domain.Languages, error) {
	var languages []*domain.Languages
	err := s.db.Select(&languages, "SELECT * FROM languages")
	return languages, err
}

// GetLanguage get language by id
func (s *SQLiteStorage) GetLanguage(languageID int) (*domain.Languages, error) {
	var lan *domain.Languages
	err := s.db.Get(&lan, "SELECT name FROM languages WHERE id = ?", languageID)
	return lan, err
}

// SaveLanguage save language
func (s *SQLiteStorage) SaveLanguage(language *domain.Languages) error {
	_, err := s.db.Exec(`
		INSERT INTO languages (id, name)
		VALUES (?, ?)
	`, language.ID, language)
	return err
}

// DeleteLanguage delete language
func (s *SQLiteStorage) DeleteLanguage(languageID int) error {
	_, err := s.db.Exec("DELETE FROM languages WHERE id = ?", languageID)
	return err
}
