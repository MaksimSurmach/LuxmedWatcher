package app

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/maksimsurmach/luxmed-watcher/internal/bot"
	"github.com/maksimsurmach/luxmed-watcher/internal/i18n"
	"github.com/maksimsurmach/luxmed-watcher/internal/observability"
	"github.com/maksimsurmach/luxmed-watcher/internal/scheduler"
	"github.com/maksimsurmach/luxmed-watcher/internal/security"
	"github.com/maksimsurmach/luxmed-watcher/internal/storage"
)

type App struct {
	cfg    Config
	logger *slog.Logger
}

func New(cfg Config, logger *slog.Logger) *App {
	return &App{cfg: cfg, logger: logger}
}

func (a *App) Run(ctx context.Context) error {
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	db, err := storage.Open(ctx, a.cfg.DatabasePath)
	if err != nil {
		return err
	}
	defer db.Close()

	catalog, err := i18n.Load(a.cfg.DefaultLocale)
	if err != nil {
		return err
	}
	cryptor, err := security.NewCryptor(a.cfg.MasterKey)
	if err != nil {
		return err
	}
	api, err := tgbotapi.NewBotAPI(a.cfg.TelegramToken)
	if err != nil {
		return err
	}

	tgBot := bot.New(api, db, catalog, cryptor, bot.Config{
		DefaultLocale:    a.cfg.DefaultLocale,
		AdminTelegramIDs: a.cfg.AdminTelegramIDs,
		MinimumInterval:  a.cfg.MinimumInterval,
		PollTimeout:      a.cfg.PollTimeout,
	}, a.logger)
	runner := scheduler.NewRunner(db, cryptor, tgBot, a.cfg.NotificationCooldown, a.cfg.EnableRawLuxMedPayload, a.logger)
	tgBot.SetRunner(runner)
	watcher := scheduler.New(db, runner, 15*time.Second, a.logger)

	go observability.RunHealth(ctx, a.cfg.HealthAddr, a.logger)
	go watcher.Run(ctx)

	a.logger.Info("luxmed watcher started", "bot", api.Self.UserName, "db", a.cfg.DatabasePath)
	return tgBot.Run(ctx)
}
