package notification

type Notifier interface {
	// Send method sends a message to the recipient
	Send(msg string) error
	// ChannelName returns the name of the channel
	ChannelName() string
}
