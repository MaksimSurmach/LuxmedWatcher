package notification

import "context"

type Notifier interface {
	// Create method creates a new Notifier instance and returns it along with an error if any
	Create(interface{}) (Notifier, error)
	// Send method sends a message to the recipient and returns an error if any
	SendMessage(ctx context.Context, message string) error
	// ChannelName returns the name of the channel and an error if any
	ChannelName() string
}

type NotifierFactoryFunc func(config interface{}) (Notifier, error)
