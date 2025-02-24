package scheduler

import (
	"fmt"
	"sync"
	"time"
)

// Scheduler описывает интерфейс планировщика,
// который может добавлять задачи, запускать их и останавливать.
type Scheduler interface {
	AddTask(interval time.Duration, task func())
	Start()
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

// NewScheduler возвращает новый объект планировщика.
func NewScheduler() Scheduler {
	return &schedulerImpl{
		tasks: []scheduledTask{},
		quit:  make(chan struct{}),
		wg:    sync.WaitGroup{},
	}
}

// AddTask регистрирует новую задачу, которая будет выполняться каждые interval.
func (s *schedulerImpl) AddTask(interval time.Duration, task func()) {
	s.tasks = append(s.tasks, scheduledTask{
		interval: interval,
		task:     task,
	})
}

// Start запускает все задачи в отдельных горутинах.
// Каждый таск крутится в цикле с time.Ticker(interval).
func (s *schedulerImpl) Start() {
	if s.started {
		fmt.Println("Scheduler already started.")
		return
	}
	s.started = true

	// Запускаем горутину для каждой задачи
	for _, t := range s.tasks {
		s.wg.Add(1)
		go s.runTask(t)
	}
}

// runTask — внутренний метод для каждой задачи.
func (s *schedulerImpl) runTask(t scheduledTask) {
	defer s.wg.Done()

	ticker := time.NewTicker(t.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			// Вызываем функцию задачи
			t.task()

		case <-s.quit:
			// Получили сигнал остановки
			return
		}
	}
}

// Stop останавливает все задачи, дожидается их корректного завершения.
func (s *schedulerImpl) Stop() {
	if !s.started {
		fmt.Println("Scheduler not started or already stopped.")
		return
	}

	close(s.quit)    // Посылаем сигнал остановки всем горутинам
	s.wg.Wait()      // Ждём, пока все горутины завершат работу
	s.started = false
}
