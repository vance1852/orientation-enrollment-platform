package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/vance1852/orientation-enrollment-platform/internal/domain"
)

const sessionColumns = `id, user_id, token_digest, issued_at, expires_at, revoked_at, last_seen_at, user_agent`

// CreateSession stores a new session row keyed by the token digest.
func (d *dataset) CreateSession(ctx context.Context, session domain.Session) (domain.Session, error) {
	if session.TokenDigest == "" {
		return domain.Session{}, domain.NewFieldError("token", "digest must not be empty")
	}
	if !session.IssuedAt.Before(session.ExpiresAt) {
		return domain.Session{}, domain.NewFieldError("session.expires_at", "must be after issued_at")
	}
	res, err := d.q.ExecContext(ctx, `
        INSERT INTO sessions (user_id, token_digest, issued_at, expires_at, revoked_at, last_seen_at, user_agent)
        VALUES (?, ?, ?, ?, ?, ?, ?)`,
		session.UserID, session.TokenDigest, formatTime(session.IssuedAt), formatTime(session.ExpiresAt),
		formatNullableTime(session.RevokedAt), formatTime(session.LastSeenAt), session.UserAgent)
	if err != nil {
		if isUniqueViolation(err) {
			return domain.Session{}, fmt.Errorf("session digest collision: %w", domain.ErrConflict)
		}
		return domain.Session{}, fmt.Errorf("insert session: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return domain.Session{}, fmt.Errorf("read inserted session id: %w", err)
	}
	session.ID = id
	return session, nil
}

// FindSessionByDigest loads the session matching a bearer token digest.
func (d *dataset) FindSessionByDigest(ctx context.Context, digest string) (domain.Session, error) {
	if digest == "" {
		return domain.Session{}, fmt.Errorf("session lookup: %w", domain.ErrUnauthenticated)
	}
	row := d.q.QueryRowContext(ctx, `SELECT `+sessionColumns+` FROM sessions WHERE token_digest = ?`, digest)
	session, err := scanSession(row)
	if err != nil {
		return domain.Session{}, notFound("session", err)
	}
	return session, nil
}

// TouchSession records the latest activity timestamp of a live session.
func (d *dataset) TouchSession(ctx context.Context, id int64, seenAt time.Time) error {
	res, err := d.q.ExecContext(ctx,
		`UPDATE sessions SET last_seen_at = ? WHERE id = ? AND revoked_at IS NULL`,
		formatTime(seenAt), id)
	if err != nil {
		return fmt.Errorf("touch session %d: %w", id, err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("touch session %d rows: %w", id, err)
	}
	if affected == 0 {
		return fmt.Errorf("session %d: %w", id, domain.ErrNotFound)
	}
	return nil
}

// RevokeSession invalidates a single session, which is what logout performs.
func (d *dataset) RevokeSession(ctx context.Context, id int64, at time.Time) error {
	res, err := d.q.ExecContext(ctx,
		`UPDATE sessions SET revoked_at = ? WHERE id = ? AND revoked_at IS NULL`,
		formatTime(at), id)
	if err != nil {
		return fmt.Errorf("revoke session %d: %w", id, err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("revoke session %d rows: %w", id, err)
	}
	if affected == 0 {
		return fmt.Errorf("session %d already revoked or missing: %w", id, domain.ErrNotFound)
	}
	return nil
}

// RevokeSessionsForUser closes the live sessions of one principal. Signing out
// is an account level decision, so it covers the sessions the principal opened
// from the campus portal as well.
func (d *dataset) RevokeSessionsForUser(ctx context.Context, userID int64, at time.Time) (int, error) {
	res, err := d.q.ExecContext(ctx,
		`UPDATE sessions SET revoked_at = ? WHERE user_id = ? AND revoked_at IS NULL`,
		formatTime(at), userID)
	if err != nil {
		return 0, fmt.Errorf("revoke sessions of user %d: %w", userID, err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("revoke sessions of user %d rows: %w", userID, err)
	}
	if affected == 0 {
		return 0, fmt.Errorf("user %d holds no live session: %w", userID, domain.ErrNotFound)
	}
	return int(affected), nil
}

// RevokeExpiredSessions is the sweep executed by the background worker. It
// revokes at most limit rows per call so a large backlog stays interruptible.
func (d *dataset) RevokeExpiredSessions(ctx context.Context, before time.Time, limit int) (int, error) {
	if limit <= 0 {
		limit = 100
	}
	res, err := d.q.ExecContext(ctx, `
        UPDATE sessions SET revoked_at = ?
        WHERE id IN (
            SELECT id FROM sessions
            WHERE revoked_at IS NULL AND expires_at <= ?
            ORDER BY expires_at
            LIMIT ?
        )`, formatTime(before), formatTime(before), limit)
	if err != nil {
		return 0, fmt.Errorf("revoke expired sessions: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("revoke expired sessions rows: %w", err)
	}
	return int(affected), nil
}

// CountActiveSessions reports how many usable sessions a user currently holds.
func (d *dataset) CountActiveSessions(ctx context.Context, userID int64, now time.Time) (int, error) {
	return countRows(ctx, d.q,
		`SELECT COUNT(*) FROM sessions WHERE user_id = ? AND revoked_at IS NULL AND expires_at > ?`,
		userID, formatTime(now))
}

func scanSession(row rowScanner) (domain.Session, error) {
	var (
		session   domain.Session
		issuedAt  string
		expiresAt string
		revokedAt sql.NullString
		lastSeen  string
	)
	if err := row.Scan(&session.ID, &session.UserID, &session.TokenDigest, &issuedAt, &expiresAt,
		&revokedAt, &lastSeen, &session.UserAgent); err != nil {
		return domain.Session{}, err
	}
	var err error
	if session.IssuedAt, err = parseTime(issuedAt); err != nil {
		return domain.Session{}, err
	}
	if session.ExpiresAt, err = parseTime(expiresAt); err != nil {
		return domain.Session{}, err
	}
	if session.RevokedAt, err = parseNullableTime(revokedAt); err != nil {
		return domain.Session{}, err
	}
	if session.LastSeenAt, err = parseTime(lastSeen); err != nil {
		return domain.Session{}, err
	}
	return session, nil
}
