package app_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/vance1852/orientation-enrollment-platform/internal/app"
	"github.com/vance1852/orientation-enrollment-platform/internal/config"
	"github.com/vance1852/orientation-enrollment-platform/internal/domain"
	"github.com/vance1852/orientation-enrollment-platform/internal/platform/logging"
)

func configFor(t *testing.T, dsn string, extra map[string]string) config.Config {
	t.Helper()
	t.Setenv("APP_DATABASE_DSN", dsn)
	t.Setenv("APP_SEED_DEMO_DATA", "true")
	t.Setenv("APP_WORKER_ENABLED", "false")
	for key, value := range extra {
		t.Setenv(key, value)
	}
	cfg, err := config.Load(os.LookupEnv)
	if err != nil {
		t.Fatalf("loading the configuration failed: %v", err)
	}
	return cfg
}

func TestBuildIsRepeatableOnTheSameDatabase(t *testing.T) {
	ctx := context.Background()
	dsn := "file:" + filepath.ToSlash(filepath.Join(t.TempDir(), "app-test.db"))
	cfg := configFor(t, dsn, nil)

	first, err := app.Build(ctx, cfg, logging.Discard())
	if err != nil {
		t.Fatalf("the first build failed: %v", err)
	}
	sections, err := first.Store().Catalog().ListSections(ctx, domain.SectionFilter{Page: domain.Page{Size: 50}})
	if err != nil {
		t.Fatalf("listing sections failed: %v", err)
	}
	if sections.Total == 0 {
		t.Fatal("the first build must seed the catalogue")
	}
	if err := first.Close(); err != nil {
		t.Fatalf("closing failed: %v", err)
	}

	// Restarting the process must not duplicate reference data and must not
	// reapply migrations.
	second, err := app.Build(ctx, cfg, logging.Discard())
	if err != nil {
		t.Fatalf("the second build failed: %v", err)
	}
	defer func() { _ = second.Close() }()

	after, err := second.Store().Catalog().ListSections(ctx, domain.SectionFilter{Page: domain.Page{Size: 50}})
	if err != nil {
		t.Fatalf("listing sections failed: %v", err)
	}
	if after.Total != sections.Total {
		t.Fatalf("sections after the restart = %d, want %d", after.Total, sections.Total)
	}
	users, err := second.Store().Users().FindUserByEmail(ctx, "student@campus.example")
	if err != nil {
		t.Fatalf("the seeded student is missing after the restart: %v", err)
	}
	if users.Role != domain.RoleStudent {
		t.Fatalf("seeded role = %s", users.Role)
	}
}

func TestBuildCreatesTheDatabaseDirectory(t *testing.T) {
	ctx := context.Background()
	nested := filepath.Join(t.TempDir(), "nested", "state")
	dsn := "file:" + filepath.ToSlash(filepath.Join(nested, "app.db")) + "?_pragma=busy_timeout(5000)"
	cfg := configFor(t, dsn, nil)

	instance, err := app.Build(ctx, cfg, logging.Discard())
	if err != nil {
		t.Fatalf("build failed: %v", err)
	}
	defer func() { _ = instance.Close() }()

	if _, err := os.Stat(nested); err != nil {
		t.Fatalf("the database directory was not created: %v", err)
	}
}

func TestBuildRejectsAnInvalidConfiguration(t *testing.T) {
	if _, err := app.Build(context.Background(), config.Config{}, logging.Discard()); err == nil {
		t.Fatal("an empty configuration must be rejected")
	}
}

func TestRunServesTrafficAndShutsDownGracefully(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserving a port failed: %v", err)
	}
	addr := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatalf("releasing the port failed: %v", err)
	}

	dsn := "file:" + filepath.ToSlash(filepath.Join(t.TempDir(), "run-test.db"))
	cfg := configFor(t, dsn, map[string]string{
		"APP_HTTP_ADDR":            addr,
		"APP_WORKER_ENABLED":       "true",
		"APP_WORKER_POLL_INTERVAL": "50ms",
		"APP_SHUTDOWN_GRACE":       "3s",
	})

	instance, err := app.Build(ctx, cfg, logging.Discard())
	if err != nil {
		t.Fatalf("build failed: %v", err)
	}
	defer func() { _ = instance.Close() }()

	done := make(chan error, 1)
	go func() { done <- instance.Run(ctx) }()

	client := &http.Client{Timeout: 2 * time.Second}
	var ready bool
	for attempt := 0; attempt < 60; attempt++ {
		res, reqErr := client.Get("http://" + addr + "/readyz")
		if reqErr == nil {
			var payload struct {
				Status        string `json:"status"`
				SchemaVersion int    `json:"schema_version"`
			}
			decodeErr := json.NewDecoder(res.Body).Decode(&payload)
			_ = res.Body.Close()
			if decodeErr != nil {
				t.Fatalf("decoding the readiness payload failed: %v", decodeErr)
			}
			if res.StatusCode != http.StatusOK || payload.Status != "ready" {
				t.Fatalf("readiness = %d %+v", res.StatusCode, payload)
			}
			if payload.SchemaVersion < 4 {
				t.Fatalf("schema version = %d", payload.SchemaVersion)
			}
			ready = true
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !ready {
		t.Fatal("the server never became ready")
	}

	live, err := client.Get("http://" + addr + "/healthz")
	if err != nil {
		t.Fatalf("liveness request failed: %v", err)
	}
	_ = live.Body.Close()
	if live.StatusCode != http.StatusOK {
		t.Fatalf("liveness = %d", live.StatusCode)
	}

	cancel()
	select {
	case runErr := <-done:
		if runErr != nil && !errors.Is(runErr, context.Canceled) {
			t.Fatalf("Run returned %v", runErr)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return after the context was cancelled")
	}

	if _, err := client.Get(fmt.Sprintf("http://%s/healthz", addr)); err == nil {
		t.Fatal("the listener must be closed after the shutdown")
	}
}
