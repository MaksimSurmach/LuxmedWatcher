package events

import (
	"sync"
)

type EventType string

// Event types
const (
	AppointmentFound EventType = "appointment_found"
)

// Event represents an event in the system
type Event struct {
	Type    EventType
	Payload interface{}
}

// EventHandler is a function that handles an event
type EventHandler func(Event)

// EventBus is a simple event bus implementation
type EventBus struct {
	subscribers map[EventType][]EventHandler
	mu          sync.RWMutex
}

// NewEventBus creates a new EventBus instance
func NewEventBus() *EventBus {
	return &EventBus{
		subscribers: make(map[EventType][]EventHandler),
	}
}

// Subscribe adds a new event handler for a specific event type
func (b *EventBus) Subscribe(eventType EventType, handler EventHandler) {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.subscribers[eventType] = append(b.subscribers[eventType], handler)
}

// Publish publishes an event to all subscribers of that event type
func (b *EventBus) Publish(event Event) {
	b.mu.RLock()
	defer b.mu.RUnlock()

	if handlers, exists := b.subscribers[event.Type]; exists {
		for _, handler := range handlers {
			go handler(event)
		}
	}
}
