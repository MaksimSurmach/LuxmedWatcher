package application

import (
	"context"
	"fmt"
	"time"

	"LuxmedWatcher/internal/config"
	"LuxmedWatcher/internal/core/scheduler"
	"LuxmedWatcher/internal/domain"
)

// SystemService инкапсулирует старт системы.
type SystemService struct {
	appointmentService  *AppointmentService
	notificationService NotificationService
	scheduler           scheduler.Scheduler
}

// NewSystemService собирает SystemService из переданных зависимостей.
func NewSystemService(appService *AppointmentService, notifService NotificationService, sched scheduler.Scheduler) *SystemService {
	return &SystemService{
		appointmentService:  appService,
		notificationService: notifService,
		scheduler:           sched,
	}
}

// Start выполняет последовательность шагов:
// 1. Аутентификация с использованием кредов из конфига,
// 2. Отправка тестового уведомления,
// 3. Постановка задач в планировщик.
func (s *SystemService) Start(ctx context.Context, cfg *config.Config) error {
	// Аутентификация: передаём креды, извлечённые из конфига.
	creds := domain.Credentials{
		Username: cfg.Credentials.Username,
		Password: cfg.Credentials.Password,
	}
	if err := s.appointmentService.Authenticate(ctx, creds); err != nil {
		return fmt.Errorf("authentication failed: %w", err)
	}
	fmt.Println("Authentication successful.")

	// Отправка тестового уведомления через NotificationService.
	testMsg := fmt.Sprintf("Test: Starting search for appointment: DoctorID=%d, CityID=%d",
		cfg.Settings.Appointments[0].DoctorID,
		cfg.Settings.Appointments[0].CityID,
	)
	if err := s.notificationService.SendTestNotification(ctx, testMsg); err != nil {
		return fmt.Errorf("test notification failed: %w", err)
	}
	fmt.Println("Test notification sent successfully.")

	// Постановка задач в планировщик для каждого набора параметров поиска.
	for _, apCfg := range cfg.Settings.Appointments {
		interval := time.Duration(cfg.Settings.CheckIntervalSec) * time.Second
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
