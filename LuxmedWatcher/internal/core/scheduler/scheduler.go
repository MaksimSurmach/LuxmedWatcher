package scheduler

import (
	"LuxmedWatcher/internal/core/luxmed"
	"LuxmedWatcher/internal/core/storage"
	"context"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
)

type TaskScheduler struct {
	db            storage.Storage
	lc            luxmed.LuxmedClient
	mu            sync.RWMutex
	wg            sync.WaitGroup
	checkInterval time.Duration
	quit          chan struct{}
	started       bool
}

// NewTaskScheduler creates a new task scheduler
func NewTaskScheduler(db storage.Storage, lc luxmed.LuxmedClient, checkInterval time.Duration) *TaskScheduler {
	return &TaskScheduler{
		db:            db,
		lc:            lc,
		checkInterval: checkInterval,
		quit:          make(chan struct{}),
		started:       bool(false)}
}

// Start starts the task scheduler
func (s *TaskScheduler) Start(ctx context.Context) error {
	if s.started {
		log.Warn("Scheduler already started")
		return nil
	}

	s.started = true
	s.wg.Add(1)

	// Start the main scheduling goroutine
	go func() {
		defer s.wg.Done()
		ticker := time.NewTicker(s.checkInterval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				log.Info("Context cancelled, stopping scheduler")
				return

			case <-ticker.C:
				if err := s.processPendingTasks(ctx); err != nil {
					log.Printf("Error processing tasks: %v", err)
				}

			case <-s.quit:
				log.Info("Quit signal received, stopping scheduler")
				return
			}
		}
	}()
	return nil
}

// Stop stops the task scheduler
func (s *TaskScheduler) Stop() {
	if !s.started {
		log.Warn("Scheduler not running")
		return
	}

	close(s.quit)
	s.wg.Wait()
	s.started = false
	log.Info("Scheduler stopped")
}

// processPendingTasks get all active tasks from the database and processes them
func (s *TaskScheduler) processPendingTasks(ctx context.Context) error {
	// Get all active tasks
	tasks, err := s.db.GetActiveAppointmentSearchTasks()
	if err != nil {
		return err
	}

	for _, task := range tasks {
		// Prepare appointment
		appointment, err := s.db.GetAppointmentRecord(task.AppointmentID)
		if err != nil {
			log.Printf("Failed to get appointment id:%d - %v", task.AppointmentID, err)
			continue
		}
		// Process task
		search_result, err := s.lc.GetAvailableAppointments(ctx, appointment, task.SearchDays)
		if err != nil {
			log.Printf("Failed to process task %d: %v", task.ID, err)
			continue
		}

		// Update last checked time
		if err := s.db.UpdateLastCheckedTask(*task.ID, time.Now()); err != nil {
			log.Printf("Failed to update last checked time for task %d: %v", task.ID, err)
		}

		// if search_result is not empty, save the results
		if len(search_result) > 0 {
			log.Infof("Found %d available appointments for task %d", len(search_result), task.ID)

			for _, res := range search_result {
				// Save appointment search result
				if err := s.db.SaveAppointmentNotification(res); err != nil {
					log.Printf("Failed to save appointment search result: %v", err)
				}
			}
		} else {
			log.Infof("No available appointments found for task %d", *task.ID)
		}
	}

	return nil
}

// SetTaskInterval sets a custom check interval for a specific task
func (s *TaskScheduler) SetTaskInterval(interval time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.checkInterval = interval
	log.Infof("Check interval set to %v", interval)
}
