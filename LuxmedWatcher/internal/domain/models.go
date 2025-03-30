package domain

import "time"

// Appointment — represents an appointment.
type Appointment struct {
	ServiceName  string
	ServiceID    int
	DateTimeFrom time.Time

	DoctorID   int
	DoctorName string

	ClinicID   int
	ClinicName string

	CityID int

	LanguageID int
	PlaceID    int
}

// Appointment
type AppointmentRecord struct {
	ID               *int      `json:"id,omitempty" db:"id"`
	Name             string    `json:"name" db:"name"`
	CityID           int       `json:"city_id" db:"city_id"`
	ServiceVariantID int       `json:"service_variant_id" db:"service_variant_id"`
	DoctorID         int       `json:"doctor_id" db:"doctor_id"`
	ClinicID         int       `json:"place_id" db:"clinic_id"`
	LanguageID       int       `json:"language_id" db:"language_id"`
	CreatedAt        time.Time `json:"created_at" db:"created_at"`
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

// NotificationChannels — represents a list of notification channels.
type NotificationChannels struct {
	ID          *int                   `json:"id,omitempty" db:"id"`
	Name        string                 `json:"name" db:"name"`
	ChannelType string                 `json:"channel_type" db:"channel_type"`
	Config      map[string]interface{} `json:"config" db:"config"`
}

// AppointmentSearch — represents a search for an appointment.
type AppointmentSearchTask struct {
	ID                    *int      `json:"id,omitempty" db:"id"`
	AppointmentID         int       `json:"appointment_id" db:"appointment_id"`
	NotificationChannelID int       `json:"notification_channel_id" db:"notification_channel_id"`
	SearchDays            int       `json:"search_days" db:"search_days"`
	CreatedAt             time.Time `json:"created_at" db:"created_at"`
	LastCheckedAt         time.Time `json:"last_checked_at" db:"last_checked_at"`
	Status                string    `json:"status" db:"status"`
	RetryCount            int       `json:"retry_count" db:"retry_count"`
	IsActive              bool      `json:"is_active" db:"is_active"`
}
