package config

import (
	"fmt"
	"os"
	"LuxmedWatcher/internal/domain"
	"gopkg.in/yaml.v3"
)

// Config — корневая структура, отображающая весь YAML.
type Config struct {
	Credentials  CredentialsConfig  `yaml:"credentials"`
	Settings     SettingsConfig     `yaml:"settings"`
	Notifications NotificationsConfig `yaml:"notifications"`
}

// CredentialsConfig — учётные данные для Luxmed.
type CredentialsConfig struct {
	Username string `yaml:"username"`
	Password string `yaml:"password"`
}

// SettingsConfig — настройки приложения (в том числе слоты, которые нужно отслеживать).
type SettingsConfig struct {
	CheckIntervalSec int    `yaml:"check_interval_s"`
	Appointments []AppointmentConfig `yaml:"appointments"`
}

// AppointmentConfig — описание одного набора параметров для поиска слотов.
type AppointmentConfig struct {
	DoctorID         int    `yaml:"doctorid"`
	DoctorName       string `yaml:"doctor_name"`
	CityName         string `yaml:"city_name"`
	CityID           int    `yaml:"cityid"`
	Location         int    `yaml:"location"`
	ServiceVariantID int    `yaml:"serviceVariantId"` // если нужно явно
}

// NotificationsConfig — параметры уведомлений во все каналы.
type NotificationsConfig struct {
	Pushover PushoverConfig `yaml:"pushover"`
	Telegram TelegramConfig `yaml:"telegram"`
	Webhook  WebhookConfig  `yaml:"webhook"`
	Slack    SlackConfig    `yaml:"slack"`
	// Здесь можно расширять: SMS, Email и др.
}

// PushoverConfig — параметры для Pushover.
type PushoverConfig struct {
	User  string `yaml:"user"`
	Token string `yaml:"token"`
}

// TelegramConfig — параметры для Telegram.
type TelegramConfig struct {
	UserID   string `yaml:"user_id"`
	BotToken string `yaml:"bot_token"`
	ChatID   string `yaml:"chat_id"`
}

// WebhookConfig — параметры для Webhook.
type WebhookConfig struct {
	URL     string            `yaml:"url"`
	Method  string            `yaml:"method"`
	Headers map[string]string `yaml:"headers"`
	Body    map[string]any    `yaml:"body"`
}

// SlackConfig — параметры для Slack.
type SlackConfig struct {
	WebhookURL string `yaml:"webhook_url"`
	Channel    string `yaml:"channel"`
}

// LoadConfig загружает конфиг из YAML-файла по указанному пути.
func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("failed to unmarshal config yaml: %w", err)
	}

	// Дополнительно можно вызвать Validate
	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	return &cfg, nil
}

// Validate проверяет, что в конфиге есть необходимые для работы поля.
// Вы можете доработать эту функцию под свои нужды.
func (c *Config) Validate() error {
	if c.Credentials.Username == "" {
		return fmt.Errorf("username is required in config")
	}
	if c.Credentials.Password == "" {
		return fmt.Errorf("password is required in config")
	}

	// Можно проверить минимальные интервалы, doctorid и т.д.
	// Например:
	for i, ap := range c.Settings.Appointments {
		if ap.DoctorID == 0 && ap.ServiceVariantID == 0 {
			return fmt.Errorf("appointment #%d: doctorid or serviceVariantId should not be zero", i)
		}
	}

	return nil
}

func (ac *AppointmentConfig) ToAppointmentSearch() domain.AppointmentSearch {
	return domain.AppointmentSearch{
		DoctorID:         ac.DoctorID,
		CityID:           ac.CityID,
		PlaceID:          ac.Location,
		ServiceVariantID: ac.ServiceVariantID,
		LanguageID:       10,  // значение по умолчанию
		SearchDays:       14,  // значение по умолчанию
	}
}

// GenerateSampleConfigFile создаёт пример config.yaml на диске.
func GenerateSampleConfigFile(path string) error {
	sample := Config{
		Credentials: CredentialsConfig{
			Username: "email@example.com",
			Password: "password123",
		},
		Settings: SettingsConfig{
			CheckIntervalSec: 120,
			Appointments: []AppointmentConfig{
				{
					DoctorID:         7409,
					DoctorName:       "Dr. John Doe",
					CityName:         "Warsaw",
					CityID:           1,
					Location:         5,
					ServiceVariantID: 4468, // пример
				},
			},
		},
		Notifications: NotificationsConfig{
			Pushover: PushoverConfig{
				User:  "user_key",
				Token: "app_token",
			},
			Telegram: TelegramConfig{
				UserID:   "@example_bot",
				BotToken: "1234567890:ABCDEF",
				ChatID:   "1234567890",
			},
			Webhook: WebhookConfig{
				URL:    "https://example.com/webhook",
				Method: "POST",
				Headers: map[string]string{
					"Content-Type": "application/json",
				},
				Body: map[string]any{
					"key": "value",
				},
			},
			Slack: SlackConfig{
				WebhookURL: "https://hooks.slack.com/services/XXX/YYY",
				Channel:    "#channel",
			},
		},
	}

	out, err := yaml.Marshal(sample)
	if err != nil {
		return fmt.Errorf("failed to marshal sample config: %w", err)
	}

	if err := os.WriteFile(path, out, 0644); err != nil {
		return fmt.Errorf("failed to write sample config: %w", err)
	}

	return nil
}
