package storage

import (
	"LuxmedWatcher/internal/domain"
	"fmt"
	"time"
)

// Abstract storage interface
type Storage interface {
	Close() error

	// Credentials
	SaveConfigParam(key string, value string) error
	GetConfigParam(key string) (string, error)

	// Appointment records
	GetAppointmentRecords() ([]*domain.AppointmentRecord, error)
	GetAppointmentRecord(id int) (*domain.AppointmentRecord, error)
	SaveAppointmentRecord(record *domain.AppointmentRecord) (int, error)
	DeleteAppointmentRecord(id int) error

	// Appointment search tasks
	GetAppointmentSearchTasks() ([]*domain.AppointmentSearchTask, error)
	GetAppointmentSearchTask(id int) (*domain.AppointmentSearchTask, error)
	SaveAppointmentSearchTask(task *domain.AppointmentSearchTask) error
	DeleteAppointmentSearchTask(id int) error
	GetActiveAppointmentSearchTasks() ([]*domain.AppointmentSearchTask, error)
	UpdateLastCheckedTask(id int, lastChecked time.Time) error

	// Notification channels
	GetNotificationChannels() ([]*domain.NotificationChannels, error)
	GetNotificationChannel(id int) (*domain.NotificationChannels, error)
	SaveNotificationChannel(channel *domain.NotificationChannels) error
	DeleteNotificationChannel(id int) error
	SaveAppointmentNotified(searchID int, appointmentID int, doctorID int, clinicID int, dateFrom time.Time) error
	IsAppointmentNotified(searchID int, appointmentID int, doctorID int, clinicID int, dateFrom time.Time) (bool, error)

	// Reference data
	GetCities() ([]*domain.City, error)
	GetCity(id int) (*domain.City, error)
	SaveCity(city *domain.City) error
	DeleteCity(id int) error

	GetServices() ([]*domain.ServiceVariantGroup, error)
	GetService(id int) (*domain.ServiceVariantGroup, error)
	SaveService(service *domain.ServiceVariantGroup) error
	DeleteService(id int) error

	GetDoctors() ([]*domain.Doctor, error)
	GetDoctor(id int) (*domain.Doctor, error)
	SaveDoctor(doctor *domain.Doctor) error
	DeleteDoctor(id int) error

	GetClinics() ([]*domain.Facilities, error)
	GetClinic(id int) (*domain.Facilities, error)
	SaveClinic(clinic *domain.Facilities) error
	DeleteClinic(id int) error

	GetLanguages() ([]*domain.Languages, error)
	GetLanguage(id int) (*domain.Languages, error)
	SaveLanguage(language *domain.Languages) error
	DeleteLanguage(id int) error
}

// NewStorage creates a new storage instance based on the provided storage type
func NewStorage(storageType string, dbPath string) (Storage, error) {
	switch storageType {
	case "sqlite":
		sqllite, err := NewSQLiteStorage(dbPath)
		if err != nil {
			return nil, err
		}
		return sqllite, nil
	default:
		panic(fmt.Sprintf("unsupported storage type: %s", storageType))
	}
}
