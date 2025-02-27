package application

import (
	"fmt"
	"LuxmedWatcher/internal/domain"
	"LuxmedWatcher/internal/core/notification"
	"context"
)

// NotificationService отвечает за отправку уведомлений.
type NotificationService interface {
	Notify(ctx context.Context, apps []domain.Appointment) error
	SendTestNotification(ctx context.Context, msg string) error
}

type notificationServiceImpl struct {
	notifiers []notification.Notifier
	// Можно добавить зависимость от Storage для логирования уведомлений.
}

func NewNotificationService(notifier notification.Notifier, /* дополнительные зависимости, например, storage.Storage */) NotificationService {
	return &notificationServiceImpl{
		notifiers: []notification.Notifier{notifier},
	}
}

func (n *notificationServiceImpl) Notify(ctx context.Context, apps []domain.Appointment) error {
	if len(apps) == 0 {
		return nil
	}
	msg := buildMessage(apps)
	for _, notifier := range n.notifiers {
		if err := notifier.Send(msg); err != nil {
			fmt.Printf("Notifier [%s] failed: %v\n", notifier.ChannelName(), err)
		}
	}
	return nil
}

func (n *notificationServiceImpl) SendTestNotification(ctx context.Context, msg string) error {
	for _, notifier := range n.notifiers {
		if err := notifier.Send(msg); err != nil {
			return err
		}
	}
	return nil
}

func buildMessage(apps []domain.Appointment) string {
	msg := "New appointments found:\n"
	for _, a := range apps {
		msg += fmt.Sprintf("- DoctorID=%d, ClinicID=%d, From=%s\n",
			a.DoctorID,
			a.ClinicID,
			a.DateTimeFrom.Format("2006-01-02 15:04"))
	}
	return msg
}
