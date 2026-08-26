package domain

import (
	"math"
	"time"
)

// JobKind names a background task type.
type JobKind string

// Background job kinds.
const (
	// JobPromoteWaitlist fills a freed seat from the section waitlist.
	JobPromoteWaitlist JobKind = "waitlist.promote"
	// JobSweepSessions revokes sessions whose lifetime elapsed.
	JobSweepSessions JobKind = "session.sweep"
)

// JobState is the persisted worker state machine.
type JobState string

// Job states.
const (
	JobQueued            JobState = "queued"
	JobRunning           JobState = "running"
	JobSucceeded         JobState = "succeeded"
	JobPermanentlyFailed JobState = "permanently_failed"
)

// MaxJobAttempts bounds the retry budget of every job.
const MaxJobAttempts = 5

// Job is a durable unit of background work. Payload keeps the business key so a
// restarted process can resume without in-memory state.
type Job struct {
	ID          int64
	Kind        JobKind
	Payload     string
	State       JobState
	Attempts    int
	MaxAttempts int
	LastError   string
	RunAfter    time.Time
	LockedAt    *time.Time
	LockedBy    string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// Attemptable reports whether the job may be picked up at the given instant.
func (j Job) Attemptable(now time.Time) bool {
	if j.State != JobQueued {
		return false
	}
	return !now.Before(j.RunAfter)
}

// BudgetExhausted reports whether the retry budget is spent.
func (j Job) BudgetExhausted() bool {
	limit := j.MaxAttempts
	if limit <= 0 {
		limit = MaxJobAttempts
	}
	return j.Attempts >= limit
}

// Backoff returns the delay before the next attempt of a job. The schedule is
// exponential and capped so a poisoned job cannot starve the queue.
func Backoff(attempt int, base, max time.Duration) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	if base <= 0 {
		base = time.Second
	}
	if max <= 0 || max < base {
		max = base
	}
	shift := attempt - 1
	if shift > 16 {
		shift = 16
	}
	scaled := float64(base) * math.Pow(2, float64(shift))
	if scaled > float64(max) {
		return max
	}
	return time.Duration(scaled)
}

// StaleLockCutoff returns the instant before which a running job is considered
// abandoned by a crashed worker and must be requeued on startup.
func StaleLockCutoff(now time.Time, lease time.Duration) time.Time {
	if lease <= 0 {
		lease = time.Minute
	}
	return now.Add(-lease)
}
