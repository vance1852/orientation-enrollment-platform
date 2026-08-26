// Package app wires configuration, persistence, services, HTTP and the
// background worker into one runnable unit.
package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/vance1852/orientation-enrollment-platform/internal/audit"
	"github.com/vance1852/orientation-enrollment-platform/internal/config"
	"github.com/vance1852/orientation-enrollment-platform/internal/domain"
	"github.com/vance1852/orientation-enrollment-platform/internal/httpapi"
	"github.com/vance1852/orientation-enrollment-platform/internal/middleware"
	"github.com/vance1852/orientation-enrollment-platform/internal/migrations"
	"github.com/vance1852/orientation-enrollment-platform/internal/platform/clock"
	"github.com/vance1852/orientation-enrollment-platform/internal/platform/ids"
	"github.com/vance1852/orientation-enrollment-platform/internal/service"
	"github.com/vance1852/orientation-enrollment-platform/internal/storage/sqlite"
	"github.com/vance1852/orientation-enrollment-platform/internal/worker"
)

// App is an assembled process.
type App struct {
	cfg         config.Config
	logger      *slog.Logger
	store       *sqlite.Store
	handler     http.Handler
	worker      *worker.Worker
	maintenance *worker.Maintenance
	clock       clock.Clock
	location    *time.Location
}

