package application

import (
	"LuxmedWatcher/internal/config"
	"LuxmedWatcher/internal/core/notification"

	// "LuxmedWatcher/internal/core/notification/channels"

	"context"
	"fmt"
	"strings"

	log "github.com/sirupsen/logrus"

	_ "LuxmedWatcher/internal/core/notification/channels"
)

// NotificationService responsible for sending notifications about available appointment slots
type NotificationService struct {
	notifiers []notification.Notifier
}

func NewNotificationService(notify_cfg config.NotificationsConfig) (*NotificationService, error) {
	var notifiers []notification.Notifier

	for _, cfg := range notify_cfg {
		for notifierType, conf := range cfg {
			// Make channel name lowercase for case-insensitive matching
			channelName := strings.ToLower(notifierType)

			// Check if this channel type exists
			factory, exists := notification.GetNotifierFactory(channelName)
			if !exists {
				available := notification.GetAvailableNotifiers()
				log.Errorf("Unknown notifier type: %s. Available types: %v", notifierType, available)
				return nil, fmt.Errorf("unknown notifier type: %s", notifierType)
			}

			// Create notifier instance
			notifier, err := factory(conf)
			if err != nil {
				log.Errorf("Failed to create notifier: %v", err)
				return nil, fmt.Errorf("failed to create notifier: %v", err)
			}

			notifiers = append(notifiers, notifier)
			log.Infof("Registered notifier: %s", notifier.ChannelName())
		}
	}
	if len(notifiers) == 0 {
		return nil, fmt.Errorf("no notifiers configured")
	}
	return &NotificationService{notifiers: notifiers}, nil
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
