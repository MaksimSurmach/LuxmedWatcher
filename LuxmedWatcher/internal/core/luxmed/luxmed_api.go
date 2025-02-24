package luxmed

import (
	"context"
)

// LuxmedClient — интерфейс, описывающий методы для работы с API Luxmed.
type LuxmedClient interface {
	// Authenticate выполняет логин (с помощью логина/пароля) и сохраняет токены.
	Authenticate(ctx context.Context, creds Credentials) error

	// RefreshTokenIfNeeded может переавторизоваться, если AccessToken истёк.
	// Можно сделать логику проверки expiration или 401-ответа от Luxmed.
	RefreshTokenIfNeeded(ctx context.Context) error

	// IsAuthenticated возвращает true, если есть валидные токены и куки.
	IsAuthenticated() bool

	// GetAvailableAppointments возвращает список слотов по заданным параметрам.
	GetAvailableAppointments(ctx context.Context, params AppointmentSearch) ([]Appointment, error)
}
