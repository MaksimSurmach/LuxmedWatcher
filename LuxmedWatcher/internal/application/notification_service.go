package application

import (
	"LuxmedWatcher/internal/config"
	"LuxmedWatcher/internal/core/notification"
	"LuxmedWatcher/internal/domain"
	"context"
	"fmt"

	log "github.com/sirupsen/logrus"
)

// NotificationService responsible for sending notifications about available appointment slots
type NotificationService struct {
	notifiers []notification.Notifier
}

var notifierFactories = make(map[string]notification.NotifierFactoryFunc)

func NewNotificationService(notify_cfg config.NotificationsConfig) (*NotificationService, error) {
	var notifiers []notification.Notifier
	for _, cfg := range notify_cfg {
		for notifierType, conf := range cfg {
			factory, ok := notifierFactories[notifierType]
			if !ok {
				log.Error("unknown notifier type: %s", notifierType)
				return nil, fmt.Errorf("unknown notifier type: %s", notifierType)
			}
			notifier, err := factory(conf)
			if err != nil {
				log.Error("failed to create notifier: %w", err)
				return nil, fmt.Errorf("failed to create notifier: %w", err)
			}
			notifiers = append(notifiers, notifier)
		}
	}
	if len(notifiers) == 0 {
		return nil, fmt.Errorf("no notifiers configured")
	}
	return &NotificationService{notifiers: notifiers}, nil
}

func (s *NotificationService) Notify(ctx context.Context, slots []domain.Appointment) error {
	if len(slots) == 0 {
		return nil
	}

	message := formatSlotsMessage(slots)
	var errs []error
	for _, notifier := range s.notifiers {
		if err := notifier.SendMessage(ctx, message); err != nil {
			errs = append(errs, fmt.Errorf("notification via %T failed: %w", notifier, err))
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("notification errors: %v", errs)
	}
	return nil
}

func (s *NotificationService) SendTextMessage(ctx context.Context, msg string) error {
	var errs []error
	for _, notifier := range s.notifiers {
		if err := notifier.SendMessage(ctx, msg); err != nil {
			errs = append(errs, fmt.Errorf("notification via %T failed: %w", notifier, err))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("notification errors: %v", errs)
	}
	return nil
}

// formatSlotsMessage форматирует сообщение с найденными слотами.
func formatSlotsMessage(slots []domain.Appointment) string {
	message := fmt.Sprintf("Found %d available appointment slots:\n\n", len(slots))
	for i, slot := range slots {
		message += fmt.Sprintf("%d. Doctor: %s, Date: %s, Time: %s, Location: %s\n",
			i+1, slot.DoctorName, slot.DateTimeFrom, slot.DateTimeTo, slot.ClinicName)
	}
	return message
}
