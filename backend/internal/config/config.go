package config

import (
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"
)

type SMTPConfig struct {
	Host        string
	Port        int
	Username    string
	Password    string
	FromAddress string
	FromName    string
	TLSMode     string
}

type TelegramConfig struct {
	BotToken string
}

type MonitoringConfig struct {
	Enabled bool
	Token   string
}

type GeoIPConfig struct {
	Enabled bool
}

type Config struct {
	Env                       string
	HTTPAddr                  string
	PublicURL                 string
	DatabaseURL               string
	DatabaseMigrationMode     string
	SecretsKeyFile            string
	LogLevel                  slog.Level
	AuthSessionTTL            time.Duration
	BootstrapAdminEmail       string
	BootstrapAdminUsername    string
	BootstrapAdminPassword    string
	BootstrapAdminDisplayName string
	SMTP                      SMTPConfig
	Telegram                  TelegramConfig
	Monitoring                MonitoringConfig
	GeoIP                     GeoIPConfig
}

func Load() Config {
	environment := env("ROUTEGATE_ENV", "dev")
	return Config{
		Env:                       environment,
		HTTPAddr:                  env("ROUTEGATE_HTTP_ADDR", ":8080"),
		PublicURL:                 env("ROUTEGATE_PUBLIC_URL", ""),
		DatabaseURL:               env("ROUTEGATE_DATABASE_URL", "postgres://routegate:routegate_dev_password@localhost:5432/routegate?sslmode=disable"),
		DatabaseMigrationMode:     env("ROUTEGATE_DATABASE_MIGRATION_MODE", "startup"),
		SecretsKeyFile:            env("ROUTEGATE_MASTER_KEY_FILE", defaultSecretsKeyFile(environment)),
		LogLevel:                  parseLogLevel(env("ROUTEGATE_LOG_LEVEL", "info")),
		AuthSessionTTL:            time.Duration(envInt("ROUTEGATE_AUTH_SESSION_TTL_HOURS", 24)) * time.Hour,
		BootstrapAdminEmail:       env("ROUTEGATE_BOOTSTRAP_ADMIN_EMAIL", ""),
		BootstrapAdminUsername:    env("ROUTEGATE_BOOTSTRAP_ADMIN_USERNAME", ""),
		BootstrapAdminPassword:    env("ROUTEGATE_BOOTSTRAP_ADMIN_PASSWORD", ""),
		BootstrapAdminDisplayName: env("ROUTEGATE_BOOTSTRAP_ADMIN_DISPLAY_NAME", ""),
		SMTP: SMTPConfig{
			Host:        env("ROUTEGATE_SMTP_HOST", ""),
			Port:        envInt("ROUTEGATE_SMTP_PORT", 587),
			Username:    env("ROUTEGATE_SMTP_USERNAME", ""),
			Password:    env("ROUTEGATE_SMTP_PASSWORD", ""),
			FromAddress: env("ROUTEGATE_SMTP_FROM_ADDRESS", ""),
			FromName:    env("ROUTEGATE_SMTP_FROM_NAME", "RouteGate"),
			TLSMode:     strings.ToLower(env("ROUTEGATE_SMTP_TLS_MODE", "starttls")),
		},
		Telegram: TelegramConfig{
			BotToken: env("ROUTEGATE_TELEGRAM_BOT_TOKEN", ""),
		},
		Monitoring: MonitoringConfig{
			Enabled: envBool("ROUTEGATE_MONITORING_ENABLED", false),
			Token:   env("ROUTEGATE_MONITORING_TOKEN", ""),
		},
		GeoIP: GeoIPConfig{
			Enabled: envBool("ROUTEGATE_GEOIP_ENABLED", true),
		},
	}
}

func defaultSecretsKeyFile(environment string) string {
	if strings.EqualFold(strings.TrimSpace(environment), "production") {
		return "/var/lib/routegate-manager/master.key"
	}
	return ".routegate/master.key"
}

func env(key, fallback string) string {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	return value
}

func parseLogLevel(value string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

func envInt(key string, fallback int) int {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed <= 0 {
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
