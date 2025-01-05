package scheduler

import (
    "sync"
    "luxmed_checker/src/luxmed"
    "luxmed_checker/src/notifications"
)

type Scheduler struct {
    checker    *luxmed.Checker
    notifier   notifications.Notifier
    appointments []Appointment
}

type Appointment struct {
    ServiceType string
    City        string
    Doctor      string
    Time        string
}

func (s *Scheduler) CheckAppointments() {
    var wg sync.WaitGroup
    for _, appt := range s.appointments {
        wg.Add(1)
        go func(appt Appointment) {
            defer wg.Done()
            available, err := s.checker.CheckAvailability(appt)
            if err != nil {
                // Логирование ошибки
                return
            }
            if available {
                s.notifier.Send(fmt.Sprintf("Запись доступна: %v", appt))
            }
        }(appt)
    }
    wg.Wait()
}
