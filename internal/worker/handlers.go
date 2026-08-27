package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/vance1852/orientation-enrollment-platform/internal/domain"
	"github.com/vance1852/orientation-enrollment-platform/internal/repository"
	"github.com/vance1852/orientation-enrollment-platform/internal/service"
)

// SessionSweepLimit bounds how many sessions one sweep attempt revokes.
const SessionSweepLimit = 200

// WaitlistPromoter is the subset of the enrollment service the promotion handler
// needs, declared here so the worker package does not depend on the concrete
// service type in tests.
type WaitlistPromoter interface {
	PromoteWaitlist(ctx context.Context, sectionID int64) (bool, error)
}

// SessionSweeper revokes sessions whose lifetime elapsed.
type SessionSweeper interface {
	SweepExpiredSessions(ctx context.Context, limit int) (int, error)
}

// NewWaitlistPromotionHandler builds the handler for domain.JobPromoteWaitlist.
//
// A section that no longer has a free seat is a normal outcome, not a failure:
// the handler completes so the job is not retried forever.
func NewWaitlistPromotionHandler(promoter WaitlistPromoter) Handler {
	return func(ctx context.Context, job domain.Job) error {
		var payload service.WaitlistJobPayload
		if err := json.Unmarshal([]byte(job.Payload), &payload); err != nil {
			return fmt.Errorf("decode waitlist job %d payload: %w", job.ID, err)
		}
		if payload.SectionID <= 0 {
			return fmt.Errorf("waitlist job %d has no section: %w", job.ID, domain.ErrValidation)
		}
		promoted, err := promoter.PromoteWaitlist(ctx, payload.SectionID)
		if err != nil {
			if errors.Is(err, domain.ErrNotFound) {
				// The section was removed while the job was queued.
				return nil
			}
			return err
		}
		_ = promoted
		return nil
	}
}

// NewSessionSweepHandler builds the handler for domain.JobSweepSessions.
func NewSessionSweepHandler(sweeper SessionSweeper) Handler {
	return func(ctx context.Context, job domain.Job) error {
		if _, err := sweeper.SweepExpiredSessions(ctx, SessionSweepLimit); err != nil {
			return fmt.Errorf("sweep expired sessions for job %d: %w", job.ID, err)
		}
		return nil
	}
}

// Maintenance keeps the recurring session sweep in the queue. Enqueuing only
// when nothing is pending stops a long outage from producing a backlog of
// identical jobs.
type Maintenance struct {
	store    repository.Store
	interval time.Duration
}

// NewMaintenance builds the recurring job scheduler.
func NewMaintenance(store repository.Store, interval time.Duration) (*Maintenance, error) {
	if store == nil {
		return nil, domain.NewFieldError("maintenance.store", "must not be nil")
	}
	if interval <= 0 {
		interval = 5 * time.Minute
	}
	return &Maintenance{store: store, interval: interval}, nil
}

// EnsureSessionSweep enqueues a sweep job when none is waiting.
func (m *Maintenance) EnsureSessionSweep(ctx context.Context, now time.Time) (bool, error) {
	queued, err := m.store.Jobs().CountJobsByState(ctx, domain.JobQueued)
	if err != nil {
		return false, err
	}
	if queued > 0 {
		return false, nil
	}
	enqueued := false
	err = m.store.InTx(ctx, func(ctx context.Context, tx repository.Repositories) error {
		if _, err := tx.Jobs().EnqueueJob(ctx, domain.Job{
			Kind:        domain.JobSweepSessions,
			Payload:     "{}",
			State:       domain.JobQueued,
			MaxAttempts: domain.MaxJobAttempts,
			RunAfter:    now.Add(m.interval),
			CreatedAt:   now,
			UpdatedAt:   now,
		}); err != nil {
			return err
		}
		enqueued = true
		return nil
	})
	if err != nil {
		return false, err
	}
	return enqueued, nil
}

// Run schedules the recurring sweep until the context is cancelled.
func (m *Maintenance) Run(ctx context.Context, now func() time.Time) error {
	ticker := time.NewTicker(m.interval)
	defer ticker.Stop()
	for {
		if _, err := m.EnsureSessionSweep(ctx, now()); err != nil {
			if errors.Is(err, context.Canceled) {
				return ctx.Err()
			}
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
