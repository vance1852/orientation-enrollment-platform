package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/vance1852/orientation-enrollment-platform/internal/domain"
	"github.com/vance1852/orientation-enrollment-platform/internal/migrations"
)

const schemaMigrationsDDL = `
CREATE TABLE IF NOT EXISTS schema_migrations (
    version    INTEGER PRIMARY KEY,
    name       TEXT NOT NULL,
    checksum   TEXT NOT NULL,
    applied_at TEXT NOT NULL
)`

// MigrationOutcome reports what a migration run actually did, so start-up logs
// distinguish a fresh install from a no-op restart.
type MigrationOutcome struct {
	Applied        []int
	AlreadyCurrent bool
	Version        int
}

// Migrate brings the database up to the latest embedded schema version.
//
// The function is safe to call on every start-up: already applied versions are
// skipped. When a stored checksum differs from the embedded file the run stops
// with domain.ErrMigrationDrift instead of rewriting or dropping user data.
func Migrate(ctx context.Context, db *sql.DB) (MigrationOutcome, error) {
	if db == nil {
		return MigrationOutcome{}, fmt.Errorf("migrate: database handle is nil")
	}
	all, err := migrations.All()
	if err != nil {
		return MigrationOutcome{}, err
	}
	if _, err := db.ExecContext(ctx, schemaMigrationsDDL); err != nil {
		return MigrationOutcome{}, fmt.Errorf("create schema_migrations: %w", err)
	}

	applied, err := loadAppliedMigrations(ctx, db)
	if err != nil {
		return MigrationOutcome{}, err
	}
	for version, record := range applied {
		if version > len(all) {
			return MigrationOutcome{}, fmt.Errorf(
				"database has migration %d but the repository only ships %d: %w",
				version, len(all), domain.ErrMigrationDrift)
		}
		embedded := all[version-1]
		if embedded.Checksum != record {
			return MigrationOutcome{}, fmt.Errorf(
				"migration %d (%s) was applied with checksum %s but the repository ships %s: %w",
				version, embedded.Name, record, embedded.Checksum, domain.ErrMigrationDrift)
		}
	}

	outcome := MigrationOutcome{Version: len(all)}
	for _, migration := range all {
		if _, ok := applied[migration.Version]; ok {
			continue
		}
		if err := applyMigration(ctx, db, migration); err != nil {
			return MigrationOutcome{}, err
		}
		outcome.Applied = append(outcome.Applied, migration.Version)
	}
	outcome.AlreadyCurrent = len(outcome.Applied) == 0
	return outcome, nil
}

func applyMigration(ctx context.Context, db *sql.DB, migration migrations.Migration) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin migration %d: %w", migration.Version, err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, migration.Script); err != nil {
		return fmt.Errorf("apply migration %d (%s): %w", migration.Version, migration.Name, err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO schema_migrations (version, name, checksum, applied_at) VALUES (?, ?, ?, ?)`,
		migration.Version, migration.Name, migration.Checksum, formatTime(time.Now())); err != nil {
		return fmt.Errorf("record migration %d: %w", migration.Version, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit migration %d: %w", migration.Version, err)
	}
	return nil
}

func loadAppliedMigrations(ctx context.Context, db *sql.DB) (map[int]string, error) {
	rows, err := db.QueryContext(ctx, `SELECT version, checksum FROM schema_migrations ORDER BY version`)
	if err != nil {
		return nil, fmt.Errorf("read schema_migrations: %w", err)
	}
	defer func() { _ = rows.Close() }()

	applied := make(map[int]string)
	for rows.Next() {
		var version int
		var checksum string
		if err := rows.Scan(&version, &checksum); err != nil {
			return nil, fmt.Errorf("scan schema_migrations: %w", err)
		}
		applied[version] = checksum
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate schema_migrations: %w", err)
	}
	return applied, nil
}

// SchemaVersion returns the highest applied migration version, or zero for a
// database that has never been migrated.
func (s *Store) SchemaVersion(ctx context.Context) (int, error) {
	var version sql.NullInt64
	err := s.db.QueryRowContext(ctx, `SELECT MAX(version) FROM schema_migrations`).Scan(&version)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, nil
		}
		return 0, fmt.Errorf("read schema version: %w", err)
	}
	if !version.Valid {
		return 0, nil
	}
	return int(version.Int64), nil
}
