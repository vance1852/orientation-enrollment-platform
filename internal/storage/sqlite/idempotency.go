package sqlite

import (
	"context"
	"fmt"
	"time"

	"github.com/vance1852/orientation-enrollment-platform/internal/domain"
	"github.com/vance1852/orientation-enrollment-platform/internal/repository"
)

// FindIdempotencyRecord loads a previously stored response snapshot. The key is
// scoped by actor, HTTP method and path so the same client supplied value cannot
// leak an answer between two different endpoints or two different users.
func (d *dataset) FindIdempotencyRecord(ctx context.Context, actorID int64, method, path, key string) (repository.IdempotencyRecord, error) {
	if key == "" {
		return repository.IdempotencyRecord{}, fmt.Errorf("idempotency key: %w", domain.ErrNotFound)
	}
	var (
		record    repository.IdempotencyRecord
		createdAt string
		expiresAt string
	)
	err := d.q.QueryRowContext(ctx, `
        SELECT actor_user_id, method, path, key, request_fingerprint, response_status, response_body,
               created_at, expires_at
        FROM idempotency_keys
        WHERE actor_user_id = ? AND method = ? AND path = ? AND key = ?`,
		actorID, method, path, key).Scan(&record.ActorUserID, &record.Method, &record.Path, &record.Key,
		&record.RequestFingerprint, &record.ResponseStatus, &record.ResponseBody, &createdAt, &expiresAt)
	if err != nil {
		return repository.IdempotencyRecord{}, notFound("idempotency record", err)
	}
	if record.CreatedAt, err = parseTime(createdAt); err != nil {
		return repository.IdempotencyRecord{}, err
	}
	if record.ExpiresAt, err = parseTime(expiresAt); err != nil {
		return repository.IdempotencyRecord{}, err
	}
	return record, nil
}

// SaveIdempotencyRecord stores the response snapshot of a completed mutation.
func (d *dataset) SaveIdempotencyRecord(ctx context.Context, record repository.IdempotencyRecord) error {
	if record.Key == "" {
		return domain.NewFieldError("idempotency_key", "must not be empty")
	}
	if record.Method == "" || record.Path == "" {
		return domain.NewFieldError("idempotency_key", "method and path scope are required")
	}
	_, err := d.q.ExecContext(ctx, `
        INSERT INTO idempotency_keys
            (actor_user_id, method, path, key, request_fingerprint, response_status, response_body,
             created_at, expires_at)
        VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		record.ActorUserID, record.Method, record.Path, record.Key, record.RequestFingerprint,
		record.ResponseStatus, record.ResponseBody, formatTime(record.CreatedAt), formatTime(record.ExpiresAt))
	if err != nil {
		if isUniqueViolation(err) {
			return fmt.Errorf("idempotency key already stored: %w", domain.ErrConflict)
		}
		return fmt.Errorf("insert idempotency record: %w", err)
	}
	return nil
}

// PurgeIdempotencyRecords drops expired replay protection rows.
func (d *dataset) PurgeIdempotencyRecords(ctx context.Context, before time.Time) (int, error) {
	res, err := d.q.ExecContext(ctx,
		`DELETE FROM idempotency_keys WHERE expires_at <= ?`, formatTime(before))
	if err != nil {
		return 0, fmt.Errorf("purge idempotency records: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("purge idempotency rows: %w", err)
	}
	return int(affected), nil
}
