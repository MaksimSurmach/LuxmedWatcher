package notification

import (
	"LuxmedWatcher/internal/core/events"
	"LuxmedWatcher/internal/core/storage"
	"LuxmedWatcher/internal/domain"
	"context"
	"time"

	log "github.com/sirupsen/logrus"
)

// NotificationWorker is a worker that processes notifications
type NotificationWorker struct {
	storage   storage.Storage
	notifiers []Notifier
	interval  time.Duration
	quit      chan struct{}
	started   bool
	eventBus  *events.EventBus
}

// NewNotificationWorker creates a new NotificationWorker
func NewNotificationWorker(storage storage.Storage, interval time.Duration, eventBus *events.EventBus) *NotificationWorker {
	return &NotificationWorker{
		storage:   storage,
		notifiers: make([]Notifier, 0),
		interval:  interval,
		quit:      make(chan struct{}),
		started:   false,
		eventBus:  eventBus,
	}
}

// RegisterNotifier registers a notifier
func (w *NotificationWorker) RegisterNotifier(notifier Notifier) {
	w.notifiers = append(w.notifiers, notifier)
	log.Infof("Registered notifier: %s", notifier.ChannelName())
}

// Start run the notification worker
func (w *NotificationWorker) Start(ctx context.Context) {
	if w.started {
		log.Warn("Notification worker already started")
		return
	}

	w.started = true
	log.Info("Starting notification worker")

	// create a channel to events
	eventChan := make(chan events.Event)

	// Subscribe to events
	w.eventBus.Subscribe(events.AppointmentFound, func(event events.Event) {
		select {
		case eventChan <- event:
		case <-ctx.Done():
		}
	})

	go func() {
		ticker := time.NewTicker(w.interval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				log.Info("Context canceled, stopping notification worker")
				return
			case <-w.quit:
				log.Info("Quit signal received, stopping notification worker")
				return
			case <-ticker.C:
				if err := w.processNotifications(ctx); err != nil {
					log.Errorf("Error processing notifications: %v", err)
				}
				// TODO: add a daily db cleanup
			case event := <-eventChan:
				// handle event
				log.Infof("Received event: %s", event.Type)
				if err := w.processNotifications(ctx); err != nil {
					log.Errorf("Error processing notifications: %v", err)
				}
			}
		}
	}()
}

// Stop worker
func (w *NotificationWorker) Stop() {
	if !w.started {
		return
	}

	close(w.quit)
	w.started = false
	log.Info("Notification worker stopped")
}

// processNotifications handles the processing of notifications
func (w *NotificationWorker) processNotifications(ctx context.Context) error {
	// get all pending notifications
	pending_notifications, err := w.storage.GetPendingNotifications()
	if err != nil {
		return err
	}

	if len(pending_notifications) == 0 {
		return nil
	}

	log.Infof("Processing %d pending notifications", len(pending_notifications))

	notifications := make(map[int][]*domain.AppointmentSearchResult)
	for _, notification := range pending_notifications {
		// get notification by ID
		notifications[notification.TaskID] = append(notifications[notification.TaskID], notification)
	}

	// process each notification
	for taskID, slots := range notifications {

		message := formatSlotsMessage(slots)

		log.Infof("Found %d available appointment slots for task %d", len(slots), taskID)
		for _, slot := range slots {
			// update status to processing
			w.storage.SetNotificationStatus(slot.ID, "processing")

			// call all notifiers
			for _, notifier := range w.notifiers {
				if err := notifier.SendMessage(ctx, message); err != nil {
					log.Errorf("Failed to send notification via %s: %v", notifier.ChannelName(), err)
					continue
				}

				log.Infof("Notification %d sent via %s", slot.ID, notifier.ChannelName())
			}
			// update status to completed
			w.storage.SetNotificationStatus(slot.ID, "completed")
		}
	}

	return nil
}
