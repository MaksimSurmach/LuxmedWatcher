package storage

import (
	"LuxmedWatcher/internal/config"
	"LuxmedWatcher/internal/domain"
	"fmt"
)

// Storage определяет абстракцию для работы с базой данных.
type Storage interface {
	// Migrate() error // запуск миграций для создания/обновления схемы БД
	Close() error

	// Управление конфигурацией
	// Получаем и сохраняем распарсенный конфиг, который является полноценной сущностью.
	GetConfig() (*config.Config, error)
	SaveConfig(cfg *config.Config) error

	// Уведомления по записям
	// Проверяем, было ли уведомление для данного слота отправлено,
	// и сохраняем факт отправки.
	IsAppointmentNotified(app domain.Appointment) (bool, error)
	MarkAppointmentsNotified(apps []domain.Appointment) error

	// // Дополнительно: история уведомлений
	// // Позволяет хранить логи отправленных уведомлений для аудита.
	// LogNotification(log domain.NotificationLog) error
	// GetNotificationHistory() ([]domain.NotificationLog, error)

	// // Дополнительно: управление задачами поиска
	// // Позволяет динамически добавлять/удалять задания, которые ищут свободные слоты.
	// GetAppointmentSearchTasks() ([]domain.AppointmentSearchTask, error)
	// SaveAppointmentSearchTask(task domain.AppointmentSearchTask) error
	// DeleteAppointmentSearchTask(taskID int) error
}

// NewStorage creates a new storage instance based on the provided storage type.
func NewStorage(storageType string, dbPath string) (Storage, error) {
	switch storageType {
	case "sqlite":
		sqllite, err := NewSQLiteStorage(dbPath)
		if err != nil {
			return nil, err
		}
		return sqllite, nil
	default:
		panic(fmt.Sprintf("unsupported storage type: %s", storageType))
	}
}
