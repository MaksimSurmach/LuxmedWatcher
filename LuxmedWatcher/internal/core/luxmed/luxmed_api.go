package luxmed

import (
	"LuxmedWatcher/internal/domain"
	"context"
)

// LuxmedClient — интерфейс, описывающий методы для работы с API Luxmed.
type LuxmedClient interface {
	// Authenticate выполняет логин (с помощью логина/пароля) и сохраняет токены.
	Authenticate(ctx context.Context, creds domain.Credentials) error

	ReAuthenticate(ctx context.Context) error

	// GetAvailableAppointments возвращает список слотов по заданным параметрам.
	GetAvailableAppointments(ctx context.Context, params domain.Appointment, SearchDays int) ([]domain.Appointment, error)
}
