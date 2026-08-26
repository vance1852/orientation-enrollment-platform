// Package sqlite implements the repository contracts on top of a real SQLite
// database accessed through database/sql. Every statement is a plain SQL
// statement executed with a context, and every cross entity write runs inside a
// database transaction.
package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/vance1852/orientation-enrollment-platform/internal/domain"
	"github.com/vance1852/orientation-enrollment-platform/internal/repository"

	_ "modernc.org/sqlite" // pure Go SQLite driver, no cgo toolchain required
)

// driverName is the database/sql driver registered by modernc.org/sqlite.
const driverName = "sqlite"

// querier is the subset of database/sql shared by *sql.DB and *sql.Tx, which
// lets one set of repository methods serve both.
type querier interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// dataset carries a querier and implements every repository interface. Methods
// are grouped by aggregate across the files of this package.
type dataset struct {
	q querier
}

// Users returns the user repository bound to the current querier.
func (d *dataset) Users() repository.UserRepository { return d }

// Sessions returns the session repository bound to the current querier.
func (d *dataset) Sessions() repository.SessionRepository { return d }

// Catalog returns the catalogue repository bound to the current querier.
func (d *dataset) Catalog() repository.CatalogRepository { return d }

// Registrations returns the registration repository bound to the current querier.
func (d *dataset) Registrations() repository.RegistrationRepository { return d }

// Enrollments returns the enrollment repository bound to the current querier.
func (d *dataset) Enrollments() repository.EnrollmentRepository { return d }

// Idempotency returns the idempotency repository bound to the current querier.
func (d *dataset) Idempotency() repository.IdempotencyRepository { return d }

// Audit returns the audit repository bound to the current querier.
func (d *dataset) Audit() repository.AuditRepository { return d }

// Jobs returns the job repository bound to the current querier.
func (d *dataset) Jobs() repository.JobRepository { return d }

// Store is the root persistence handle backed by a SQLite database file.
type Store struct {
	*dataset
	db *sql.DB
}

// Options tunes the connection pool of a store.
type Options struct {
	MaxOpenConns int
	MaxIdleConns int
	ConnLifetime time.Duration
}

// Open resolves the DSN, applies the required pragmas and returns a store.
// The caller owns Close.
func Open(ctx context.Context, dsn string, opts Options) (*Store, error) {
	resolved, err := normalizeDSN(dsn)
	if err != nil {
		return nil, err
	}
	db, err := sql.Open(driverName, resolved)
	if err != nil {
		return nil, fmt.Errorf("open sqlite database: %w", err)
	}
	if opts.MaxOpenConns <= 0 {
		opts.MaxOpenConns = 8
	}
	if opts.MaxIdleConns <= 0 {
		opts.MaxIdleConns = opts.MaxOpenConns
	}
	db.SetMaxOpenConns(opts.MaxOpenConns)
	db.SetMaxIdleConns(opts.MaxIdleConns)
	if opts.ConnLifetime > 0 {
		db.SetConnMaxLifetime(opts.ConnLifetime)
	}
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping sqlite database: %w", err)
	}
	return &Store{dataset: &dataset{q: db}, db: db}, nil
}

// DB exposes the pool for migration bookkeeping inside this package.
func (s *Store) DB() *sql.DB { return s.db }

// Ping verifies the database is reachable, used by the readiness endpoint.
func (s *Store) Ping(ctx context.Context) error {
	if err := s.db.PingContext(ctx); err != nil {
		return fmt.Errorf("sqlite ping: %w", err)
	}
	return nil
}

// Close releases the pool.
func (s *Store) Close() error {
	if s.db == nil {
		return nil
	}
	if err := s.db.Close(); err != nil {
		return fmt.Errorf("close sqlite database: %w", err)
	}
	return nil
}

// InTx runs fn inside one transaction. The transaction is rolled back when fn
// returns an error or panics, so a failed cross entity write never leaves a
// partially applied state behind.
func (s *Store) InTx(ctx context.Context, fn func(ctx context.Context, tx repository.Repositories) error) (err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	committed := false
	defer func() {
		if recovered := recover(); recovered != nil {
			_ = tx.Rollback()
			panic(recovered)
		}
		if !committed {
			if rollbackErr := tx.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
				err = errors.Join(err, fmt.Errorf("rollback transaction: %w", rollbackErr))
			}
		}
	}()

	scoped := &dataset{q: tx}
	if err = fn(ctx, scoped); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("commit transaction: %w", err)
	}
	committed = true
	return nil
}

// normalizeDSN guarantees the pragmas the schema depends on. Foreign keys are
// off by default in SQLite, and WAL plus a busy timeout is what lets concurrent
// enrollment requests queue instead of failing outright.
func normalizeDSN(dsn string) (string, error) {
	trimmed := strings.TrimSpace(dsn)
	if trimmed == "" {
		return "", fmt.Errorf("sqlite dsn must not be empty")
	}
	required := []struct {
		marker string
		value  string
	}{
		{"_pragma=foreign_keys", "_pragma=foreign_keys(1)"},
		{"_pragma=busy_timeout", "_pragma=busy_timeout(10000)"},
		{"_pragma=journal_mode", "_pragma=journal_mode(WAL)"},
		{"_txlock=", "_txlock=immediate"},
	}
	for _, item := range required {
		if strings.Contains(trimmed, item.marker) {
			continue
		}
		if strings.Contains(trimmed, "?") {
			trimmed += "&" + item.value
		} else {
			trimmed += "?" + item.value
		}
	}
	return trimmed, nil
}

// formatTime renders an instant for storage. All timestamps are stored as UTC
// RFC3339 strings with nanosecond precision so lexical ordering equals
// chronological ordering.
func formatTime(t time.Time) string {
	return t.UTC().Format(time.RFC3339Nano)
}

// formatNullableTime renders an optional instant.
func formatNullableTime(t *time.Time) any {
	if t == nil {
		return nil
	}
	return formatTime(*t)
}

// parseTime reads a stored timestamp.
func parseTime(raw string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse stored timestamp %q: %w", raw, err)
	}
	return parsed.UTC(), nil
}

// parseNullableTime reads an optional stored timestamp.
func parseNullableTime(raw sql.NullString) (*time.Time, error) {
	if !raw.Valid || raw.String == "" {
		return nil, nil
	}
	parsed, err := parseTime(raw.String)
	if err != nil {
		return nil, err
	}
	return &parsed, nil
}

// notFound converts sql.ErrNoRows into the domain sentinel while keeping the
// original driver error reachable through the chain.
func notFound(entity string, err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%s: %w", entity, domain.ErrNotFound)
	}
	return err
}

// boolToInt maps a Go bool onto the SQLite integer representation.
func boolToInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

// isUniqueViolation reports whether the driver rejected a write because of a
// unique index, which the service layer maps onto a business conflict.
func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "unique constraint failed") || strings.Contains(msg, "constraint failed: unique")
}

// nullableInt64 renders an optional identifier for storage.
func nullableInt64(v *int64) any {
	if v == nil {
		return nil
	}
	return *v
}

// readNullableInt64 reads an optional identifier.
func readNullableInt64(raw sql.NullInt64) *int64 {
	if !raw.Valid {
		return nil
	}
	value := raw.Int64
	return &value
}
