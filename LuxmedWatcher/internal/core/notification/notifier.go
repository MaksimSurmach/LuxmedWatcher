package notification

import (
	"LuxmedWatcher/internal/domain"
	"context"
	"fmt"
)

type Notifier interface {
	// Send method sends a message to the recipient and returns an error if any
	SendMessage(ctx context.Context, message string) error
	// ChannelName returns the name of the channel and an error if any
	ChannelName() string
}

type NotifierFactoryFunc func(config interface{}) (Notifier, error)

var notifierFactories = make(map[string]NotifierFactoryFunc)

func RegisterNotifier(name string, factory NotifierFactoryFunc) {
	notifierFactories[name] = factory
}

func GetNotifierFactories() map[string]NotifierFactoryFunc {
	return notifierFactories
}

// GetAvailableNotifiers returns a list of all registered notifier names
func GetAvailableNotifiers() []string {
	names := make([]string, 0, len(notifierFactories))
	for name := range notifierFactories {
		names = append(names, name)
	}
	return names
}

// GetNotifierFactory returns the factory function for a given notifier name
func GetNotifierFactory(name string) (NotifierFactoryFunc, bool) {
	factory, exists := notifierFactories[name]
	return factory, exists
}

// formatSlotsMessage форматирует сообщение с найденными слотами.
func formatSlotsMessage(slots []*domain.AppointmentSearchResult) string {
	message := fmt.Sprintf("Found %d available appointment slots:\n\n", len(slots))
	for i, slot := range slots {
		message += fmt.Sprintf("Usługa: %s\n", slot.ServiceName)
		message += fmt.Sprintf("Lekarz: %s\n", slot.DoctorName)
		message += fmt.Sprintf("Data: %s\n", slot.DateTimeFrom)
		message += fmt.Sprintf("Miejsce: %s\n", slot.ClinicName)
		if i < len(slots)-1 {
			message += "\n"
		}
		if len(message) > 4000 {
			message += fmt.Sprintf("\n...and %d more", len(slots)-i-1)
			break
		}
	}
	return message
}
