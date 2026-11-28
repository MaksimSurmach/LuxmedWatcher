package app

import (
	"cmp"
	"encoding/base64"
	"fmt"
	"log/slog"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/maksimsurmach/luxmed-watcher/internal/i18n"
	"github.com/maksimsurmach/luxmed-watcher/internal/luxmed"
)

type Config struct {
	TelegramToken          string
	DatabasePath           string
	DatabaseURL            string
	ReconnectJournalDir    string
	MasterKey              []byte
	DefaultLocale          string
	AdminTelegramIDs       map[int64]bool
	MinimumInterval        time.Duration
	DefaultInterval        time.Duration
	NotificationCooldown   time.Duration
	HistoryRetention       time.Duration
	PollTimeout            int
	LogLevel               slog.Level
	EnableRawLuxMedPayload bool
	HealthAddr             string
	BrowserAPIURL          string
	BrowserRoutes          map[int64]luxmed.BrowserRoute
	BrowserWorkerKey       string
	BrowserViewerURL       string
	MiniAppURL             string
	MiniAppLink            string
	MiniAppAgentURL        string
	MiniAppTicketKey       string
	ControlKey             string
	ControlTelegramID      int64
}

func LoadConfig() (Config, error) {
	cfg := Config{
		TelegramToken:        strings.TrimSpace(os.Getenv("TELEGRAM_BOT_TOKEN")),
		DatabasePath:         envString("DATABASE_PATH", "data/luxmed-watcher.sqlite"),
		DatabaseURL:          strings.TrimSpace(os.Getenv("DATABASE_URL")),
		ReconnectJournalDir:  strings.TrimSpace(os.Getenv("RECONNECT_JOURNAL_DIR")),
		DefaultLocale:        envString("DEFAULT_LOCALE", "ru"),
		AdminTelegramIDs:     parseIDSet(os.Getenv("ADMIN_TELEGRAM_IDS")),
		MinimumInterval:      envDuration("MIN_CHECK_INTERVAL", 2*time.Minute),
		DefaultInterval:      envDuration("DEFAULT_CHECK_INTERVAL", 10*time.Minute),
		NotificationCooldown: envDuration("NOTIFICATION_COOLDOWN", 6*time.Hour),
		PollTimeout:          envInt("TELEGRAM_POLL_TIMEOUT", 30),
		LogLevel:             parseLogLevel(envString("LOG_LEVEL", "INFO")),
		HealthAddr:           envString("HEALTH_ADDR", ":8080"),
		BrowserAPIURL:        strings.TrimSpace(os.Getenv("LUXMED_BROWSER_API_URL")),
		BrowserViewerURL:     strings.TrimSpace(os.Getenv("LUXMED_VIEWER_URL")),
		BrowserWorkerKey:     strings.TrimSpace(os.Getenv("LUXMED_WORKER_KEY")),
		MiniAppURL:           strings.TrimSpace(os.Getenv("LUXMED_MINIAPP_URL")),
		MiniAppLink:          strings.TrimRight(strings.TrimSpace(os.Getenv("LUXMED_MINIAPP_LINK")), "/"),
		MiniAppAgentURL:      strings.TrimSpace(os.Getenv("LUXMED_MINIAPP_AGENT_URL")),
		MiniAppTicketKey:     strings.TrimSpace(os.Getenv("LUXMED_INTERNAL_KEY")),
		ControlKey:           strings.TrimSpace(os.Getenv("LUXMED_CONTROL_KEY")),
	}
	cfg.EnableRawLuxMedPayload = envBool("ENABLE_RAW_LUXMED_PAYLOAD", false)

	key, err := parseMasterKey(os.Getenv("APP_MASTER_KEY"))
	if err != nil {
		return Config{}, err
	}
	cfg.MasterKey = key

	cfg.BrowserRoutes, err = luxmed.ParseBrowserRoutes(os.Getenv("LUXMED_BROWSER_ROUTES"))
	if err != nil {
		return Config{}, err
	}
	if os.Getenv("LUXMED_BROWSER_SHARED") == "1" {
		if cfg.BrowserRoutes != nil || !luxmed.ValidTicketKey(cfg.BrowserWorkerKey) {
			return Config{}, fmt.Errorf("shared workers require LUXMED_WORKER_KEY and account IDs")
		}
		cfg.BrowserRoutes, err = luxmed.SharedBrowserRoutes(cfg.BrowserAPIURL, os.Getenv("LUXMED_BROWSER_VIEWER_URL"),
			cmp.Or(os.Getenv("LUXMED_BROWSER_ACCOUNT_IDS"), os.Getenv("ADMIN_TELEGRAM_IDS")))
		if err != nil {
			return Config{}, err
		}
	}
	if cfg.BrowserRoutes == nil && cfg.BrowserAPIURL != "" && len(cfg.AdminTelegramIDs) != 1 {
		return Config{}, fmt.Errorf("browser mode requires exactly one admin Telegram ID")
	}
	if (cfg.MiniAppURL == "") != (cfg.MiniAppAgentURL == "") || (cfg.MiniAppURL != "" && cfg.BrowserAPIURL == "" && cfg.BrowserRoutes == nil) {
		return Config{}, fmt.Errorf("Mini App requires browser mode and both Mini App URLs")
	}
	// Widgets can open a Mini App only through its t.me direct link, which carries the slot as startapp.
	if link := cfg.MiniAppLink; link != "" && (cfg.MiniAppURL == "" || !strings.HasPrefix(link, "https://t.me/") || strings.ContainsAny(link, "?#")) {
		return Config{}, fmt.Errorf("LUXMED_MINIAPP_LINK must be a t.me Mini App link without a query, used with LUXMED_MINIAPP_URL")
	}
	if cfg.BrowserRoutes != nil {
		adminAssigned := false
		for id := range cfg.AdminTelegramIDs {
			_, assigned := cfg.BrowserRoutes[id]
			adminAssigned = adminAssigned || assigned
		}
		if cfg.MiniAppURL == "" || !adminAssigned || !luxmed.ValidTicketKey(cfg.MiniAppTicketKey) {
			return Config{}, fmt.Errorf("browser routes require the Mini App, a browser assigned to an admin and a 32-byte hex LUXMED_INTERNAL_KEY")
		}
	}
	if cfg.ControlKey != "" {
		if !luxmed.ValidTicketKey(cfg.ControlKey) {
			return Config{}, fmt.Errorf("LUXMED_CONTROL_KEY must be a 32-byte hex key")
		}
		if raw := strings.TrimSpace(os.Getenv("LUXMED_CONTROL_TELEGRAM_ID")); raw != "" {
			cfg.ControlTelegramID, err = strconv.ParseInt(raw, 10, 64)
			if err != nil || cfg.ControlTelegramID <= 0 {
				return Config{}, fmt.Errorf("invalid LUXMED_CONTROL_TELEGRAM_ID")
			}
		} else if len(cfg.AdminTelegramIDs) == 1 {
			for id := range cfg.AdminTelegramIDs {
				cfg.ControlTelegramID = id
			}
		} else {
			return Config{}, fmt.Errorf("set LUXMED_CONTROL_TELEGRAM_ID to the assigned account")
		}
	}
	if cfg.TelegramToken == "" {
		return Config{}, fmt.Errorf("TELEGRAM_BOT_TOKEN is required")
	}
	if !slices.Contains(i18n.SupportedLocales, cfg.DefaultLocale) {
		return Config{}, fmt.Errorf("DEFAULT_LOCALE must be one of %s", strings.Join(i18n.SupportedLocales, ", "))
	}
	if cfg.MinimumInterval < time.Second || cfg.PollTimeout < 1 || cfg.PollTimeout > 60 || cfg.NotificationCooldown < 0 {
		return Config{}, fmt.Errorf("invalid check interval, polling timeout or notification cooldown")
	}
	retentionDays := envInt("DATA_RETENTION_DAYS", 180)
	if retentionDays < 0 || retentionDays > 3650 {
		return Config{}, fmt.Errorf("DATA_RETENTION_DAYS must be between 0 and 3650")
	}
	cfg.HistoryRetention = time.Duration(retentionDays) * 24 * time.Hour
	if cfg.DefaultInterval < cfg.MinimumInterval {
		cfg.DefaultInterval = cfg.MinimumInterval
	}
	return cfg, nil
}

