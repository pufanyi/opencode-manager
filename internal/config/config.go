package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Telegram TelegramConfig `yaml:"telegram"`
	Process  ProcessConfig  `yaml:"process"`
	Projects []ProjectConfig `yaml:"projects"`
	Storage  StorageConfig  `yaml:"storage"`
}

type TelegramConfig struct {
	Token        string  `yaml:"token"`
	AllowedUsers []int64 `yaml:"allowed_users"`
}

type ProcessConfig struct {
	OpencodeBinary      string        `yaml:"opencode_binary"`
	PortRange           PortRange     `yaml:"port_range"`
	HealthCheckInterval time.Duration `yaml:"health_check_interval"`
	MaxRestartAttempts  int           `yaml:"max_restart_attempts"`
}

type PortRange struct {
	Start int `yaml:"start"`
	End   int `yaml:"end"`
}

type ProjectConfig struct {
	Name      string `yaml:"name"`
	Directory string `yaml:"directory"`
	AutoStart bool   `yaml:"auto_start"`
}

type StorageConfig struct {
	Database string `yaml:"database"`
}

func Load(path string) (*Config, error) {
	cfg := &Config{
		Process: ProcessConfig{
			OpencodeBinary:      "opencode",
			PortRange:           PortRange{Start: 14096, End: 14196},
			HealthCheckInterval: 30 * time.Second,
			MaxRestartAttempts:  3,
		},
		Storage: StorageConfig{
			Database: "./data/opencode-manager.db",
		},
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config file: %w", err)
	}

	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parsing config file: %w", err)
	}

	applyEnvOverrides(cfg)

	if err := validate(cfg); err != nil {
		return nil, fmt.Errorf("config validation: %w", err)
	}

	return cfg, nil
}

func applyEnvOverrides(cfg *Config) {
	if v := os.Getenv("TELEGRAM_TOKEN"); v != "" {
		cfg.Telegram.Token = v
	}
	if v := os.Getenv("TELEGRAM_ALLOWED_USERS"); v != "" {
		var users []int64
		for _, s := range strings.Split(v, ",") {
			s = strings.TrimSpace(s)
			if id, err := strconv.ParseInt(s, 10, 64); err == nil {
				users = append(users, id)
			}
		}
		if len(users) > 0 {
			cfg.Telegram.AllowedUsers = users
		}
	}
	if v := os.Getenv("OPENCODE_BINARY"); v != "" {
		cfg.Process.OpencodeBinary = v
	}
	if v := os.Getenv("STORAGE_DATABASE"); v != "" {
		cfg.Storage.Database = v
	}
}

func validate(cfg *Config) error {
	if cfg.Telegram.Token == "" {
		return fmt.Errorf("telegram.token is required")
	}
	if len(cfg.Telegram.AllowedUsers) == 0 {
		return fmt.Errorf("telegram.allowed_users must have at least one user")
	}
	if cfg.Process.PortRange.Start >= cfg.Process.PortRange.End {
		return fmt.Errorf("port_range.start must be less than port_range.end")
	}
	if cfg.Process.PortRange.Start < 1024 || cfg.Process.PortRange.End > 65535 {
		return fmt.Errorf("port_range must be within 1024-65535")
	}
	return nil
}
