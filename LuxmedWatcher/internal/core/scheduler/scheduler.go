package scheduler

import (
	"sync"
	"time"
	"context"
	log "github.com/sirupsen/logrus"
	"LuxmedWatcher/internal/domain"
)

// Scheduler описывает интерфейс планировщика,
// который может добавлять задачи, запускать их и останавливать.
type Scheduler interface {
	AddTask(interval time.Duration, task func())
	Start(ctx context.Context)
	Stop()
}

// scheduledTask хранит параметры одной задачи (интервал и функция).
type scheduledTask struct {
	interval time.Duration
	task     func()
}

// schedulerImpl — конкретная реализация Scheduler.
type schedulerImpl struct {
	tasks   []scheduledTask
	quit    chan struct{}
	wg      sync.WaitGroup
	started bool
}

func NewScheduler() Scheduler {
	return &schedulerImpl{
		tasks: []scheduledTask{},
		quit:  make(chan struct{}),
		wg:    sync.WaitGroup{},
	}
}

func (s *schedulerImpl) AddTask(interval time.Duration, task func()) {
	s.tasks = append(s.tasks, scheduledTask{
		interval: interval,
		task:     task,
	})
}

func (s *schedulerImpl) Start(ctx context.Context) {
	if s.started {
		log.Warn("Scheduler already started")
		return
	}
	s.started = true

	// Run goroutine for each task with context to handle graceful shutdown
	for _, t := range s.tasks {
		s.wg.Add(1)
		go s.runTask(t, ctx)
		log.Infof("Task with interval %v started", t.interval)
	}
}

func (s *schedulerImpl) runTask(t scheduledTask, ctx context.Context) {
	defer s.wg.Done()

	ticker := time.NewTicker(t.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			// Run task
			t.task()

		case <-ctx.Done():
			// Context cancelled
            return

		case <-s.quit:
			// SIGTERM received
			return
		}
	}
}

func (s *schedulerImpl) Stop() {
	if !s.started {
		log.Warn("Scheduler already stopped.")
		return
	}

	close(s.quit)    // Signal all goroutines to stop
	s.wg.Wait()      // Wait for all goroutines to finish
	s.started = false
}


type TaskProcessor interface {
    ProcessTask(ctx context.Context, task domain.AppointmentSearchTask) error
}

type TaskScheduler struct {
    repo domain.AppointmentSearchTaskRepository
    processor TaskProcessor
    checkInterval time.Duration
    maxRetries int
}

func NewTaskScheduler(
    repo domain.AppointmentSearchTaskRepository, 
    processor TaskProcessor,
    checkInterval time.Duration,
    maxRetries int,
) *TaskScheduler {
    return &TaskScheduler{
        repo: repo,
        processor: processor,
        checkInterval: checkInterval,
        maxRetries: maxRetries,
    }
}

func (s *TaskScheduler) Start(ctx context.Context) error {
    ticker := time.NewTicker(s.checkInterval)
    defer ticker.Stop()
    
    for {
        select {
        case <-ctx.Done():
            return ctx.Err()
        case <-ticker.C:
            if err := s.processPendingTasks(ctx); err != nil {
                log.Printf("Error processing tasks: %v", err)
            }
        }
    }
}

func (s *TaskScheduler) processPendingTasks(ctx context.Context) error {
    tasks, err := s.repo.GetPendingTasks()
    if err != nil {
        return err
    }
    
    for _, task := range tasks {
        // Обновляем время последней проверки
        err := s.repo.UpdateLastChecked(task.ID, time.Now())
        if err != nil {
            log.Printf("Failed to update last checked time for task %d: %v", task.ID, err)
            continue
        }
        
        // Обновляем статус на "in_progress"
        err = s.repo.UpdateStatus(task.ID, "in_progress")
        if err != nil {
            log.Printf("Failed to update status for task %d: %v", task.ID, err)
            continue
        }
        
        // Обрабатываем задачу
        err = s.processor.ProcessTask(ctx, task)
        if err != nil {
            log.Printf("Failed to process task %d: %v", task.ID, err)
            
            // Увеличиваем счетчик попыток
            err = s.repo.IncrementRetryCount(task.ID)
            if err != nil {
                log.Printf("Failed to increment retry count for task %d: %v", task.ID, err)
            }
            
            // Проверяем, не превышено ли максимальное количество попыток
            if task.RetryCount >= s.maxRetries {
                err = s.repo.UpdateStatus(task.ID, "failed")
                if err != nil {
                    log.Printf("Failed to update status for task %d: %v", task.ID, err)
                }
            } else {
                err = s.repo.UpdateStatus(task.ID, "pending")
                if err != nil {
                    log.Printf("Failed to update status for task %d: %v", task.ID, err)
                }
            }
            continue
        }
        
        // Обновляем статус на "completed"
        err = s.repo.UpdateStatus(task.ID, "completed")
        if err != nil {
            log.Printf("Failed to update status for task %d: %v", task.ID, err)
        }
    }
    
    return nil
}