func envString(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func envInt(key string, fallback int) int {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func envBool(key string, fallback bool) bool {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func envDuration(key string, fallback time.Duration) time.Duration {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	if parsed, err := time.ParseDuration(value); err == nil {
		return parsed
	}
	seconds, err := strconv.Atoi(value)
	if err != nil {
		return fallback
	}
	return time.Duration(seconds) * time.Second
}

func parseIDSet(raw string) map[int64]bool {
	ids := make(map[int64]bool)
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		id, err := strconv.ParseInt(part, 10, 64)
		if err == nil {
			ids[id] = true
		}
	}
	return ids
}

func parseMasterKey(raw string) ([]byte, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, fmt.Errorf("APP_MASTER_KEY is required; use 32 raw bytes encoded as base64")
	}
	decoded, err := base64.StdEncoding.DecodeString(raw)
	if err == nil && len(decoded) == 32 {
		return decoded, nil
	}
	if len(raw) == 32 {
		return []byte(raw), nil
	}
	return nil, fmt.Errorf("APP_MASTER_KEY must decode to 32 bytes")
}

func parseLogLevel(raw string) slog.Level {
	switch strings.ToUpper(strings.TrimSpace(raw)) {
	case "DEBUG":
		return slog.LevelDebug
	case "WARN":
		return slog.LevelWarn
	case "ERROR":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
