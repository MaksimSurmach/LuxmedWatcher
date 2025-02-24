package cli

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"LuxmedWatcher/internal/config"
	"LuxmedWatcher/internal/core/luxmed"
	"LuxmedWatcher/internal/core/scheduler"
)

// RunCLI — главный вход в логику CLI.
// Вызывается из main.go.
func RunCLI() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	switch os.Args[1] {
	case "generate-config":
		generateConfigCommand()
	case "start":
		startCommand()
	case "now":
		checkNowCommand()
	case "test":
		testCommand()
	default:
		printUsage()
		os.Exit(1)
	}
}

// printUsage выводит справку по доступным командам.
func printUsage() {
	fmt.Println("Usage:")
	fmt.Println("  luxmed-watcher generate-config   # Создать пример config.yaml")
	fmt.Println("  luxmed-watcher start             # Запуск сервиса (планировщика)")
	fmt.Println("  luxmed-watcher check-now         # Однократная проверка слотов")
}

// Команда: generate-config
func generateConfigCommand() {
	// Для удобства можно создать флаги (если нужно), но пока обойдёмся без них.
	fs := flag.NewFlagSet("generate-config", flag.ExitOnError)
	fs.Parse(os.Args[2:]) // парсим дополнительные аргументы, если есть

	err := config.GenerateSampleConfigFile("config.yaml")
	if err != nil {
		fmt.Printf("Error generating config: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("Sample config.yaml generated successfully.")
}

// Команда: start
func startCommand() {
	fs := flag.NewFlagSet("start", flag.ExitOnError)
	configPath := fs.String("config", "config.yaml", "Path to config file")
	fs.Parse(os.Args[2:])

	// Загружаем конфиг
	cfg, err := config.LoadConfig(*configPath)
	if err != nil {
		fmt.Printf("Error loading config: %v\n", err)
		os.Exit(1)
	}

	// Создаём клиента Luxmed (наш core/luxmed)
	client := luxmed.NewLuxmedClient()

	// Авторизация
	ctx := context.Background()
	if err := client.Authenticate(ctx, luxmed.Credentials{
		Username: cfg.Credentials.Username,
		Password: cfg.Credentials.Password,
	}); err != nil {
		fmt.Printf("Error authenticating: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("Successfully authenticated in Luxmed.")

	// Создаём планировщик
	sched := scheduler.NewScheduler()

	// Допустим, у нас в конфиге есть список appointments для мониторинга:
	for _, apCfg := range cfg.Settings.Appointments {
		// Пример интервала (по умолчанию 60s или берём из config)
		interval := time.Duration(cfg.Settings.CheckIntervalSec) * time.Second
		if interval < time.Second {
			interval = 60 * time.Second
		}

		params := luxmed.AppointmentSearch{
			DoctorID:         apCfg.DoctorID,
			CityID:           apCfg.CityID,
			PlaceID:          apCfg.Location,
			LanguageID:       10,
			ServiceVariantID: apCfg.ServiceVariantID,
		}

		sched.AddTask(interval, func() {
			checkAndNotify(ctx, client, params)
		})
	}

	fmt.Println("Scheduler started. Press Ctrl+C to stop (placeholder).")
	sched.Start()
	select {}
}

func checkNowCommand() {
	fs := flag.NewFlagSet("check-now", flag.ExitOnError)
	configPath := fs.String("config", "config.yaml", "Path to config file")
	fs.Parse(os.Args[2:])

	cfg, err := config.LoadConfig(*configPath)
	if err != nil {
		fmt.Printf("Error loading config: %v\n", err)
		os.Exit(1)
	}

	client := luxmed.NewLuxmedClient()

	ctx := context.Background()
	if err := client.Authenticate(ctx, luxmed.Credentials{
		Username: cfg.Credentials.Username,
		Password: cfg.Credentials.Password,
	}); err != nil {
		fmt.Printf("Error authenticating: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("Successfully authenticated in Luxmed.")

	if len(cfg.Settings.Appointments) == 0 {
		fmt.Println("No appointments in config. Exiting.")
		os.Exit(0)
	}

	// Проверяем только первый Appointment (или все по очереди).
	for _, apCfg := range cfg.Settings.Appointments {
		params := luxmed.AppointmentSearch{
			DoctorID:         apCfg.DoctorID,
			CityID:           apCfg.CityID,
			PlaceID:          apCfg.Location,
			LanguageID:       10,
			ServiceVariantID: apCfg.ServiceVariantID,
		}
		slots, err := client.GetAvailableAppointments(ctx, params)
		if err != nil {
			fmt.Printf("Error getting appointments: %v\n", err)
			continue
		}
		fmt.Printf("Found %d slots for cityID=%d, doctorID=%d\n", len(slots), apCfg.CityID, apCfg.DoctorID)
		for _, s := range slots {
			fmt.Printf("  - %s, Doctor: %s, Clinic: %s\n", s.DateTimeFrom.Format("2006-01-02 15:04"), s.DoctorName, s.ClinicName)
		}
	}
}

// checkAndNotify — упрощённая функция.
// Здесь мы делаем запрос в Luxmed и (условно) выводим в консоль или вызывали бы реальное уведомление.
func checkAndNotify(ctx context.Context, client luxmed.LuxmedClient, params luxmed.AppointmentSearch) {
	slots, err := client.GetAvailableAppointments(ctx, params)
	fmt.Fprintf(os.Stdout, "[Scheduler] Checking appointments for doctorID=%d, cityID=%d\n", params.DoctorID, params.CityID)
	if err != nil {
		fmt.Printf("[Scheduler] Error checking appointments: %v\n", err)
		return
	}

	if len(slots) > 0 {
		fmt.Printf("[Scheduler] Found %d slots for doctorID=%d at cityID=%d\n",
			len(slots), params.DoctorID, params.CityID)
		// TODO: вызвать core/notification.SendNotification(...) или что-то подобное
	} else {
		fmt.Printf("[Scheduler] No new slots found for doctorID=%d, cityID=%d\n", params.DoctorID, params.CityID)
	}
}

func testCommand() {
	fmt.Println("Test command")
}