// Build opens the database, migrates it, seeds reference data and assembles all
// layers. It returns an error instead of calling os.Exit so tests can reuse it.
func Build(ctx context.Context, cfg config.Config, logger *slog.Logger) (*App, error) {
	if logger == nil {
		logger = slog.Default()
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if err := ensureDatabaseDirectory(cfg.DatabaseDSN); err != nil {
		return nil, err
	}

	store, err := sqlite.Open(ctx, cfg.DatabaseDSN, sqlite.Options{MaxOpenConns: 8})
	if err != nil {
		return nil, err
	}
	outcome, err := sqlite.Migrate(ctx, store.DB())
	if err != nil {
		_ = store.Close()
		return nil, err
	}
	logger.Info("schema ready",
		"version", outcome.Version, "applied", outcome.Applied, "already_current", outcome.AlreadyCurrent)

	expectedSchema, err := migrations.LatestVersion()
	if err != nil {
		_ = store.Close()
		return nil, err
	}

	location := clock.BusinessLocation(cfg.BusinessLocation)
	systemClock := clock.System{}
	recorder := audit.NewRecorder(func() time.Time { return systemClock.Now() })

	if cfg.SeedDemoData {
		seeded, seedErr := sqlite.Seed(ctx, store, sqlite.SeedSpec{
			Now:              systemClock.Now(),
			RegistrarEmail:   envOr("APP_SEED_REGISTRAR_EMAIL", "registrar@campus.example"),
			RegistrarPass:    envOr("APP_SEED_REGISTRAR_PASSWORD", "orientation-registrar-2026"),
			StudentEmail:     envOr("APP_SEED_STUDENT_EMAIL", "student@campus.example"),
			StudentPass:      envOr("APP_SEED_STUDENT_PASSWORD", "orientation-student-2026"),
			TermCode:         envOr("APP_SEED_TERM_CODE", "2026-autumn"),
			BusinessLocation: location,
		})
		if seedErr != nil {
			_ = store.Close()
			return nil, seedErr
		}
		logger.Info("reference data",
			"skipped", seeded.Skipped, "term_id", seeded.TermID, "sections", len(seeded.SectionIDs))
	}

	deps := service.Deps{Store: store, Clock: systemClock, Audit: recorder, Logger: logger}
	authService, err := service.NewAuthService(deps, cfg.SessionTTL)
	if err != nil {
		_ = store.Close()
		return nil, err
	}
	catalogService, err := service.NewCatalogService(deps)
	if err != nil {
		_ = store.Close()
		return nil, err
	}
	registrationService, err := service.NewRegistrationService(deps)
	if err != nil {
		_ = store.Close()
		return nil, err
	}
	enrollmentService, err := service.NewEnrollmentService(deps, cfg.MaxEnrollmentRetries)
	if err != nil {
		_ = store.Close()
		return nil, err
	}
	idempotencyService, err := service.NewIdempotencyService(deps)
	if err != nil {
		_ = store.Close()
		return nil, err
	}

	api, err := httpapi.New(httpapi.Services{
		Auth:          authService,
		Catalog:       catalogService,
		Registrations: registrationService,
		Enrollments:   enrollmentService,
		Idempotency:   idempotencyService,
	}, store, logger, expectedSchema)
	if err != nil {
		_ = store.Close()
		return nil, err
	}

	handler := middleware.Chain(api.Routes(),
		middleware.RequestID(),
		middleware.Recover(logger),
		middleware.AccessLog(logger),
		middleware.Timeout(cfg.RequestTimeout),
	)

	app := &App{
		cfg:      cfg,
		logger:   logger,
		store:    store,
		handler:  handler,
		clock:    systemClock,
		location: location,
	}

	if cfg.WorkerEnabled {
		workerID, err := ids.NewWorkerID("orientation")
		if err != nil {
			_ = store.Close()
			return nil, err
		}
		bg, err := worker.New(store, systemClock, recorder, logger, worker.Config{
			WorkerID:     workerID,
			PollInterval: cfg.WorkerPollInterval,
			Lease:        cfg.WorkerLease,
			BackoffBase:  cfg.WorkerBackoffBase,
			BackoffMax:   cfg.WorkerBackoffMax,
		})
		if err != nil {
			_ = store.Close()
			return nil, err
		}
		if err := bg.Register(domain.JobPromoteWaitlist, worker.NewWaitlistPromotionHandler(enrollmentService)); err != nil {
			_ = store.Close()
			return nil, err
		}
		if err := bg.Register(domain.JobSweepSessions, worker.NewSessionSweepHandler(authService)); err != nil {
			_ = store.Close()
			return nil, err
		}
		maintenance, err := worker.NewMaintenance(store, 5*time.Minute)
		if err != nil {
			_ = store.Close()
			return nil, err
		}
		app.worker = bg
		app.maintenance = maintenance
	}
	return app, nil
}

// Handler exposes the assembled HTTP handler, which the HTTP tests exercise.
func (a *App) Handler() http.Handler { return a.handler }

// Store exposes the persistence handle for operational tooling and tests.
func (a *App) Store() *sqlite.Store { return a.store }

// Close releases the database pool.
func (a *App) Close() error { return a.store.Close() }

// Run serves HTTP and drives the background worker until the context is
// cancelled, then shuts down within the configured grace period.
func (a *App) Run(ctx context.Context) error {
	server := &http.Server{
		Addr:              a.cfg.HTTPAddr,
		Handler:           a.handler,
		ReadHeaderTimeout: 10 * time.Second,
	}

	var wg sync.WaitGroup
	errCh := make(chan error, 3)

	wg.Add(1)
	go func() {
		defer wg.Done()
		a.logger.Info("http server listening", "addr", a.cfg.HTTPAddr)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- fmt.Errorf("http server: %w", err)
		}
	}()

	if a.worker != nil {
		wg.Add(2)
		go func() {
			defer wg.Done()
			if err := a.worker.Run(ctx); err != nil && !isShutdown(err) {
				errCh <- fmt.Errorf("worker: %w", err)
			}
		}()
		go func() {
			defer wg.Done()
			if err := a.maintenance.Run(ctx, func() time.Time { return a.clock.Now() }); err != nil && !isShutdown(err) {
				errCh <- fmt.Errorf("maintenance: %w", err)
			}
		}()
	}

	var runErr error
	select {
	case <-ctx.Done():
		a.logger.Info("shutdown signal received")
	case runErr = <-errCh:
		a.logger.Error("component failed", "error", runErr.Error())
	}

	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), a.cfg.ShutdownGrace)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		runErr = errors.Join(runErr, fmt.Errorf("http shutdown: %w", err))
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if !isShutdown(err) {
			runErr = errors.Join(runErr, err)
		}
	}
	return runErr
}

func isShutdown(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, http.ErrServerClosed)
}

// ensureDatabaseDirectory creates the parent directory of a file backed DSN so
// the first start-up in a fresh container does not fail on a missing folder.
func ensureDatabaseDirectory(dsn string) error {
	path := strings.TrimPrefix(dsn, "file:")
	if idx := strings.IndexByte(path, '?'); idx >= 0 {
		path = path[:idx]
	}
	path = strings.TrimSpace(path)
	if path == "" || strings.HasPrefix(path, ":memory:") {
		return nil
	}
	dir := filepath.Dir(path)
	if dir == "" || dir == "." {
		return nil
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("create database directory %s: %w", dir, err)
	}
	return nil
}

func envOr(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}
