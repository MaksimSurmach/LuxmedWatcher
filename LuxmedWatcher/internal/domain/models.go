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
	CityID            int
	CityName          string // optional
	ServiceVariantID  int
	DoctorID          int
	PlaceID           int
	PlaceName         string
	LanguageID        int
	ReferralID        int
	ReferralTypeID    int
	ProcessID         string
	SearchDays        int
	CreationTimestamp time.Time
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
	Channel string    `json:"channel"` // какой канал использовался
	Message string    `json:"message"` // текст уведомления
	SentAt  time.Time `json:"sent_at"` // время отправки
}

// AppointmentSearchTask – задание для поиска свободных слотов.
type AppointmentSearchTask struct {
	ID                int               `json:"id"`
	AppointmentSearch AppointmentSearch `json:"appointment_search"`
	CreatedAt         time.Time         `json:"created_at"`
	UpdatedAt         time.Time         `json:"updated_at"`
}
