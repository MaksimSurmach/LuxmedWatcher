package luxmed

import (
	"LuxmedWatcher/internal/domain"
	"context"
)

// LuxmedClient — Luxmed API client interface.
type LuxmedClient interface {
	// Authenticate provides a way to authenticate with the Luxmed API.
	Authenticate(ctx context.Context, creds domain.Credentials) error

	ReAuthenticate(ctx context.Context) error

	// GetAvailableAppointments makes a request to the Luxmed API to get available appointments.
	GetAvailableAppointments(ctx context.Context, params *domain.AppointmentRecord, SearchDays int) ([]*domain.AppointmentSearchResult, error)
}
