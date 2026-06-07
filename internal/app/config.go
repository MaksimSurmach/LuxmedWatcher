package app

import (
	"encoding/base64"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	TelegramToken          string
	DatabasePath           string
	MasterKey              []byte
	DefaultLocale          string
	AdminTelegramIDs       map[int64]bool
	MinimumInterval        time.Duration
	DefaultInterval        time.Duration
	NotificationCooldown   time.Duration
	PollTimeout            int
	LogLevel               slog.Level
	EnableRawLuxMedPayload bool
	HealthAddr             string
}

func LoadConfig() (Config, error) {
	cfg := Config{
		TelegramToken:        strings.TrimSpace(os.Getenv("TELEGRAM_BOT_TOKEN")),
		DatabasePath:         envString("DATABASE_PATH", "data/luxmed-watcher.sqlite"),
		DefaultLocale:        envString("DEFAULT_LOCALE", "ru"),
		AdminTelegramIDs:     parseIDSet(os.Getenv("ADMIN_TELEGRAM_IDS")),
		MinimumInterval:      envDuration("MIN_CHECK_INTERVAL", 2*time.Minute),
		DefaultInterval:      envDuration("DEFAULT_CHECK_INTERVAL", 5*time.Minute),
		NotificationCooldown: envDuration("NOTIFICATION_COOLDOWN", 6*time.Hour),
		PollTimeout:          envInt("TELEGRAM_POLL_TIMEOUT", 30),
		LogLevel:             parseLogLevel(envString("LOG_LEVEL", "INFO")),
		HealthAddr:           envString("HEALTH_ADDR", ":8080"),
	}
	cfg.EnableRawLuxMedPayload = envBool("ENABLE_RAW_LUXMED_PAYLOAD", false)

	key, err := parseMasterKey(os.Getenv("APP_MASTER_KEY"))
	if err != nil {
		return Config{}, err
	}
	cfg.MasterKey = key

	if cfg.TelegramToken == "" {
		return Config{}, fmt.Errorf("TELEGRAM_BOT_TOKEN is required")
	}
	if cfg.DefaultLocale != "en" && cfg.DefaultLocale != "ru" && cfg.DefaultLocale != "pl" {
		return Config{}, fmt.Errorf("DEFAULT_LOCALE must be one of en, ru, pl")
	}
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
