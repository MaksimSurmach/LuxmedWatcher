package storage

import "time"

// Appointment — можно вынести в общий пакет, но допустим лежит здесь
type Appointment struct {
	ID          int
	DateTimeFrom time.Time
	DateTimeTo   time.Time
	DoctorID    int
	ClinicID    int
	// Добавьте поля (DoctorName, ClinicName) если хотите хранить
}

// Config — если хотите хранить конфиг в базе, упрощённо
type Config struct {
	ID      int    // primary key
	Content string // JSON/YAML или иной формат — на ваше усмотрение
}

// Storage — интерфейс для работы с базой.
type Storage interface {
	Init() error
	Close() error

	// Config
	LoadConfig() (*Config, error)
	SaveConfig(cfg *Config) error

	// Appointment notifications
	IsAlreadyNotified(app Appointment) (bool, error)
	MarkAppointmentsNotified([]Appointment) error
}
