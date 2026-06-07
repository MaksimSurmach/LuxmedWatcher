package domain

import "time"

type UserStatus string
type UserRole string
type AccountStatus string
type WatchStatus string
type DoctorMode string
type FacilityMode string
type HistoryStatus string
type NotificationStatus string

const (
	UserStatusPendingInvite UserStatus = "pending_invite"
	UserStatusActive        UserStatus = "active"
	UserStatusBlocked       UserStatus = "blocked"

	UserRoleUser  UserRole = "user"
	UserRoleAdmin UserRole = "admin"

	AccountStatusNotConfigured AccountStatus = "not_configured"
	AccountStatusActive        AccountStatus = "active"
	AccountStatusAuthFailed    AccountStatus = "auth_failed"
	AccountStatusMFARequired   AccountStatus = "mfa_required"
	AccountStatusDisabled      AccountStatus = "disabled"

	WatchStatusActive  WatchStatus = "active"
	WatchStatusPaused  WatchStatus = "paused"
	WatchStatusDeleted WatchStatus = "deleted"

	DoctorModeAny   DoctorMode = "any"
	DoctorModeExact DoctorMode = "exact"

	FacilityModeAll      FacilityMode = "all"
	FacilityModeSelected FacilityMode = "selected"

	HistoryStatusNew       HistoryStatus = "new"
	HistoryStatusSeenAgain HistoryStatus = "seen_again"
	HistoryStatusNotified  HistoryStatus = "notified"
	HistoryStatusExpired   HistoryStatus = "expired"
	HistoryStatusHidden    HistoryStatus = "hidden"

	NotificationStatusSent   NotificationStatus = "sent"
	NotificationStatusFailed NotificationStatus = "failed"
)

type User struct {
	ID             int64
	TelegramUserID int64
	TelegramChatID int64
	Username       string
	DisplayName    string
	Locale         string
	Status         UserStatus
	Role           UserRole
	CreatedAt      time.Time
	UpdatedAt      time.Time
	LastSeenAt     time.Time
}

func (u User) IsActive() bool {
	return u.Status == UserStatusActive && u.Role != ""
}

func (u User) IsAdmin() bool {
	return u.Role == UserRoleAdmin
}

type InviteCode struct {
	ID              int64
	CodeHash        string
	CreatedByUserID int64
	MaxUses         int
	UsedCount       int
	ExpiresAt       *time.Time
	DisabledAt      *time.Time
	CreatedAt       time.Time
}

type LuxMedAccount struct {
	ID                   int64
	UserID               int64
	Login                string
	EncryptedPassword    string
	EncryptedSessionData string
	SessionExpiresAt     *time.Time
	LastLoginAt          *time.Time
	LastLoginError       string
	Status               AccountStatus
}

type TimeWindow struct {
	Weekday int    `json:"weekday"`
	From    string `json:"from"`
	To      string `json:"to"`
}

type Watch struct {
	ID                   int64
	UserID               int64
	Name                 string
	Status               WatchStatus
	CityID               int
	CityName             string
	ServiceID            int
	ServiceName          string
	DoctorID             *int
	DoctorName           string
	DoctorMode           DoctorMode
	FacilityMode         FacilityMode
	FacilityIDs          []int
	FacilityNames        []string
	DateFrom             *time.Time
	DateTo               *time.Time
	NextDays             int
	TimeWindows          []TimeWindow
	CheckIntervalSeconds int
	LastCheckedAt        *time.Time
	LastSuccessAt        *time.Time
	LastError            string
	CreatedAt            time.Time
	UpdatedAt            time.Time
}

type Appointment struct {
	ExternalID   string
	Fingerprint  string
	DateTime     time.Time
	ServiceID    int
	ServiceName  string
	DoctorID     int
	DoctorName   string
	FacilityID   int
	FacilityName string
	Address      string
	CityID       int
	CityName     string
	BookingURL   string
	RawPayload   string
}

type AppointmentHistory struct {
	ID      int64
	WatchID int64
	Appointment
	FirstSeenAt    time.Time
	LastSeenAt     time.Time
	LastNotifiedAt *time.Time
	SeenCount      int
	Status         HistoryStatus
}

type NotificationHistory struct {
	ID                     int64
	WatchID                int64
	AppointmentHistoryID   int64
	AppointmentFingerprint string
	SentAt                 time.Time
	MessageID              int
	Status                 NotificationStatus
	Error                  string
}

type City struct {
	ID   int
	Name string
}

type Service struct {
	ID   int
	Name string
}

type Doctor struct {
	ID            int
	AcademicTitle string
	FirstName     string
	LastName      string
	Facilities    []Facility
}

func (d Doctor) DisplayName() string {
	name := d.FirstName + " " + d.LastName
	if d.AcademicTitle != "" {
		return d.AcademicTitle + " " + name
	}
	return name
}

type Facility struct {
	ID      int
	Name    string
	Address string
}

type DoctorsAndFacilities struct {
	Doctors    []Doctor
	Facilities []Facility
}
