package worker_test

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vance1852/orientation-enrollment-platform/internal/audit"
	"github.com/vance1852/orientation-enrollment-platform/internal/domain"
	"github.com/vance1852/orientation-enrollment-platform/internal/platform/clock"
	"github.com/vance1852/orientation-enrollment-platform/internal/platform/logging"
	"github.com/vance1852/orientation-enrollment-platform/internal/storage/sqlite"
	"github.com/vance1852/orientation-enrollment-platform/internal/worker"
)

type workerFixture struct {
	store *sqlite.Store
	clock *clock.Fixed
	audit *audit.Recorder
}

func newWorkerFixture(t *testing.T) workerFixture {
	t.Helper()
	ctx := context.Background()
	dsn := "file:" + filepath.ToSlash(filepath.Join(t.TempDir(), "worker-test.db"))
	store, err := sqlite.Open(ctx, dsn, sqlite.Options{MaxOpenConns: 4})
	if err != nil {
		t.Fatalf("opening the database failed: %v", err)
	}
	if _, err := sqlite.Migrate(ctx, store.DB()); err != nil {
		t.Fatalf("migrating failed: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	fixed := clock.NewFixed(time.Date(2026, time.August, 26, 10, 0, 0, 0, time.UTC))
	return workerFixture{store: store, clock: fixed,
		audit: audit.NewRecorder(func() time.Time { return fixed.Now() })}
}

func (f workerFixture) newWorker(t *testing.T) *worker.Worker {
	t.Helper()
	instance, err := worker.New(f.store, f.clock, f.audit, logging.Discard(), worker.Config{
		WorkerID:     "test-worker",
		PollInterval: 5 * time.Millisecond,
		Lease:        time.Minute,
		BackoffBase:  2 * time.Second,
		BackoffMax:   30 * time.Second,
	})
	if err != nil {
		t.Fatalf("building the worker failed: %v", err)
	}
	return instance
}

func (f workerFixture) enqueue(t *testing.T, kind domain.JobKind, payload string) domain.Job {
	t.Helper()
	now := f.clock.Now()
	job, err := f.store.Jobs().EnqueueJob(context.Background(), domain.Job{
		Kind: kind, Payload: payload, RunAfter: now, CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		t.Fatalf("enqueueing failed: %v", err)
	}
	return job
}

func TestWorkerExecutesAndClosesAJob(t *testing.T) {
	fixture := newWorkerFixture(t)
	instance := fixture.newWorker(t)
	var executed atomic.Int32

	if err := instance.Register(domain.JobPromoteWaitlist, func(ctx context.Context, job domain.Job) error {
		executed.Add(1)
		return nil
	}); err != nil {
		t.Fatalf("registering failed: %v", err)
	}
	if err := instance.Register(domain.JobPromoteWaitlist, func(context.Context, domain.Job) error { return nil }); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("registering the same kind twice must fail, got %v", err)
	}
	if len(instance.Kinds()) != 1 {
		t.Fatalf("kinds = %v", instance.Kinds())
	}

	job := fixture.enqueue(t, domain.JobPromoteWaitlist, `{"section_id":1}`)
	claimed, err := instance.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("running the job failed: %v", err)
	}
	if !claimed || executed.Load() != 1 {
		t.Fatalf("claimed = %v, executions = %d", claimed, executed.Load())
	}

	stored, err := fixture.store.Jobs().FindJobByID(context.Background(), job.ID)
	if err != nil {
		t.Fatalf("reading the job failed: %v", err)
	}
	if stored.State != domain.JobSucceeded || stored.Attempts != 1 || stored.LockedBy != "" {
		t.Fatalf("job after success = %+v", stored)
	}

	idle, err := instance.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("polling an empty queue failed: %v", err)
	}
	if idle {
		t.Fatal("an empty queue must report no work")
	}
}

