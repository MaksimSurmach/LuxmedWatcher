package application

import (
	"context"
	"fmt"
	"time"

	"LuxmedWatcher/internal/config"
	"LuxmedWatcher/internal/core/scheduler"
	"LuxmedWatcher/internal/domain"
	"LuxmedWatcher/internal/core/storage"
	"LuxmedWatcher/internal/core/luxmed"
)

// SystemService инкапсулирует старт системы.
type SystemService struct {
	appointmentService  *AppointmentService
	notificationService *NotificationService
	scheduler           scheduler.Scheduler
	store               storage.Storage
	config              *config.Config
}

// NewSystemService собирает SystemService из переданных зависимостей.
func NewSystemService(cfg *config.Config) (*SystemService, error) {
	client := luxmed.NewLuxmedClient()
	appointmentService := NewAppointmentService(client)

	// create sqlite storage
	store := storage.NewSQLiteStorage(cfg.Settings.DbPath)
	if err := store.Init(); err != nil {
		return nil, fmt.Errorf("failed to initialize storage: %w", err)
	}

	// create notifier
	notificationService, err := NewNotificationService(cfg.Notifications)
	if err != nil {
		return nil, fmt.Errorf("failed to create notification service: %w", err)
	}

	sched := scheduler.NewScheduler()

	return &SystemService{
		appointmentService:  appointmentService,
		notificationService: notificationService,
		scheduler:           sched,
		store:               store,
		config:              cfg,
	}, nil
}


func (s *SystemService) Start(ctx context.Context) error {
	// Аутентификация: передаём креды, извлечённые из конфига.
	creds := domain.Credentials{
		Username: s.config.Credentials.Username,
		Password: s.config.Credentials.Password,
	}
	if err := s.appointmentService.Authenticate(ctx, creds); err != nil {
		return fmt.Errorf("authentication failed: %w", err)
	}
	fmt.Println("Authentication successful.")

	// Отправка тестового уведомления через NotificationService.
	testMsg := fmt.Sprintf("Test: Starting search for appointment: DoctorID=%d, CityID=%d",
		s.config.Appointments[0].DoctorID,
		s.config.Appointments[0].CityID,
	)
	if err := s.notificationService.SendTextMessage(ctx, testMsg); err != nil {
		return fmt.Errorf("test notification failed: %w", err)
	}
	fmt.Println("Test notification sent successfully.")

	// Постановка задач в планировщик для каждого набора параметров поиска.
	for _, apCfg := range s.config.Appointments {
		interval := time.Duration(s.config.Settings.CheckIntervalSec) * time.Second
		if interval < time.Second {
			interval = 60 * time.Second
		}
		params := domain.AppointmentSearch{
			DoctorID:         apCfg.DoctorID,
			CityID:           apCfg.CityID,
			PlaceID:          apCfg.Location,
			LanguageID:       10,
			ServiceVariantID: apCfg.ServiceVariantID,
			SearchDays:       14,
		}
		p := params
		s.scheduler.AddTask(interval, func() {
			slots, err := s.appointmentService.CheckAppointments(ctx, p)
			fmt.Printf("[Scheduler] Checking appointments for doctorID=%d, cityID=%d\n", p.DoctorID, p.CityID)
			if err != nil {
				fmt.Printf("[Scheduler] Error: %v\n", err)
				return
			}
			if len(slots) > 0 {
				if err := s.notificationService.Notify(ctx, slots); err != nil {
					fmt.Printf("[Scheduler] Notification error: %v\n", err)
				}
			} else {
				fmt.Printf("[Scheduler] No new slots found for doctorID=%d, cityID=%d\n", p.DoctorID, p.CityID)
			}
		})
	}
	s.scheduler.Start()
	// Здесь можно добавить graceful shutdown через обработку сигналов.
	select {} // Блокировка работы сервиса
}
