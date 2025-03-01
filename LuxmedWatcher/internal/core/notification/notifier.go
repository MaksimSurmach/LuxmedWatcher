package notification

import "context"

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