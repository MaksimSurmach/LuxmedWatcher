package app

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/maksimsurmach/luxmed-watcher/internal/bot"
	"github.com/maksimsurmach/luxmed-watcher/internal/i18n"
	"github.com/maksimsurmach/luxmed-watcher/internal/luxmed"
	"github.com/maksimsurmach/luxmed-watcher/internal/miniapp"
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

	var db *storage.DB
	var err error
	backend := "sqlite"
	if a.cfg.DatabaseURL != "" {
		backend = "postgres"
		db, err = storage.OpenPostgres(ctx, a.cfg.DatabaseURL)
	} else {
		db, err = storage.Open(ctx, a.cfg.DatabasePath)
	}
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
	httpClient := &http.Client{Timeout: time.Duration(a.cfg.PollTimeout+10) * time.Second}
	api, err := tgbotapi.NewBotAPIWithClient(a.cfg.TelegramToken, tgbotapi.APIEndpoint, telegramHTTP{
		ctx: ctx, client: httpClient,
	})
	if err != nil {
		return errors.New("connect to Telegram Bot API: check the token and network")
	}

	var browsers map[int64]*luxmed.Browser
	routes := a.cfg.BrowserRoutes
	if routes == nil && a.cfg.BrowserAPIURL != "" {
		routes = make(map[int64]luxmed.BrowserRoute)
		for id := range a.cfg.AdminTelegramIDs {
			routes[id] = luxmed.BrowserRoute{API: a.cfg.BrowserAPIURL}
		}
	}
	if routes != nil {
		browsers = make(map[int64]*luxmed.Browser, len(routes))
		for id, route := range routes {
			browser, err := luxmed.NewBrowser(route.API, a.cfg.BrowserViewerURL)
			if err != nil {
				return err
			}
			browsers[id] = browser
		}
	}
	var miniApp *luxmed.MiniApp
	if a.cfg.MiniAppURL != "" {
		miniApp, err = luxmed.NewMiniApp(a.cfg.MiniAppAgentURL, a.cfg.MiniAppURL, a.cfg.MiniAppTicketKey)
		if err != nil {
			return err
		}
	}
	tgBot := bot.New(api, db, catalog, cryptor, bot.Config{
		DefaultLocale:    a.cfg.DefaultLocale,
		AdminTelegramIDs: a.cfg.AdminTelegramIDs,
		MinimumInterval:  a.cfg.MinimumInterval,
		DefaultInterval:  a.cfg.DefaultInterval,
		PollTimeout:      a.cfg.PollTimeout,
		Browsers:         browsers,
		MiniApp:          miniApp,
	}, a.logger)
	runner := scheduler.NewRunner(db, cryptor, tgBot, a.cfg.MinimumInterval, a.cfg.NotificationCooldown, a.cfg.EnableRawLuxMedPayload)
	runner.UseBrowsers(browsers)
	tgBot.SetRunner(runner)
	watcher := scheduler.New(db, runner, 15*time.Second, a.cfg.HistoryRetention, a.logger)

	var dashboard http.Handler
	if miniApp != nil {
		dashboard = miniapp.Dashboard(miniapp.DashboardConfig{
			Origin: miniApp.Origin(), BotID: api.Self.ID,
			MinimumInterval: a.cfg.MinimumInterval, DefaultInterval: a.cfg.DefaultInterval,
		}, db, browsers, miniApp)
	}
	var workers sync.WaitGroup
	workers.Add(2)
	go func() {
		defer workers.Done()
		observability.RunHealth(ctx, a.cfg.HealthAddr, db.Ping, dashboard, a.logger)
	}()
	go func() {
		defer workers.Done()
		watcher.Run(ctx)
	}()

	a.logger.Info("luxmed watcher started", "bot", api.Self.UserName, "db", backend)
	err = tgBot.Run(ctx)
	stop()
	workers.Wait()
	return err
}

type telegramHTTP struct {
	ctx    context.Context
	client *http.Client
}

func (c telegramHTTP) Do(req *http.Request) (*http.Response, error) {
	return c.client.Do(req.WithContext(c.ctx))
}
