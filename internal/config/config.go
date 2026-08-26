// Package config loads runtime settings from the environment. No credential is
// ever compiled into the binary or committed to the repository.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config is the fully resolved runtime configuration.
type Config struct {
	HTTPAddr             string
	DatabaseDSN          string
	LogLevel             string
	SessionTTL           time.Duration
	ShutdownGrace        time.Duration
	RequestTimeout       time.Duration
	WorkerPollInterval   time.Duration
	WorkerLease          time.Duration
	WorkerBackoffBase    time.Duration
	WorkerBackoffMax     time.Duration
	WorkerEnabled        bool
	SeedDemoData         bool
	BusinessLocation     string
	MaxEnrollmentRetries int
}

// Load reads configuration from the process environment and applies defaults.
func Load(lookup func(string) (string, bool)) (Config, error) {
	if lookup == nil {
		lookup = os.LookupEnv
	}
	cfg := Config{
		HTTPAddr:             stringVal(lookup, "APP_HTTP_ADDR", ":8080"),
		DatabaseDSN:          stringVal(lookup, "APP_DATABASE_DSN", "file:data/orientation.db?_pragma=busy_timeout(5000)"),
		LogLevel:             stringVal(lookup, "APP_LOG_LEVEL", "info"),
		BusinessLocation:     stringVal(lookup, "APP_BUSINESS_TZ", "Asia/Shanghai"),
		MaxEnrollmentRetries: 4,
	}

	var err error
	if cfg.SessionTTL, err = durationVal(lookup, "APP_SESSION_TTL", 12*time.Hour); err != nil {
		return Config{}, err
	}
	if cfg.ShutdownGrace, err = durationVal(lookup, "APP_SHUTDOWN_GRACE", 15*time.Second); err != nil {
		return Config{}, err
	}
	if cfg.RequestTimeout, err = durationVal(lookup, "APP_REQUEST_TIMEOUT", 20*time.Second); err != nil {
		return Config{}, err
	}
	if cfg.WorkerPollInterval, err = durationVal(lookup, "APP_WORKER_POLL_INTERVAL", 2*time.Second); err != nil {
		return Config{}, err
	}
	if cfg.WorkerLease, err = durationVal(lookup, "APP_WORKER_LEASE", time.Minute); err != nil {
		return Config{}, err
	}
	if cfg.WorkerBackoffBase, err = durationVal(lookup, "APP_WORKER_BACKOFF_BASE", 2*time.Second); err != nil {
		return Config{}, err
	}
	if cfg.WorkerBackoffMax, err = durationVal(lookup, "APP_WORKER_BACKOFF_MAX", 5*time.Minute); err != nil {
		return Config{}, err
	}
	if cfg.WorkerEnabled, err = boolVal(lookup, "APP_WORKER_ENABLED", true); err != nil {
		return Config{}, err
	}
	if cfg.SeedDemoData, err = boolVal(lookup, "APP_SEED_DEMO_DATA", true); err != nil {
		return Config{}, err
	}
	if retries, ok := lookup("APP_MAX_ENROLLMENT_RETRIES"); ok {
		parsed, convErr := strconv.Atoi(strings.TrimSpace(retries))
		if convErr != nil || parsed < 0 || parsed > 32 {
			return Config{}, fmt.Errorf("APP_MAX_ENROLLMENT_RETRIES must be an integer between 0 and 32")
		}
		cfg.MaxEnrollmentRetries = parsed
	}
	return cfg, cfg.Validate()
}

// Validate rejects configurations that cannot produce a working server.
func (c Config) Validate() error {
	if strings.TrimSpace(c.HTTPAddr) == "" {
		return fmt.Errorf("APP_HTTP_ADDR must not be empty")
	}
	if strings.TrimSpace(c.DatabaseDSN) == "" {
		return fmt.Errorf("APP_DATABASE_DSN must not be empty")
	}
	if c.SessionTTL < time.Minute {
		return fmt.Errorf("APP_SESSION_TTL must be at least one minute")
	}
	if c.WorkerPollInterval < 10*time.Millisecond {
		return fmt.Errorf("APP_WORKER_POLL_INTERVAL must be at least 10ms")
	}
	if c.WorkerBackoffMax < c.WorkerBackoffBase {
		return fmt.Errorf("APP_WORKER_BACKOFF_MAX must not be smaller than APP_WORKER_BACKOFF_BASE")
	}
	return nil
}

// Redacted renders the configuration for logs without exposing the DSN query
// string, which may carry a password on non-SQLite deployments.
func (c Config) Redacted() map[string]string {
	dsn := c.DatabaseDSN
	if idx := strings.IndexByte(dsn, '?'); idx >= 0 {
		dsn = dsn[:idx] + "?<redacted>"
	}
	if at := strings.LastIndexByte(dsn, '@'); at >= 0 {
		dsn = "<redacted>@" + dsn[at+1:]
	}
	return map[string]string{
		"http_addr":            c.HTTPAddr,
		"database_dsn":         dsn,
		"log_level":            c.LogLevel,
		"session_ttl":          c.SessionTTL.String(),
		"worker_enabled":       strconv.FormatBool(c.WorkerEnabled),
		"worker_poll_interval": c.WorkerPollInterval.String(),
		"business_tz":          c.BusinessLocation,
	}
}

func stringVal(lookup func(string) (string, bool), key, fallback string) string {
	if raw, ok := lookup(key); ok {
		if trimmed := strings.TrimSpace(raw); trimmed != "" {
			return trimmed
		}
	}
	return fallback
}

func durationVal(lookup func(string) (string, bool), key string, fallback time.Duration) (time.Duration, error) {
	raw, ok := lookup(key)
	if !ok || strings.TrimSpace(raw) == "" {
		return fallback, nil
	}
	parsed, err := time.ParseDuration(strings.TrimSpace(raw))
	if err != nil {
		return 0, fmt.Errorf("%s must be a Go duration such as 30s: %w", key, err)
	}
	if parsed <= 0 {
		return 0, fmt.Errorf("%s must be positive", key)
	}
	return parsed, nil
}

func boolVal(lookup func(string) (string, bool), key string, fallback bool) (bool, error) {
	raw, ok := lookup(key)
	if !ok || strings.TrimSpace(raw) == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseBool(strings.TrimSpace(raw))
	if err != nil {
		return false, fmt.Errorf("%s must be a boolean: %w", key, err)
	}
	return parsed, nil
}
