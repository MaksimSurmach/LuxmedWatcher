package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Credentials struct {
		Username string `yaml:"username"`
		Password string `yaml:"password"`
	} `yaml:"credentials"`

	Settings struct {
		Doctor         string `yaml:"doctor"`
		City           string `yaml:"city"`
		CheckInterval  int    `yaml:"check_interval"`
	} `yaml:"settings"`

	Notifications struct {
		Email    string `yaml:"email"`
		Telegram string `yaml:"telegram"`
		Webhook  string `yaml:"webhook"`
	} `yaml:"notifications"`
}

// LoadConfig loads configuration from YAML file
func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("Error reading file: %w", err)
	}

	var config Config
	if err := yaml.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("Email unmarshalling YAML: %w", err)
	}

	return &config, nil
}

// SaveConfig saves configuration to YAML file
func SaveConfig(config *Config, path string) error {
	data, err := yaml.Marshal(config)
	if err != nil {
		return fmt.Errorf("Error marshalling YAML: %w", err)
	}

	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("Error writing file: %w", err)
	}

	return nil
}
