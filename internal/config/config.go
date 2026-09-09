// Package config reads the process configuration from the environment.
// Everything the user can change at runtime lives in the database instead.
package config

import (
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config is the process-level configuration.
type Config struct {
	ListenAddr  string
	DBPath      string
	BasicAuth   string // "user:password"; empty disables authentication
	LogLevel    slog.Level
	LogFormat   string // "text" or "json"
	HTTPTimeout time.Duration
}

// FromEnv builds a Config from TGH_* environment variables.
func FromEnv() (Config, error) {
	cfg := Config{
		ListenAddr:  env("TGH_LISTEN_ADDR", "127.0.0.1:8080"),
		DBPath:      env("TGH_DB_PATH", "data/tg-harvester.db"),
		BasicAuth:   os.Getenv("TGH_BASIC_AUTH"),
		LogFormat:   env("TGH_LOG_FORMAT", "text"),
		HTTPTimeout: 60 * time.Second,
	}
	if err := cfg.LogLevel.UnmarshalText([]byte(env("TGH_LOG_LEVEL", "info"))); err != nil {
		return Config{}, fmt.Errorf("TGH_LOG_LEVEL: %w", err)
	}
	if cfg.LogFormat != "text" && cfg.LogFormat != "json" {
		return Config{}, fmt.Errorf("TGH_LOG_FORMAT: want \"text\" or \"json\", got %q", cfg.LogFormat)
	}
	if raw := os.Getenv("TGH_HTTP_TIMEOUT"); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil || d <= 0 {
			return Config{}, fmt.Errorf("TGH_HTTP_TIMEOUT: want a positive duration such as 60s, got %q", raw)
		}
		cfg.HTTPTimeout = d
	}
	if cfg.BasicAuth != "" {
		if _, _, ok := strings.Cut(cfg.BasicAuth, ":"); !ok {
			return Config{}, fmt.Errorf("TGH_BASIC_AUTH: want \"user:password\"")
		}
	}
	return cfg, nil
}

// Logger builds the application logger.
func (c Config) Logger(w *os.File) *slog.Logger {
	opts := &slog.HandlerOptions{Level: c.LogLevel}
	if c.LogFormat == "json" {
		return slog.New(slog.NewJSONHandler(w, opts))
	}
	return slog.New(slog.NewTextHandler(w, opts))
}

// String renders the configuration for the startup log, without secrets.
func (c Config) String() string {
	return fmt.Sprintf("listen=%s db=%s basic_auth=%s log=%s/%s http_timeout=%s",
		c.ListenAddr, c.DBPath, strconv.FormatBool(c.BasicAuth != ""), c.LogFormat, c.LogLevel, c.HTTPTimeout)
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
