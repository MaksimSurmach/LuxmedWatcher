package notification

import (
	"fmt"
	"LuxmedWatcher/internal/storage"
)

// Notifier — адаптер для конкретного канала (Telegram, Webhook, etc).
type Notifier interface {
	Send(msg string) error
	ChannelName() string // для логирования/идентификации
}

// NotificationService — основной сервис для отправки уведомлений о новых слотах.
type NotificationService struct {
	storage   storage.Storage
	notifiers []Notifier
}

// NewNotificationService — конструктор, принимает Storage и набор адаптеров.
func NewNotificationService(st storage.Storage, n ...Notifier) *NotificationService {
	return &NotificationService{
		storage:   st,
		notifiers: n,
	}
}

// Appointment alias, чтобы не дублировать структуру
type Appointment = storage.Appointment

// NotifyAppointments — принимает массив слотов, проверяет что "не было отправлено", отправляет по нужным каналам.
func (s *NotificationService) NotifyAppointments(apps []Appointment) error {
	if len(apps) == 0 {
		return nil
	}

	var toNotify []Appointment
	for _, app := range apps {
		already, err := s.storage.IsAlreadyNotified(app)
		if err != nil {
			return fmt.Errorf("check IsAlreadyNotified failed: %w", err)
		}
		if !already {
			toNotify = append(toNotify, app)
		}
	}

	if len(toNotify) == 0 {
		// ничего нового
		return nil
	}

	// Отправляем уведомление
	// Вариант 1: одна "сводка" (msg) на все слоты
	msg := s.buildMessage(toNotify)
	for _, n := range s.notifiers {
		if err := n.Send(msg); err != nil {
			// решите, что делать при ошибке — лог, return, и т.д.
			fmt.Printf("Notifier [%s] failed: %v\n", n.ChannelName(), err)
		}
	}

	// Помечаем слоты как отправленные
	err := s.storage.MarkAppointmentsNotified(toNotify)
	if err != nil {
		return fmt.Errorf("MarkAppointmentsNotified failed: %w", err)
	}

	return nil
}

// buildMessage — формирует текст уведомления на основе новых слотов
func (s *NotificationService) buildMessage(apps []Appointment) string {
	msg := "New appointments found:\n"
	for _, a := range apps {
		msg += fmt.Sprintf("- DoctorID=%d, ClinicID=%d, From=%s\n",
			a.DoctorID,
			a.ClinicID,
			a.DateTimeFrom.Format("2006-01-02 15:04"),
		)
	}
	return msg
}
