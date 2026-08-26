package config_test

import (
	"strings"
	"testing"
	"time"

	"github.com/vance1852/orientation-enrollment-platform/internal/config"
)

func lookupFrom(values map[string]string) func(string) (string, bool) {
	return func(key string) (string, bool) {
		value, ok := values[key]
		return value, ok
	}
}

func TestLoadAppliesDefaults(t *testing.T) {
	cfg, err := config.Load(lookupFrom(nil))
	if err != nil {
		t.Fatalf("loading defaults failed: %v", err)
	}
	if cfg.HTTPAddr != ":8080" {
		t.Fatalf("HTTPAddr = %q", cfg.HTTPAddr)
	}
	if cfg.SessionTTL != 12*time.Hour {
		t.Fatalf("SessionTTL = %s", cfg.SessionTTL)
	}
	if !cfg.WorkerEnabled {
		t.Fatal("the worker must be enabled by default")
	}
	if cfg.MaxEnrollmentRetries != 4 {
		t.Fatalf("MaxEnrollmentRetries = %d", cfg.MaxEnrollmentRetries)
	}
	if cfg.BusinessLocation != "Asia/Shanghai" {
		t.Fatalf("BusinessLocation = %q", cfg.BusinessLocation)
	}
}

func TestLoadReadsEveryOverride(t *testing.T) {
	cfg, err := config.Load(lookupFrom(map[string]string{
		"APP_HTTP_ADDR":              "127.0.0.1:9999",
		"APP_DATABASE_DSN":           "file:/tmp/orientation.db",
		"APP_LOG_LEVEL":              "debug",
		"APP_SESSION_TTL":            "30m",
		"APP_SHUTDOWN_GRACE":         "5s",
		"APP_REQUEST_TIMEOUT":        "3s",
		"APP_WORKER_POLL_INTERVAL":   "250ms",
		"APP_WORKER_LEASE":           "45s",
		"APP_WORKER_BACKOFF_BASE":    "1s",
		"APP_WORKER_BACKOFF_MAX":     "20s",
		"APP_WORKER_ENABLED":         "false",
		"APP_SEED_DEMO_DATA":         "false",
		"APP_BUSINESS_TZ":            "UTC",
		"APP_MAX_ENROLLMENT_RETRIES": "0",
	}))
	if err != nil {
		t.Fatalf("loading overrides failed: %v", err)
	}
	if cfg.HTTPAddr != "127.0.0.1:9999" || cfg.LogLevel != "debug" {
		t.Fatalf("config = %+v", cfg)
	}
	if cfg.SessionTTL != 30*time.Minute || cfg.WorkerPollInterval != 250*time.Millisecond {
		t.Fatalf("durations = %s %s", cfg.SessionTTL, cfg.WorkerPollInterval)
	}
	if cfg.WorkerEnabled || cfg.SeedDemoData {
		t.Fatal("both toggles must be off")
	}
	if cfg.MaxEnrollmentRetries != 0 {
		t.Fatalf("MaxEnrollmentRetries = %d", cfg.MaxEnrollmentRetries)
	}
}

func TestLoadRejectsInvalidValues(t *testing.T) {
	cases := map[string]map[string]string{
		"malformed duration":      {"APP_SESSION_TTL": "half an hour"},
		"non positive duration":   {"APP_SESSION_TTL": "-1h"},
		"session ttl too small":   {"APP_SESSION_TTL": "30s"},
		"malformed boolean":       {"APP_WORKER_ENABLED": "maybe"},
		"retries out of range":    {"APP_MAX_ENROLLMENT_RETRIES": "99"},
		"retries not a number":    {"APP_MAX_ENROLLMENT_RETRIES": "many"},
		"backoff inverted":        {"APP_WORKER_BACKOFF_BASE": "1m", "APP_WORKER_BACKOFF_MAX": "1s"},
		"poll interval too small": {"APP_WORKER_POLL_INTERVAL": "1ms"},
	}
	for name, values := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := config.Load(lookupFrom(values)); err == nil {
				t.Fatal("expected the configuration to be rejected")
			}
		})
	}
}

func TestBlankOverridesFallBackToDefaults(t *testing.T) {
	cfg, err := config.Load(lookupFrom(map[string]string{
		"APP_HTTP_ADDR":    "   ",
		"APP_DATABASE_DSN": "",
		"APP_SESSION_TTL":  "  ",
	}))
	if err != nil {
		t.Fatalf("blank overrides must be ignored, got %v", err)
	}
	if cfg.HTTPAddr != ":8080" || cfg.SessionTTL != 12*time.Hour {
		t.Fatalf("config = %+v, want the built-in defaults", cfg)
	}
	if cfg.DatabaseDSN == "" {
		t.Fatal("the DSN must fall back to the packaged default")
	}
}

func TestRedactedHidesCredentials(t *testing.T) {
	cfg, err := config.Load(lookupFrom(map[string]string{
		"APP_DATABASE_DSN": "postgres://campus:s3cret@db.internal:5432/orientation?sslmode=require",
	}))
	if err != nil {
		t.Fatalf("loading failed: %v", err)
	}
	rendered := cfg.Redacted()
	dsn := rendered["database_dsn"]
	if strings.Contains(dsn, "s3cret") {
		t.Fatalf("the redacted DSN still contains the password: %q", dsn)
	}
	if strings.Contains(dsn, "sslmode=require") {
		t.Fatalf("the redacted DSN still contains the query string: %q", dsn)
	}
	if rendered["worker_enabled"] != "true" {
		t.Fatalf("worker_enabled = %q", rendered["worker_enabled"])
	}
}

func TestValidateIsCalledFromLoad(t *testing.T) {
	cfg := config.Config{HTTPAddr: ":8080", DatabaseDSN: "file:x", SessionTTL: time.Hour,
		WorkerPollInterval: time.Second, WorkerBackoffBase: time.Second, WorkerBackoffMax: time.Minute}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("a complete configuration must validate, got %v", err)
	}
	broken := cfg
	broken.DatabaseDSN = ""
	if err := broken.Validate(); err == nil {
		t.Fatal("a configuration without a DSN must be rejected")
	}
}
