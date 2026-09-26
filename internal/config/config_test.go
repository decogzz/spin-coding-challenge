package config

import (
	"log/slog"
	"strings"
	"testing"
	"time"
)

func envFrom(values map[string]string) func(string) string {
	return func(key string) string { return values[key] }
}

func TestLoadDefaults(t *testing.T) {
	cfg, err := Load(envFrom(map[string]string{
		"DATABASE_URL":      "postgres://localhost/db",
		"PROVIDER_BASE_URL": "http://provider:8081",
	}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.HTTPAddr != ":8080" || cfg.LogLevel != slog.LevelInfo || cfg.ShutdownTimeout != 15*time.Second {
		t.Errorf("unexpected defaults: %+v", cfg)
	}
	p := cfg.Provider
	if p.BaseURL != "http://provider:8081" || p.AttemptTimeout != 2*time.Second || p.MaxAttempts != 3 ||
		p.BreakerFailures != 5 || p.BreakerOpenTimeout != 30*time.Second {
		t.Errorf("unexpected provider defaults: %+v", p)
	}
}

func TestLoadOverrides(t *testing.T) {
	cfg, err := Load(envFrom(map[string]string{
		"HTTP_ADDR":                     ":9000",
		"DATABASE_URL":                  "postgres://localhost/db",
		"LOG_LEVEL":                     "debug",
		"PROVIDER_BASE_URL":             "http://provider:8081",
		"PROVIDER_ATTEMPT_TIMEOUT":      "750ms",
		"PROVIDER_MAX_ATTEMPTS":         "1",
		"PROVIDER_BREAKER_FAILURES":     "10",
		"PROVIDER_BREAKER_OPEN_TIMEOUT": "1m",
	}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.HTTPAddr != ":9000" || cfg.LogLevel != slog.LevelDebug {
		t.Errorf("unexpected config: %+v", cfg)
	}
	p := cfg.Provider
	if p.AttemptTimeout != 750*time.Millisecond || p.MaxAttempts != 1 || p.BreakerFailures != 10 || p.BreakerOpenTimeout != time.Minute {
		t.Errorf("unexpected provider config: %+v", p)
	}
}

func TestLoadReportsEveryProblem(t *testing.T) {
	_, err := Load(envFrom(map[string]string{
		"LOG_LEVEL":                "loud",
		"PROVIDER_ATTEMPT_TIMEOUT": "soon",
		"PROVIDER_MAX_ATTEMPTS":    "0",
	}))
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, key := range []string{"DATABASE_URL", "PROVIDER_BASE_URL", "LOG_LEVEL", "PROVIDER_ATTEMPT_TIMEOUT", "PROVIDER_MAX_ATTEMPTS"} {
		if !strings.Contains(err.Error(), key) {
			t.Errorf("error does not mention %s: %v", key, err)
		}
	}
}
