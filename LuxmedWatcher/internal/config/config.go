package config

import (
	"LuxmedWatcher/internal/domain"
	"fmt"

	"github.com/go-playground/validator/v10"
	log "github.com/sirupsen/logrus"
	"github.com/spf13/viper"
)

// Config — root structure for the application configuration
type Config struct {
	Credentials   CredentialsConfig   `mapstructure:"credentials" validate:"required"`
	Settings      SettingsConfig      `mapstructure:"settings"`
	Notifications NotificationsConfig `mapstructure:"notifications" validate:"required,gt=0,dive"`
	Appointments  []AppointmentConfig `mapstructure:"appointments"`
}

// CredentialsConfig — authorization credentials for the Luxmed API
type CredentialsConfig struct {
	Username string `mapstructure:"username" validate:"required"`
	Password string `mapstructure:"password" validate:"required"`
}

// SettingsConfig — application settings
type SettingsConfig struct {
	CheckIntervalSec int    `mapstructure:"check_interval_s"`
	DbPath           string `mapstructure:"db_path"`
	DbProvider       string `mapstructure:"db_provider"`
	LogPath          string `mapstructure:"log_path"`
	LogLevel         string `mapstructure:"log_level"`
	Language         string `mapstructure:"language"`
}

// AppointmentConfig — appointment configuration with id
type AppointmentConfig struct {
	DoctorID           int    `mapstructure:"doctorid"`
	DoctorName         string `mapstructure:"doctor_name"`
	CityName           string `mapstructure:"city_name"`
	CityID             int    `mapstructure:"cityid"`
	Location           int    `mapstructure:"location"`
	LocationName       string `mapstructure:"location_name"`
	ServiceVariantID   int    `mapstructure:"serviceVariantId"`
	ServiceVariantName string `mapstructure:"serviceVariantName"`
}

type NotifierConfig map[string]interface{}

type NotificationsConfig []NotifierConfig

// LoadConfig — load configuration from the file
func LoadConfig(path string) (*Config, error) {
	v := viper.New()
	v.SetConfigFile(path)
	v.SetConfigType("yaml")

	// set default values
	v.SetDefault("settings.check_interval_s", 20)
	v.SetDefault("settings.db_path", "db.sqlite")
	v.SetDefault("settings.db_provider", "sqlite")
	v.SetDefault("settings.log_path", "log.txt")
	v.SetDefault("settings.log_level", "INFO")
	v.SetDefault("settings.language", "pl")

	if err := v.ReadInConfig(); err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}
	log.Info("Config file loaded")

	var cfg Config
	if err := v.Unmarshal(&cfg); err != nil {
		return nil, err
	}

	validate := validator.New()
	if err := validate.Struct(&cfg); err != nil {
		return nil, fmt.Errorf("failed to validate config: %w", err)
	}
	log.Info("Config validated")
	return &cfg, nil
}

func (ac *AppointmentConfig) ToAppointmentSearch() domain.AppointmentSearch {
	return domain.AppointmentSearch{
		DoctorID:         ac.DoctorID,
		CityID:           ac.CityID,
		PlaceID:          ac.Location,
		ServiceVariantID: ac.ServiceVariantID,
		LanguageID:       10, // default value
		SearchDays:       14, // default value
	}
}