func TestWorkerRetriesWithBackoffAndRetiresTheJob(t *testing.T) {
	fixture := newWorkerFixture(t)
	instance := fixture.newWorker(t)
	var attempts atomic.Int32
	failure := errors.New("promotion target locked")

	if err := instance.Register(domain.JobPromoteWaitlist, func(context.Context, domain.Job) error {
		attempts.Add(1)
		return failure
	}); err != nil {
		t.Fatalf("registering failed: %v", err)
	}
	job := fixture.enqueue(t, domain.JobPromoteWaitlist, `{"section_id":1}`)

	for i := 1; i <= domain.MaxJobAttempts; i++ {
		claimed, err := instance.RunOnce(context.Background())
		if err != nil {
			t.Fatalf("attempt %d failed: %v", i, err)
		}
		if !claimed {
			t.Fatalf("attempt %d found no job", i)
		}
		stored, err := fixture.store.Jobs().FindJobByID(context.Background(), job.ID)
		if err != nil {
			t.Fatalf("reading the job failed: %v", err)
		}
		if i < domain.MaxJobAttempts {
			if stored.State != domain.JobQueued {
				t.Fatalf("attempt %d left the job in %s", i, stored.State)
			}
			expectedDelay := domain.Backoff(i, 2*time.Second, 30*time.Second)
			if !stored.RunAfter.Equal(fixture.clock.Now().Add(expectedDelay)) {
				t.Fatalf("attempt %d scheduled at %s, want %s",
					i, stored.RunAfter, fixture.clock.Now().Add(expectedDelay))
			}
			if stored.LastError != failure.Error() {
				t.Fatalf("last error = %q", stored.LastError)
			}
			// Move past the backoff so the next attempt can claim the row.
			fixture.clock.Advance(expectedDelay + time.Second)
			continue
		}
		if stored.State != domain.JobPermanentlyFailed {
			t.Fatalf("the last attempt left the job in %s", stored.State)
		}
	}
	if attempts.Load() != int32(domain.MaxJobAttempts) {
		t.Fatalf("handler ran %d times, want %d", attempts.Load(), domain.MaxJobAttempts)
	}

	trail, err := fixture.store.Audit().ListAuditEvents(context.Background(), domain.AuditFilter{
		Action: string(domain.ActionJobFailed), Page: domain.Page{Size: 10}})
	if err != nil {
		t.Fatalf("reading the trail failed: %v", err)
	}
	if trail.Total != 1 {
		t.Fatalf("permanent failures audited = %d, want 1", trail.Total)
	}
	if trail.Items[0].Result != domain.ResultFailure {
		t.Fatalf("audit result = %s", trail.Items[0].Result)
	}

	if claimed, err := instance.RunOnce(context.Background()); err != nil || claimed {
		t.Fatalf("a retired job must not be retried: claimed=%v err=%v", claimed, err)
	}
}

func TestWorkerReturnsAJobToTheQueueOnCancellation(t *testing.T) {
	fixture := newWorkerFixture(t)
	instance := fixture.newWorker(t)
	ctx, cancel := context.WithCancel(context.Background())

	if err := instance.Register(domain.JobPromoteWaitlist, func(handlerCtx context.Context, job domain.Job) error {
		cancel()
		return handlerCtx.Err()
	}); err != nil {
		t.Fatalf("registering failed: %v", err)
	}
	job := fixture.enqueue(t, domain.JobPromoteWaitlist, `{"section_id":1}`)

	claimed, err := instance.RunOnce(ctx)
	if !claimed {
		t.Fatal("the job must have been claimed before the cancellation")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	stored, err := fixture.store.Jobs().FindJobByID(context.Background(), job.ID)
	if err != nil {
		t.Fatalf("reading the job failed: %v", err)
	}
	if stored.State != domain.JobQueued {
		t.Fatalf("a cancelled attempt must return the job to the queue, got %s", stored.State)
	}
	if stored.Attempts != 1 {
		t.Fatalf("attempts = %d, want 1", stored.Attempts)
	}
}

func TestWorkerRecoversJobsAbandonedByACrashedProcess(t *testing.T) {
	fixture := newWorkerFixture(t)
	instance := fixture.newWorker(t)
	if err := instance.Register(domain.JobSweepSessions, func(context.Context, domain.Job) error { return nil }); err != nil {
		t.Fatalf("registering failed: %v", err)
	}
	job := fixture.enqueue(t, domain.JobSweepSessions, "{}")

	// Simulate a crash: the row stays leased with an expired lease.
	if _, err := fixture.store.Jobs().ClaimNextJob(context.Background(),
		[]domain.JobKind{domain.JobSweepSessions}, "dead-worker", fixture.clock.Now()); err != nil {
		t.Fatalf("leasing the job failed: %v", err)
	}
	fixture.clock.Advance(2 * time.Minute)

	recovered, err := instance.RecoverStaleJobs(context.Background())
	if err != nil {
		t.Fatalf("recovery failed: %v", err)
	}
	if recovered != 1 {
		t.Fatalf("recovered = %d, want 1", recovered)
	}
	claimed, err := instance.RunOnce(context.Background())
	if err != nil || !claimed {
		t.Fatalf("the recovered job must run: claimed=%v err=%v", claimed, err)
	}
	stored, err := fixture.store.Jobs().FindJobByID(context.Background(), job.ID)
	if err != nil {
		t.Fatalf("reading the job failed: %v", err)
	}
	if stored.State != domain.JobSucceeded || stored.Attempts != 2 {
		t.Fatalf("job after recovery = %+v", stored)
	}
}

func TestWorkerRunStopsWhenTheContextIsCancelled(t *testing.T) {
	fixture := newWorkerFixture(t)
	instance := fixture.newWorker(t)
	var executed atomic.Int32
	if err := instance.Register(domain.JobSweepSessions, func(context.Context, domain.Job) error {
		executed.Add(1)
		return nil
	}); err != nil {
		t.Fatalf("registering failed: %v", err)
	}
	fixture.enqueue(t, domain.JobSweepSessions, "{}")

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	err := instance.Run(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Run must report why it stopped, got %v", err)
	}
	if executed.Load() == 0 {
		t.Fatal("the loop must have drained the queue before stopping")
	}
}

func TestWorkerWithoutHandlersRefusesToRun(t *testing.T) {
	fixture := newWorkerFixture(t)
	instance := fixture.newWorker(t)
	if _, err := instance.RunOnce(context.Background()); err == nil {
		t.Fatal("a worker without handlers must report a configuration error")
	}
	if err := instance.Register(domain.JobPromoteWaitlist, nil); !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("a nil handler must be rejected, got %v", err)
	}
	if _, err := worker.New(nil, fixture.clock, fixture.audit, nil, worker.Config{}); !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("a missing store must be rejected, got %v", err)
	}
}

