package storage

import (
	"LuxmedWatcher/internal/config"
	"LuxmedWatcher/internal/domain"
)

// Storage определяет абстракцию для работы с базой данных.
type Storage interface {
	// Методы жизненного цикла базы
	Init() error
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


// NewStorage создаёт новое хранилище с указанным типом.
func NewStorage(storageType string, dbPath string) Storage {
	switch storageType {
	case "sqlite":
		return NewSQLiteStorage(dbPath)
	default:
		return nil
	}
}