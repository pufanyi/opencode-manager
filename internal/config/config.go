package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Telegram TelegramConfig
	Process  ProcessConfig
	Web      WebConfig
	Firebase FirebaseConfig
}

type FirebaseConfig struct {
	Enabled     bool
	APIKey      string
	DatabaseURL string
	Email       string // Go client service account email
	Password    string // Go client service account password
}

type WebConfig struct {
	Enabled bool
	Addr    string
}

type TelegramConfig struct {
	Token         string
	AllowedUsers  []int64
	BoardInterval time.Duration
}

type ProcessConfig struct {
	OpencodeBinary      string
	ClaudeCodeBinary    string
	PortRange           PortRange
	HealthCheckInterval time.Duration
	MaxRestartAttempts  int
}

type PortRange struct {
	Start int
	End   int
}

// Defaults returns a Config with sensible default values.
func Defaults() *Config {
	return &Config{
		Telegram: TelegramConfig{
			BoardInterval: 2 * time.Second,
		},
		Process: ProcessConfig{
			OpencodeBinary:      "opencode",
			ClaudeCodeBinary:    "claude",
			PortRange:           PortRange{Start: 14096, End: 14196},
			HealthCheckInterval: 30 * time.Second,
			MaxRestartAttempts:  3,
		},
	}
}

// LoadFromSettings builds a Config from a key-value settings map (from DB).
func LoadFromSettings(settings map[string]string) *Config {
	cfg := Defaults()

	if v, ok := settings["telegram.token"]; ok {
		cfg.Telegram.Token = v
	}
	if v, ok := settings["telegram.allowed_users"]; ok {
		cfg.Telegram.AllowedUsers = parseIntList(v)
	}
	if v, ok := settings["telegram.board_interval"]; ok {
		if d, err := time.ParseDuration(v); err == nil {
			cfg.Telegram.BoardInterval = d
		}
	}
	if v, ok := settings["process.opencode_binary"]; ok {
		cfg.Process.OpencodeBinary = v
	}
	if v, ok := settings["process.claudecode_binary"]; ok {
		cfg.Process.ClaudeCodeBinary = v
	}
	if v, ok := settings["process.port_range_start"]; ok {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.Process.PortRange.Start = n
		}
	}
	if v, ok := settings["process.port_range_end"]; ok {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.Process.PortRange.End = n
		}
	}
	if v, ok := settings["process.health_check_interval"]; ok {
		if d, err := time.ParseDuration(v); err == nil {
			cfg.Process.HealthCheckInterval = d
		}
	}
	if v, ok := settings["process.max_restart_attempts"]; ok {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.Process.MaxRestartAttempts = n
		}
	}
	if v, ok := settings["web.enabled"]; ok {
		cfg.Web.Enabled = v == "true"
	}
	if v, ok := settings["web.addr"]; ok {
		cfg.Web.Addr = v
	}

	// Firebase
	if v, ok := settings["firebase.enabled"]; ok {
		cfg.Firebase.Enabled = v == "true"
	}
	if v, ok := settings["firebase.api_key"]; ok {
		cfg.Firebase.APIKey = v
	}
	if v, ok := settings["firebase.database_url"]; ok {
		cfg.Firebase.DatabaseURL = v
	}
	if v, ok := settings["firebase.email"]; ok {
		cfg.Firebase.Email = v
	}
	if v, ok := settings["firebase.password"]; ok {
		cfg.Firebase.Password = v
	}

	return cfg
}

// ToSettings converts a Config to a key-value settings map for DB storage.
func (c *Config) ToSettings() map[string]string {
	m := map[string]string{
		"telegram.token":                c.Telegram.Token,
		"telegram.allowed_users":        formatIntList(c.Telegram.AllowedUsers),
		"telegram.board_interval":       c.Telegram.BoardInterval.String(),
		"process.opencode_binary":       c.Process.OpencodeBinary,
		"process.claudecode_binary":     c.Process.ClaudeCodeBinary,
		"process.port_range_start":      strconv.Itoa(c.Process.PortRange.Start),
		"process.port_range_end":        strconv.Itoa(c.Process.PortRange.End),
		"process.health_check_interval": c.Process.HealthCheckInterval.String(),
		"process.max_restart_attempts":  strconv.Itoa(c.Process.MaxRestartAttempts),
		"web.enabled":                   strconv.FormatBool(c.Web.Enabled),
		"web.addr":                      c.Web.Addr,
		"firebase.enabled":              strconv.FormatBool(c.Firebase.Enabled),
		"firebase.api_key":              c.Firebase.APIKey,
		"firebase.database_url":         c.Firebase.DatabaseURL,
		"firebase.email":                c.Firebase.Email,
		"firebase.password":             c.Firebase.Password,
	}
	return m
}

// ApplyEnvOverrides applies environment variable overrides to the config.
func ApplyEnvOverrides(cfg *Config) {
	if v := os.Getenv("TELEGRAM_TOKEN"); v != "" {
		cfg.Telegram.Token = v
	}
	if v := os.Getenv("TELEGRAM_ALLOWED_USERS"); v != "" {
		if users := parseIntList(v); len(users) > 0 {
			cfg.Telegram.AllowedUsers = users
		}
	}
	if v := os.Getenv("OPENCODE_BINARY"); v != "" {
		cfg.Process.OpencodeBinary = v
	}
	if v := os.Getenv("CLAUDECODE_BINARY"); v != "" {
		cfg.Process.ClaudeCodeBinary = v
	}
	if v := os.Getenv("FIREBASE_API_KEY"); v != "" {
		cfg.Firebase.APIKey = v
	}
	if v := os.Getenv("FIREBASE_DATABASE_URL"); v != "" {
		cfg.Firebase.DatabaseURL = v
	}
	if v := os.Getenv("FIREBASE_EMAIL"); v != "" {
		cfg.Firebase.Email = v
	}
	if v := os.Getenv("FIREBASE_PASSWORD"); v != "" {
		cfg.Firebase.Password = v
	}
	if v := os.Getenv("FIREBASE_ENABLED"); v == "true" {
		cfg.Firebase.Enabled = true
	}
}

// Validate checks that the config is valid.
func Validate(cfg *Config) error {
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

func parseIntList(s string) []int64 {
	var result []int64
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if id, err := strconv.ParseInt(part, 10, 64); err == nil {
			result = append(result, id)
		}
	}
	return result
}

func formatIntList(ids []int64) string {
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = strconv.FormatInt(id, 10)
	}
	return strings.Join(parts, ",")
}
