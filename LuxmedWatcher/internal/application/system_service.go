package application

import (
	"context"
	"fmt"
	"time"

	"LuxmedWatcher/internal/config"
	"LuxmedWatcher/internal/core/luxmed"
	"LuxmedWatcher/internal/core/scheduler"
	"LuxmedWatcher/internal/core/storage"
	"LuxmedWatcher/internal/domain"

	log "github.com/sirupsen/logrus"
)

// SystemService инкапсулирует старт системы.
type SystemService struct {
	appointmentService  *AppointmentService
	notificationService *NotificationService
	scheduler           *scheduler.TaskScheduler
	storage             storage.Storage
	config              *config.Config
}

// NewSystemService собирает SystemService из переданных зависимостей.
func NewSystemService(cfg *config.Config) (*SystemService, error) {
	client := luxmed.NewLuxmedClient()
	appointmentService := NewAppointmentService(client)

	// create sqlite storage
	storage, err := storage.NewStorage(cfg.Settings.DbProvider, cfg.Settings.DbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize storage: %w", err)
	}

	// create notifier
	notificationService, err := NewNotificationService(cfg.Notifications)
	if err != nil {
		return nil, fmt.Errorf("failed to create notification service: %w", err)
	}

	sched := scheduler.NewTaskScheduler(storage, client, time.Duration(cfg.Settings.CheckIntervalSec)*time.Second)

	return &SystemService{
		appointmentService:  appointmentService,
		notificationService: notificationService,
		scheduler:           sched,
		storage:             storage,
		config:              cfg,
	}, nil
}

func (s *SystemService) Start(ctx context.Context) error {
	// First, we need to check if we can authenticate with the Luxmed API
	creds := domain.Credentials{
		Username: s.config.Credentials.Username,
		Password: s.config.Credentials.Password,
	}
	if err := s.appointmentService.Authenticate(ctx, creds); err != nil {
		return fmt.Errorf("authentication failed: %w", err)
	}
	log.Info("Authentication successful")

	// Send initial notification to test the notification service
	// TODO: move this to a separate method and fix text
	testMsg := fmt.Sprintf("Test: Starting search for appointment: DoctorID=%d, CityID=%d",
		s.config.Appointments[0].DoctorID,
		s.config.Appointments[0].CityID,
	)
	if err := s.notificationService.SendTextMessage(ctx, testMsg); err != nil {
		return fmt.Errorf("test notification failed: %w", err)
	}
	log.Info("Start notification sent")

	// Create tasks for each appointment configuration
	for _, apCfg := range s.config.Appointments {
		interval := time.Duration(s.config.Settings.CheckIntervalSec) * time.Second
		if interval < time.Second {
			log.Warn("Check interval is too low, setting to 10 seconds")
			interval = 10 * time.Second
		}
		if interval < 5*time.Minute {
			log.Warn("Check interval is too low, you may get banned by Luxmed")
		}

		// create appointment
		id, err := s.CreateAppointment(apCfg)
		if err != nil {
			return fmt.Errorf("failed to create appointment: %w", err)
		}

		// create all notification channels
		// TODO: move this to a separate method

		// create search task
		// TODO: move this to a separate method

	s.scheduler.Start(ctx)

	<-ctx.Done()
	s.scheduler.Stop()
	return nil
}

func (s *SystemService) CreateAppointment(params domain.AppointmentRecord) (int, error) {
	if params.Name == "" {
		// TODO: add name generation
		params.Name = "Appointment"
	}
	id, err := s.storage.CreateAppointment(params)
	if err != nil {
		return nil, fmt.Errorf("failed to create appointment: %w", err)
	}
	log.Infof("Created appointment with ID %d", id)
	return id, nil
}
