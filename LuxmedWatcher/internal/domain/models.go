package domain

import "time"

// Appointment
type Appointment struct {
	ServiceName  string
	ServiceID    int
	DateTimeFrom time.Time

	DoctorID   int
	DoctorName string

	ClinicID   int
	ClinicName string
}

// AppointmentSearch parameters for search.
type AppointmentSearch struct {
	CityID           int
	ServiceVariantID int
	DoctorID         int
	PlaceID          int
	LanguageID       int
	SearchDays       int
}

type AppointmentSearchTaskRepository interface {
	Create(task *AppointmentSearchTask) error
	GetPendingTasks() ([]AppointmentSearchTask, error)
	UpdateStatus(taskID int, status string) error
	UpdateLastChecked(taskID int, time time.Time) error
	IncrementRetryCount(taskID int) error
	DeleteTask(taskID int) error
}

// Credentials — auth credentials.
type Credentials struct {
	Username string
	Password string
}

// AuthTokens — auth tokens.
type AuthTokens struct {
	AccessToken    string
	RefreshToken   string
	LXToken        string
	XsrfToken      string
	Cookies        map[string]string
	ExpirationTime time.Time
}

type NotificationLog struct {
	ID      int       `json:"id"`
	Channel string    `json:"channel"`
	Message string    `json:"message"`
	SentAt  time.Time `json:"sent_at"`
}

type AppointmentSearchTask struct {
	ID                int               `json:"id"`
	AppointmentSearch AppointmentSearch `json:"appointment_search"`
	CreatedAt         time.Time         `json:"created_at"`
	UpdatedAt         time.Time         `json:"updated_at"`
	LastCheckedAt     time.Time
	RetryCount        int
	Status            string
}
