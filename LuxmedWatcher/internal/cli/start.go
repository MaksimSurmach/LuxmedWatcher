package cli

import (
	"context"
	"fmt"
	"os"

	"LuxmedWatcher/internal/application"
	"LuxmedWatcher/internal/config"
	"LuxmedWatcher/internal/core/luxmed"
	"LuxmedWatcher/internal/core/notification/channels"
	"LuxmedWatcher/internal/core/scheduler"
	"LuxmedWatcher/internal/core/storage"

	"github.com/spf13/cobra"
)

var configPath string

var startCmd = &cobra.Command{
	Use:   "start",
	Short: "Start the full Luxmed Watcher system",
	Long: `Запускает систему полностью:
- Загружает и валидирует конфигурацию,
- Проводит аутентификацию в Luxmed,
- Отправляет тестовое уведомление о начале поиска,
- Постановляет задачи в планировщик для периодической проверки.
`,
	Run: func(cmd *cobra.Command, args []string) {
		// Загружаем конфиг
		cfg, err := config.LoadConfig(configPath)
		if err != nil {
			fmt.Printf("Error loading config: %v\n", err)
			os.Exit(1)
		}

		// Собираем зависимости:
		// 1. Клиент Luxmed. Он передаётся в AppointmentService.
		client := luxmed.NewLuxmedClient()
		appointmentService := application.NewAppointmentService(client)

		// 2. Хранилище (например, SQLite)
		store := storage.NewSQLiteStorage("data.db")
		if err := store.Init(); err != nil {
			fmt.Printf("Error initializing storage: %v\n", err)
			os.Exit(1)
		}

		// 3. Уведомитель. Создадим, например, WebhookNotifier с дефолтными настройками.
		notifier := channels.NewWebhookNotifier(
			"https://example.com/webhook",
			"POST",
			map[string]string{"Content-Type": "application/json"},
			map[string]interface{}{"base": "value"},
		)
		notificationService := application.NewNotificationService(notifier)

		// 4. Планировщик заданий.
		sched := scheduler.NewScheduler()

		// Создаём SystemService, которому передаём все зависимости.
		systemService := application.NewSystemService(appointmentService, notificationService, sched)

		// Запускаем систему с передачей конфигурации (включая креды) и базового контекста.
		ctx := context.Background()
		if err := systemService.Start(ctx, cfg); err != nil {
			fmt.Printf("Failed to start system: %v\n", err)
			os.Exit(1)
		}
	},
}

func init() {
	rootCmd.AddCommand(startCmd)
	startCmd.PersistentFlags().StringVar(&configPath, "config", "config.yaml", "Path to configuration file")
}
