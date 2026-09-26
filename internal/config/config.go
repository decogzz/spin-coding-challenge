// Package config loads the API's settings from environment variables.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"gitlab.com/decogzz/spin-coding-challenge/internal/provider"
)

type Config struct {
	HTTPAddr        string
	DatabaseURL     string
	LogLevel        slog.Level
	ShutdownTimeout time.Duration
	Provider        provider.Config
}

// Load reads the configuration through getenv (os.Getenv in production) and
// reports every missing or malformed variable at once.
func Load(getenv func(string) string) (Config, error) {
	env := reader{getenv: getenv}
	defaults := provider.DefaultConfig("")

	cfg := Config{
		HTTPAddr:        env.string("HTTP_ADDR", ":8080"),
		DatabaseURL:     env.required("DATABASE_URL"),
		LogLevel:        env.logLevel("LOG_LEVEL", slog.LevelInfo),
		ShutdownTimeout: env.duration("SHUTDOWN_TIMEOUT", 15*time.Second),
		Provider: provider.Config{
			BaseURL:            env.required("PROVIDER_BASE_URL"),
			AttemptTimeout:     env.duration("PROVIDER_ATTEMPT_TIMEOUT", defaults.AttemptTimeout),
			MaxAttempts:        env.positiveInt("PROVIDER_MAX_ATTEMPTS", defaults.MaxAttempts),
			BaseBackoff:        env.duration("PROVIDER_BASE_BACKOFF", defaults.BaseBackoff),
			MaxBackoff:         env.duration("PROVIDER_MAX_BACKOFF", defaults.MaxBackoff),
			BreakerFailures:    uint32(env.positiveInt("PROVIDER_BREAKER_FAILURES", int(defaults.BreakerFailures))), //nolint:gosec // bounded by positiveInt
			BreakerOpenTimeout: env.duration("PROVIDER_BREAKER_OPEN_TIMEOUT", defaults.BreakerOpenTimeout),
		},
	}
	return cfg, errors.Join(env.errs...)
}

type reader struct {
	getenv func(string) string
	errs   []error
}

func (r *reader) string(key, fallback string) string {
	if v := r.getenv(key); v != "" {
		return v
	}
	return fallback
}

func (r *reader) required(key string) string {
	v := r.getenv(key)
	if v == "" {
		r.errs = append(r.errs, fmt.Errorf("%s is required", key))
	}
	return v
}

func (r *reader) duration(key string, fallback time.Duration) time.Duration {
	v := r.getenv(key)
	if v == "" {
		return fallback
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		r.errs = append(r.errs, fmt.Errorf("%s must be a positive duration such as 2s, got %q", key, v))
		return fallback
	}
	return d
}

func (r *reader) positiveInt(key string, fallback int) int {
	v := r.getenv(key)
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 || n > 1_000 {
		r.errs = append(r.errs, fmt.Errorf("%s must be an integer between 1 and 1000, got %q", key, v))
		return fallback
	}
	return n
}

func (r *reader) logLevel(key string, fallback slog.Level) slog.Level {
	v := r.getenv(key)
	if v == "" {
		return fallback
	}
	var level slog.Level
	if err := level.UnmarshalText([]byte(v)); err != nil {
		r.errs = append(r.errs, fmt.Errorf("%s must be debug, info, warn or error, got %q", key, v))
		return fallback
	}
	return level
}
