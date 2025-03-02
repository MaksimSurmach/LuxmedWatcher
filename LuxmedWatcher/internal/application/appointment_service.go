package application

import (
	"context"
	"LuxmedWatcher/internal/core/luxmed"
	"LuxmedWatcher/internal/domain"
)

type AppointmentService struct {
	client luxmed.LuxmedClient
}

func NewAppointmentService(client luxmed.LuxmedClient) *AppointmentService {
	return &AppointmentService{client: client}
}

func (s *AppointmentService) Authenticate(ctx context.Context, creds domain.Credentials) error {
	return s.client.Authenticate(ctx, creds)
}

// CheckAppointments checks for available appointments
func (s *AppointmentService) CheckAppointments(ctx context.Context, params domain.AppointmentSearch) ([]domain.Appointment, error) {
	return s.client.GetAvailableAppointments(ctx, params)
}
