package cli

import (
	"context"
	"fmt"
	"os"

	"LuxmedWatcher/internal/application"
	"LuxmedWatcher/internal/config"
	"os/signal"
	"syscall"
	"github.com/spf13/cobra"
)

var configPath string

var startCmd = &cobra.Command{
	Use:   "start",
	Short: "Start the full Luxmed Watcher system",
	Long: `Start the full Luxmed Watcher system, including the scheduler, storage, and notification services.
This command will load the configuration from the specified file and start the system.
`,
	Run: func(cmd *cobra.Command, args []string) {
		// Загружаем конфиг
		cfg, err := config.LoadConfig(configPath)
		if err != nil {
			fmt.Printf("Error loading config: %v\n", err)
			os.Exit(1)
		}

		// Создаем системный сервис, передавая только конфиг
		systemService, err := application.NewSystemService(cfg)
		if err != nil {
			fmt.Printf("Failed to initialize system: %v\n", err)
			os.Exit(1)
		}

		// Создаем контекст с возможностью отмены
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		// Настройка обработки сигналов для graceful shutdown
		signalCh := make(chan os.Signal, 1)
		signal.Notify(signalCh, syscall.SIGINT, syscall.SIGTERM)
		go func() {
			<-signalCh
			fmt.Println("\nReceived shutdown signal, shutting down gracefully...")
			cancel()
		}()

		// Запускаем систему с контекстом
		if err := systemService.Start(ctx); err != nil {
			fmt.Printf("Failed to start system: %v\n", err)
			os.Exit(1)
		}
	},
}

func init() {
	rootCmd.AddCommand(startCmd)
	startCmd.PersistentFlags().StringVar(&configPath, "config", "config.yaml", "Path to configuration file")
}