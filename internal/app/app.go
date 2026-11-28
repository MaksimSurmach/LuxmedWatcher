package app

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
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
	if err := a.configureReconnectJournal(db); err != nil {
		return err
	}

	catalog, err := i18n.Load(a.cfg.DefaultLocale)
	if err != nil {
		return err
	}
	cryptor, err := security.NewCryptor(a.cfg.MasterKey)
	if err != nil {
		return err
	}
	if err := db.EncryptLegacyLogins(ctx, cryptor); err != nil {
		return fmt.Errorf("encrypt legacy LuxMed credentials: %w", err)
	}
	httpClient := &http.Client{
		Timeout: time.Duration(a.cfg.PollTimeout+10) * time.Second,
		// Keep delivery tracing scoped to a single Telegram request.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
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
			browser, err := luxmed.NewRoutedBrowser(id, route, a.cfg.BrowserViewerURL, a.cfg.BrowserWorkerKey)
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
		MiniAppLink:      a.cfg.MiniAppLink,
		MenuClient:       telegramHTTP{ctx: ctx, client: &http.Client{Timeout: 3 * time.Second}},
	}, a.logger)
	runner := scheduler.NewRunner(db, cryptor, tgBot, a.cfg.MinimumInterval, a.cfg.NotificationCooldown, a.cfg.EnableRawLuxMedPayload)
	runner.UseBrowsers(browsers)
	tgBot.SetRunner(runner)
	watcher := scheduler.New(db, runner, 15*time.Second, a.cfg.HistoryRetention, a.logger)

	dashboardCfg := miniapp.DashboardConfig{
		MinimumInterval: a.cfg.MinimumInterval,
		DefaultInterval: a.cfg.DefaultInterval,
		SyncAppMenu:     tgBot.SyncAppMenu,
	}
	var dashboard http.Handler
	if miniApp != nil {
		dashboardCfg.Origin, dashboardCfg.BotID = miniApp.Origin(), api.Self.ID
		dashboard = miniapp.Dashboard(dashboardCfg, db, browsers, miniApp, runner)
	}
	var workers sync.WaitGroup
	if a.cfg.ControlKey != "" {
		if browsers[a.cfg.ControlTelegramID] == nil {
			return errors.New("control API user needs an assigned browser")
		}
		listener, err := net.Listen("tcp", "127.0.0.1:8094")
		if err != nil {
			return fmt.Errorf("listen for private control API: %w", err)
		}
		server := &http.Server{
			Handler:           miniapp.Control(dashboardCfg, db, browsers, a.cfg.ControlTelegramID, a.cfg.ControlKey, runner),
			ReadHeaderTimeout: 5 * time.Second,
			ReadTimeout:       10 * time.Second,
			WriteTimeout:      30 * time.Second,
			IdleTimeout:       30 * time.Second,
			MaxHeaderBytes:    8 << 10,
		}
		context.AfterFunc(ctx, func() { _ = server.Close() })
		workers.Go(func() {
			a.logger.Info("private control API listening", "addr", listener.Addr())
			if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
				a.logger.Error("private control API stopped", "error", err)
				stop()
			}
		})
	}
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

func (a *App) configureReconnectJournal(db *storage.DB) error {
	if a.cfg.ReconnectJournalDir == "-" && a.cfg.DatabaseURL != "" {
		return nil
	}
	if a.cfg.ReconnectJournalDir != "" {
		return db.SetReconnectJournalDirectory(a.cfg.ReconnectJournalDir)
	}
	if a.cfg.DatabasePath != "" || a.cfg.DatabaseURL == "" {
		err := db.SetReconnectJournalDirectory(a.cfg.DatabasePath + ".reconnect")
		if err == nil || a.cfg.DatabaseURL == "" {
			return err
		}
	}
	dir, err := postgresReconnectJournal(a.cfg.DatabaseURL)
	if err == nil {
		err = db.SetReconnectJournalDirectory(dir)
	}
	if err != nil {
		a.logger.Warn("reconnect journal unavailable, using PostgreSQL for rejection records", "err", err)
	}
	return nil
}

func postgresReconnectJournal(databaseURL string) (string, error) {
	state := os.Getenv("XDG_STATE_HOME")
	if !filepath.IsAbs(state) {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		state = filepath.Join(home, ".local", "state")
	}
	database, err := url.Parse(databaseURL)
	if err != nil {
		return "", errors.New("invalid database URL for reconnect journal")
	}
	// Keep the same path across credential rotation.
	options := database.Query()
	identity := database.Host + database.Path + "?" + options.Get("host") + ":" +
		options.Get("port") + ":" + options.Get("search_path")
	key := sha256.Sum256([]byte(identity))
	return filepath.Join(state, "luxmed-watcher", fmt.Sprintf("reconnect-%x", key[:16])), nil
}

type telegramHTTP struct {
	ctx    context.Context
	client *http.Client
}

func (c telegramHTTP) Do(req *http.Request) (*http.Response, error) {
	// Bot API requests have no context unless the caller supplies one.
	if req.Context() == context.Background() {
		req = req.WithContext(c.ctx)
	}
	return c.client.Do(req)
}
