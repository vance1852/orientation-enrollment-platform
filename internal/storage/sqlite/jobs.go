package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/vance1852/orientation-enrollment-platform/internal/domain"
)

const jobColumns = `id, kind, payload, state, attempts, max_attempts, last_error, run_after,
       locked_at, locked_by, created_at, updated_at`

// EnqueueJob appends a durable unit of background work. It runs inside the
// caller's transaction so a job is only visible once the business write commits.
func (d *dataset) EnqueueJob(ctx context.Context, job domain.Job) (domain.Job, error) {
	if strings.TrimSpace(string(job.Kind)) == "" {
		return domain.Job{}, domain.NewFieldError("job.kind", "must not be empty")
	}
	if job.MaxAttempts <= 0 {
		job.MaxAttempts = domain.MaxJobAttempts
	}
	if job.State == "" {
		job.State = domain.JobQueued
	}
	res, err := d.q.ExecContext(ctx, `
        INSERT INTO jobs (kind, payload, state, attempts, max_attempts, last_error, run_after,
                          locked_at, locked_by, created_at, updated_at)
        VALUES (?, ?, ?, ?, ?, ?, ?, NULL, '', ?, ?)`,
		string(job.Kind), job.Payload, string(job.State), job.Attempts, job.MaxAttempts,
		job.LastError, formatTime(job.RunAfter), formatTime(job.CreatedAt), formatTime(job.UpdatedAt))
	if err != nil {
		return domain.Job{}, fmt.Errorf("insert job %s: %w", job.Kind, err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return domain.Job{}, fmt.Errorf("read inserted job id: %w", err)
	}
	job.ID = id
	return job, nil
}

// ClaimNextJob atomically leases the oldest ready job of the requested kinds.
// The UPDATE carries the state predicate so two workers cannot take the same
// row, and the attempt counter is advanced as part of the same statement.
func (d *dataset) ClaimNextJob(ctx context.Context, kinds []domain.JobKind, workerID string, now time.Time) (domain.Job, error) {
	if len(kinds) == 0 {
		return domain.Job{}, domain.NewFieldError("kinds", "at least one job kind is required")
	}
	placeholders := make([]string, len(kinds))
	args := make([]any, 0, len(kinds)+4)
	args = append(args, formatTime(now), workerID, formatTime(now))
	for i := range kinds {
		placeholders[i] = "?"
	}
	kindArgs := make([]any, 0, len(kinds))
	for _, kind := range kinds {
		kindArgs = append(kindArgs, string(kind))
	}

	query := `
        UPDATE jobs
        SET state = 'running', locked_at = ?, locked_by = ?, attempts = attempts + 1, updated_at = ?
        WHERE id = (
            SELECT id FROM jobs
            WHERE state = 'queued' AND run_after <= ? AND kind IN (` + strings.Join(placeholders, ",") + `)
            ORDER BY run_after ASC, id ASC
            LIMIT 1
        )
        RETURNING ` + jobColumns

	args = append(args, formatTime(now))
	args = append(args, kindArgs...)

	row := d.q.QueryRowContext(ctx, query, args...)
	job, err := scanJob(row)
	if err != nil {
		return domain.Job{}, notFound("claimable job", err)
	}
	return job, nil
}

// MarkJobSucceeded closes a job after a successful attempt.
func (d *dataset) MarkJobSucceeded(ctx context.Context, id int64, at time.Time) error {
	return d.finishJob(ctx, id, `
        UPDATE jobs SET state = 'succeeded', locked_at = NULL, locked_by = '', last_error = '', updated_at = ?
        WHERE id = ? AND state = 'running'`, formatTime(at), id)
}

// MarkJobRetry returns a failed job to the queue with its backoff deadline.
func (d *dataset) MarkJobRetry(ctx context.Context, id int64, runAfter time.Time, lastErr string, at time.Time) error {
	return d.finishJob(ctx, id, `
        UPDATE jobs SET state = 'queued', locked_at = NULL, locked_by = '', last_error = ?,
                        run_after = ?, updated_at = ?
        WHERE id = ? AND state = 'running'`, lastErr, formatTime(runAfter), formatTime(at), id)
}

// MarkJobPermanentlyFailed retires a job whose retry budget is exhausted.
func (d *dataset) MarkJobPermanentlyFailed(ctx context.Context, id int64, lastErr string, at time.Time) error {
	return d.finishJob(ctx, id, `
        UPDATE jobs SET state = 'permanently_failed', locked_at = NULL, locked_by = '',
                        last_error = ?, updated_at = ?
        WHERE id = ? AND state = 'running'`, lastErr, formatTime(at), id)
}

// RequeueStaleJobs recovers work that a crashed worker left in the running
// state. It is executed on every start-up before the worker loop begins.
func (d *dataset) RequeueStaleJobs(ctx context.Context, lockedBefore time.Time, at time.Time) (int, error) {
	res, err := d.q.ExecContext(ctx, `
        UPDATE jobs
        SET state = 'queued', locked_at = NULL, locked_by = '', run_after = ?, updated_at = ?
        WHERE state = 'running' AND (locked_at IS NULL OR locked_at <= ?)`,
		formatTime(at), formatTime(at), formatTime(lockedBefore))
	if err != nil {
		return 0, fmt.Errorf("requeue stale jobs: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("requeue stale job rows: %w", err)
	}
	return int(affected), nil
}

// FindJobByID loads a single job row.
func (d *dataset) FindJobByID(ctx context.Context, id int64) (domain.Job, error) {
	row := d.q.QueryRowContext(ctx, `SELECT `+jobColumns+` FROM jobs WHERE id = ?`, id)
	job, err := scanJob(row)
	if err != nil {
		return domain.Job{}, notFound(fmt.Sprintf("job %d", id), err)
	}
	return job, nil
}

// CountJobsByState reports the queue depth of one state.
func (d *dataset) CountJobsByState(ctx context.Context, state domain.JobState) (int, error) {
	return countRows(ctx, d.q, `SELECT COUNT(*) FROM jobs WHERE state = ?`, string(state))
}

func (d *dataset) finishJob(ctx context.Context, id int64, query string, args ...any) error {
	res, err := d.q.ExecContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("update job %d: %w", id, err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("update job %d rows: %w", id, err)
	}
	if affected == 0 {
		return fmt.Errorf("job %d is not held by this worker: %w", id, domain.ErrConflict)
	}
	return nil
}

func scanJob(row rowScanner) (domain.Job, error) {
	var (
		job       domain.Job
		kind      string
		state     string
		runAfter  string
		lockedAt  sql.NullString
		createdAt string
		updatedAt string
	)
	if err := row.Scan(&job.ID, &kind, &job.Payload, &state, &job.Attempts, &job.MaxAttempts,
		&job.LastError, &runAfter, &lockedAt, &job.LockedBy, &createdAt, &updatedAt); err != nil {
		return domain.Job{}, err
	}
	job.Kind = domain.JobKind(kind)
	job.State = domain.JobState(state)

	var err error
	if job.RunAfter, err = parseTime(runAfter); err != nil {
		return domain.Job{}, err
	}
	if job.LockedAt, err = parseNullableTime(lockedAt); err != nil {
		return domain.Job{}, err
	}
	if job.CreatedAt, err = parseTime(createdAt); err != nil {
		return domain.Job{}, err
	}
	if job.UpdatedAt, err = parseTime(updatedAt); err != nil {
		return domain.Job{}, err
	}
	return job, nil
}
