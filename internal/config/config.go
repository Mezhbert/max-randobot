// Package config загружает конфигурацию бота из переменных окружения (.env).
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"

	"github.com/joho/godotenv"
)

// Config — конфигурация приложения.
type Config struct {
	BotToken      string
	WebhookURL    string
	WebhookSecret string
	WebhookPath   string
	HTTPPort      string
	DBDSN         string
	DBMigrate     bool
	TZ            string
	MaxRPS        int
	LogLevel      slog.Level
	MetricsPath   string // путь для endpoint /metrics (по умолчанию /metrics)
	DedupTTL      string // TTL дедупликатора (по умолчанию из dedup.DefaultTTL)
}

// Load загружает конфигурацию. Файл .env опционален (переменные окружения
// имеют приоритет — это позволяет настраивать в docker-compose / systemd).
func Load(files ...string) (*Config, error) {
	// Пытаемся подгрузить .env, если он есть. Ошибку "нет файла" игнорируем.
	for _, f := range files {
		_ = godotenv.Load(f) //nolint:errcheck // опционально
	}

	cfg := &Config{
		BotToken:      getEnv("BOT_TOKEN", ""),
		WebhookURL:    strings.TrimRight(getEnv("WEBHOOK_URL", ""), "/"),
		WebhookSecret: getEnv("WEBHOOK_SECRET", ""),
		WebhookPath:   getEnv("WEBHOOK_PATH", "/webhook"),
		HTTPPort:      getEnv("HTTP_PORT", "8080"),
		DBDSN:         getEnv("DB_DSN", ""),
		DBMigrate:     getEnvBool("DB_MIGRATE", true),
		TZ:            getEnv("TZ", "Europe/Moscow"),
		MaxRPS:        getEnvInt("MAX_RPS", 28),
		LogLevel:      parseLevel(getEnv("LOG_LEVEL", "info")),
		MetricsPath:   getEnv("METRICS_PATH", "/metrics"),
		DedupTTL:      getEnv("DEDUP_TTL", ""),
	}

	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// FullWebhookURL возвращает полный URL вебхука (URL + path).
func (c *Config) FullWebhookURL() string { return c.WebhookURL + c.WebhookPath }

func (c *Config) validate() error {
	var errs []string
	if c.BotToken == "" {
		errs = append(errs, "BOT_TOKEN не задан")
	}
	if c.WebhookURL == "" {
		errs = append(errs, "WEBHOOK_URL не задан")
	} else if !strings.HasPrefix(c.WebhookURL, "https://") {
		// С 25 мая платформа MAX требует HTTPS для вебхуков.
		errs = append(errs, "WEBHOOK_URL должен начинаться с https:// (HTTP запрещён платформой MAX)")
	}
	if c.WebhookSecret == "" {
		errs = append(errs, "WEBHOOK_SECRET не задан")
	} else if len(c.WebhookSecret) < 5 || len(c.WebhookSecret) > 256 {
		errs = append(errs, "WEBHOOK_SECRET должен быть 5-256 символов")
	}
	if c.DBDSN == "" {
		errs = append(errs, "DB_DSN не задан")
	}
	if c.MaxRPS < 1 || c.MaxRPS > 30 {
		errs = append(errs, "MAX_RPS должен быть 1-30 (лимит платформы MAX — 30 rps)")
	}
	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}
	return nil
}

func getEnv(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return def
}

func getEnvInt(key string, def int) int {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func getEnvBool(key string, def bool) bool {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			return b
		}
	}
	return def
}

func parseLevel(s string) slog.Level {
	switch strings.ToLower(s) {
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

// String — безопасное строковое представление (без токена).
func (c *Config) String() string {
	return fmt.Sprintf("webhook=%s port=%s tz=%s rps=%d migrate=%v metrics=%s",
		c.FullWebhookURL(), c.HTTPPort, c.TZ, c.MaxRPS, c.DBMigrate, c.MetricsPath)
}
