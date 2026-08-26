// Package worker runs the durable background queue. Jobs live in the database,
// so a restarted process resumes the work a crashed one abandoned.
package worker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/vance1852/orientation-enrollment-platform/internal/audit"
	"github.com/vance1852/orientation-enrollment-platform/internal/domain"
	"github.com/vance1852/orientation-enrollment-platform/internal/platform/clock"
	"github.com/vance1852/orientation-enrollment-platform/internal/platform/logging"
	"github.com/vance1852/orientation-enrollment-platform/internal/repository"
)

// Handler executes one job. Returning an error schedules a retry until the
// retry budget of the job is spent.
type Handler func(ctx context.Context, job domain.Job) error

// Config tunes the polling loop and the retry schedule.
type Config struct {
	WorkerID     string
	PollInterval time.Duration
	Lease        time.Duration
	BackoffBase  time.Duration
	BackoffMax   time.Duration
}

// Worker claims and executes queued jobs.
type Worker struct {
	store    repository.Store
	clock    clock.Clock
	audit    *audit.Recorder
	logger   *slog.Logger
	cfg      Config
	handlers map[domain.JobKind]Handler
}

// New builds a worker. Handlers are registered separately so the wiring layer
// controls which kinds this process is responsible for.
func New(store repository.Store, clk clock.Clock, recorder *audit.Recorder, logger *slog.Logger, cfg Config) (*Worker, error) {
	if store == nil {
		return nil, domain.NewFieldError("worker.store", "must not be nil")
	}
	if clk == nil {
		return nil, domain.NewFieldError("worker.clock", "must not be nil")
	}
	if recorder == nil {
		return nil, domain.NewFieldError("worker.audit", "must not be nil")
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = time.Second
	}
	if cfg.Lease <= 0 {
		cfg.Lease = time.Minute
	}
	if cfg.BackoffBase <= 0 {
		cfg.BackoffBase = time.Second
	}
	if cfg.BackoffMax < cfg.BackoffBase {
		cfg.BackoffMax = cfg.BackoffBase
	}
	if cfg.WorkerID == "" {
		cfg.WorkerID = "worker"
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Worker{
		store:    store,
		clock:    clk,
		audit:    recorder,
		logger:   logger,
		cfg:      cfg,
		handlers: make(map[domain.JobKind]Handler),
	}, nil
}

// Register binds a handler to a job kind.
func (w *Worker) Register(kind domain.JobKind, handler Handler) error {
	if handler == nil {
		return domain.NewFieldError("handler", "must not be nil")
	}
	if _, exists := w.handlers[kind]; exists {
		return fmt.Errorf("handler for %s already registered: %w", kind, domain.ErrConflict)
	}
	w.handlers[kind] = handler
	return nil
}

// Kinds returns the registered job kinds in a stable order.
func (w *Worker) Kinds() []domain.JobKind {
	kinds := make([]domain.JobKind, 0, len(w.handlers))
	for _, kind := range []domain.JobKind{domain.JobPromoteWaitlist, domain.JobSweepSessions} {
		if _, ok := w.handlers[kind]; ok {
			kinds = append(kinds, kind)
		}
	}
	return kinds
}

// RecoverStaleJobs requeues work whose lease expired, which is exactly the work
// a previous process was running when it stopped.
func (w *Worker) RecoverStaleJobs(ctx context.Context) (int, error) {
	now := w.clock.Now().UTC()
	count, err := w.store.Jobs().RequeueStaleJobs(ctx, domain.StaleLockCutoff(now, w.cfg.Lease), now)
	if err != nil {
		return 0, err
	}
	if count > 0 {
		w.logger.Info("requeued abandoned jobs", "count", count, "worker_id", w.cfg.WorkerID)
	}
	return count, nil
}

// RunOnce claims and processes at most one job. It reports whether a job was
// picked up, which lets the polling loop sleep only when the queue is idle.
func (w *Worker) RunOnce(ctx context.Context) (bool, error) {
	kinds := w.Kinds()
	if len(kinds) == 0 {
		return false, fmt.Errorf("worker has no registered handlers")
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	now := w.clock.Now().UTC()
	job, err := w.store.Jobs().ClaimNextJob(ctx, kinds, w.cfg.WorkerID, now)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return false, nil
		}
		return false, err
	}

	handler, ok := w.handlers[job.Kind]
	if !ok {
		failure := fmt.Sprintf("no handler registered for kind %s", job.Kind)
		if markErr := w.store.Jobs().MarkJobPermanentlyFailed(ctx, job.ID, failure, w.clock.Now().UTC()); markErr != nil {
			return true, markErr
		}
		return true, w.recordPermanentFailure(ctx, job, errors.New(failure))
	}

	handlerErr := handler(ctx, job)
	completedAt := w.clock.Now().UTC()
	if handlerErr == nil {
		if err := w.store.Jobs().MarkJobSucceeded(ctx, job.ID, completedAt); err != nil {
			return true, err
		}
		logging.FromContext(ctx, w.logger).Debug("job succeeded",
			"job_id", job.ID, "kind", string(job.Kind), "attempts", job.Attempts)
		return true, nil
	}

	// A cancelled context is an orderly shutdown, not a job failure: the job
	// returns to the queue immediately so the next process can pick it up.
	if errors.Is(handlerErr, context.Canceled) || errors.Is(handlerErr, context.DeadlineExceeded) {
		releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if err := w.store.Jobs().MarkJobRetry(releaseCtx, job.ID, completedAt, handlerErr.Error(), completedAt); err != nil {
			return true, errors.Join(handlerErr, err)
		}
		return true, handlerErr
	}

	if job.BudgetExhausted() {
		if err := w.store.Jobs().MarkJobPermanentlyFailed(ctx, job.ID, handlerErr.Error(), completedAt); err != nil {
			return true, errors.Join(handlerErr, err)
		}
		if err := w.recordPermanentFailure(ctx, job, handlerErr); err != nil {
			return true, err
		}
		w.logger.Warn("job permanently failed",
			"job_id", job.ID, "kind", string(job.Kind), "attempts", job.Attempts, "error", handlerErr.Error())
		return true, nil
	}

	delay := domain.Backoff(job.Attempts, w.cfg.BackoffBase, w.cfg.BackoffMax)
	if err := w.store.Jobs().MarkJobRetry(ctx, job.ID, completedAt.Add(delay), handlerErr.Error(), completedAt); err != nil {
		return true, errors.Join(handlerErr, err)
	}
	w.logger.Info("job scheduled for retry",
		"job_id", job.ID, "kind", string(job.Kind), "attempts", job.Attempts, "retry_in", delay.String())
	return true, nil
}

// Run polls until the context is cancelled. It always returns the reason it
// stopped so the caller can distinguish a shutdown from a fault.
func (w *Worker) Run(ctx context.Context) error {
	if _, err := w.RecoverStaleJobs(ctx); err != nil {
		return err
	}
	ticker := time.NewTicker(w.cfg.PollInterval)
	defer ticker.Stop()

	for {
		for {
			claimed, err := w.RunOnce(ctx)
			if err != nil {
				if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
					return ctx.Err()
				}
				w.logger.Error("worker iteration failed", "error", err.Error())
				break
			}
			if !claimed {
				break
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (w *Worker) recordPermanentFailure(ctx context.Context, job domain.Job, cause error) error {
	return w.store.InTx(ctx, func(ctx context.Context, tx repository.Repositories) error {
		return w.audit.Record(ctx, tx.Audit(), audit.Entry{
			Action:     domain.ActionJobFailed,
			ObjectType: "job",
			ObjectID:   fmt.Sprintf("%d", job.ID),
			Result:     domain.ResultFailure,
			Detail:     fmt.Sprintf("%s after %d attempts: %s", job.Kind, job.Attempts, cause.Error()),
		})
	})
}