// stubPromoter records the sections a promotion handler was asked to fill.
type stubPromoter struct {
	calls []int64
	err   error
}

func (s *stubPromoter) PromoteWaitlist(_ context.Context, sectionID int64) (bool, error) {
	s.calls = append(s.calls, sectionID)
	return s.err == nil, s.err
}

func TestWaitlistPromotionHandlerReadsThePersistedPayload(t *testing.T) {
	promoter := &stubPromoter{}
	handler := worker.NewWaitlistPromotionHandler(promoter)

	if err := handler(context.Background(), domain.Job{ID: 1, Payload: `{"section_id":42}`}); err != nil {
		t.Fatalf("handling failed: %v", err)
	}
	if len(promoter.calls) != 1 || promoter.calls[0] != 42 {
		t.Fatalf("promoter calls = %v", promoter.calls)
	}

	if err := handler(context.Background(), domain.Job{ID: 2, Payload: "not json"}); err == nil {
		t.Fatal("a malformed payload must fail the job")
	}
	if err := handler(context.Background(), domain.Job{ID: 3, Payload: `{"section_id":0}`}); !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("a payload without a section must be a validation error, got %v", err)
	}

	// A section that vanished while the job waited is not a failure.
	missing := &stubPromoter{err: fmt.Errorf("section 9: %w", domain.ErrNotFound)}
	if err := worker.NewWaitlistPromotionHandler(missing)(context.Background(),
		domain.Job{ID: 4, Payload: `{"section_id":9}`}); err != nil {
		t.Fatalf("a missing section must complete the job, got %v", err)
	}

	broken := &stubPromoter{err: errors.New("database offline")}
	if err := worker.NewWaitlistPromotionHandler(broken)(context.Background(),
		domain.Job{ID: 5, Payload: `{"section_id":9}`}); err == nil {
		t.Fatal("a real failure must be reported so the job retries")
	}
}

type stubSweeper struct {
	limit int
	err   error
}

func (s *stubSweeper) SweepExpiredSessions(_ context.Context, limit int) (int, error) {
	s.limit = limit
	return 3, s.err
}

func TestSessionSweepHandlerUsesTheConfiguredLimit(t *testing.T) {
	sweeper := &stubSweeper{}
	if err := worker.NewSessionSweepHandler(sweeper)(context.Background(), domain.Job{ID: 1}); err != nil {
		t.Fatalf("handling failed: %v", err)
	}
	if sweeper.limit != worker.SessionSweepLimit {
		t.Fatalf("limit = %d, want %d", sweeper.limit, worker.SessionSweepLimit)
	}
	failing := &stubSweeper{err: errors.New("database offline")}
	if err := worker.NewSessionSweepHandler(failing)(context.Background(), domain.Job{ID: 2}); err == nil {
		t.Fatal("a sweep failure must be reported")
	}
}

func TestMaintenanceOnlyEnqueuesWhenTheQueueIsIdle(t *testing.T) {
	fixture := newWorkerFixture(t)
	maintenance, err := worker.NewMaintenance(fixture.store, time.Minute)
	if err != nil {
		t.Fatalf("building maintenance failed: %v", err)
	}
	ctx := context.Background()

	enqueued, err := maintenance.EnsureSessionSweep(ctx, fixture.clock.Now())
	if err != nil {
		t.Fatalf("scheduling failed: %v", err)
	}
	if !enqueued {
		t.Fatal("an idle queue must receive a sweep job")
	}
	again, err := maintenance.EnsureSessionSweep(ctx, fixture.clock.Now())
	if err != nil {
		t.Fatalf("scheduling failed: %v", err)
	}
	if again {
		t.Fatal("a pending job must not be duplicated")
	}
	queued, err := fixture.store.Jobs().CountJobsByState(ctx, domain.JobQueued)
	if err != nil {
		t.Fatalf("counting failed: %v", err)
	}
	if queued != 1 {
		t.Fatalf("queued jobs = %d, want 1", queued)
	}

	if _, err := worker.NewMaintenance(nil, time.Minute); !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("a missing store must be rejected, got %v", err)
	}
}
