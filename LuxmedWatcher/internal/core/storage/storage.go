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
	// SaveCredentials saves the credentials
	SaveConfigParam(key string, value string) error
	// GetConfigParam returns a single configuration parameter by key
	GetConfigParam(key string) (string, error)

	// Appointment records
	// GetAppointmentRecords returns a list of appointment records
	GetAppointmentRecords() ([]*domain.AppointmentRecord, error)
	// GetAppointmentRecord returns a single appointment record by ID
	GetAppointmentRecord(id int) (*domain.AppointmentRecord, error)
	// SaveAppointmentRecord saves an appointment record
	SaveAppointmentRecord(record *domain.AppointmentRecord) (int, error)
	// DeleteAppointmentRecord deletes an appointment record by ID
	DeleteAppointmentRecord(id int) error

	// Appointment search tasks
	// GetAppointmentSearchTasks returns a list of appointment search tasks
	GetAppointmentSearchTasks() ([]*domain.AppointmentSearchTask, error)
	// GetAppointmentSearchTask returns a single appointment search task by ID
	GetAppointmentSearchTask(id int) (*domain.AppointmentSearchTask, error)
	// SaveAppointmentSearchTask saves an appointment search task
	SaveAppointmentSearchTask(task *domain.AppointmentSearchTask) error
	// DeleteAppointmentSearchTask deletes an appointment search task by ID
	DeleteAppointmentSearchTask(id int) error
	// GetActiveAppointmentSearchTasks returns a list of active appointment search tasks
	GetActiveAppointmentSearchTasks() ([]*domain.AppointmentSearchTask, error)
	// UpdateLastCheckedTask updates the last checked timestamp for the task
	UpdateLastCheckedTask(id int, lastChecked time.Time) error

	// Notification channels
	// GetNotificationChannels returns a list of notification channels
	GetNotificationChannels() ([]*domain.NotificationChannels, error)
	// GetNotificationChannel returns a single notification channel by ID
	GetNotificationChannel(id int) (*domain.NotificationChannels, error)
	// SaveNotificationChannel saves a notification channel
	SaveNotificationChannel(channel *domain.NotificationChannels) error
	// DeleteNotificationChannel deletes a notification channel by ID
	DeleteNotificationChannel(id int) error
	// GetNotificationChannelByType returns a single notification channel by type
	SaveAppointmentNotification(appointment *domain.AppointmentSearchResult) error
	// GetPendingNotifications returns all pending notifications
	GetPendingNotifications() ([]*domain.AppointmentSearchResult, error)
	// SetNotificationStatus sets the status of a notification
	SetNotificationStatus(notificationID int, status string) error

	// Reference data
	// GetCities returns a list of cities
	GetCities() ([]*domain.City, error)
	// GetCity returns a single city by ID
	GetCity(id int) (*domain.City, error)
	// SaveCity saves a city
	SaveCity(city *domain.City) error
	// DeleteCity deletes a city by ID
	DeleteCity(id int) error

	// GetServices returns a list of services
	GetServices() ([]*domain.ServiceVariantGroup, error)
	// GetService returns a single service by ID
	GetService(id int) (*domain.ServiceVariantGroup, error)
	// SaveService saves a service
	SaveService(service *domain.ServiceVariantGroup) error
	// DeleteService deletes a service by ID
	DeleteService(id int) error

	// GetDoctors returns a list of doctors
	GetDoctors() ([]*domain.Doctor, error)
	// GetDoctor returns a single doctor by ID
	GetDoctor(id int) (*domain.Doctor, error)
	// SaveDoctor saves a doctor
	SaveDoctor(doctor *domain.Doctor) error
	// DeleteDoctor deletes a doctor by ID
	DeleteDoctor(id int) error

	// GetClinics returns a list of clinics
	GetClinics() ([]*domain.Facilities, error)
	// GetClinic returns a single clinic by ID
	GetClinic(id int) (*domain.Facilities, error)
	// SaveClinic saves a clinic
	SaveClinic(clinic *domain.Facilities) error
	// DeleteClinic deletes a clinic by ID
	DeleteClinic(id int) error

	// GetLanguages returns a list of languages
	GetLanguages() ([]*domain.Languages, error)
	// GetLanguage returns a single language by ID
	GetLanguage(id int) (*domain.Languages, error)
	// SaveLanguage saves a language
	SaveLanguage(language *domain.Languages) error
	// DeleteLanguage deletes a language by ID
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
