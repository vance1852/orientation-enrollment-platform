package domain

import (
	"strings"
	"time"
)

// Role enumerates the two business roles of the platform. A student only ever
// acts on their own record; a registrar operates on the whole term.
type Role string

const (
	// RoleStudent may submit a registration and manage their own enrollments.
	RoleStudent Role = "student"
	// RoleRegistrar verifies registrations, inspects rosters and audit trails,
	// and may force-drop a seat on behalf of a student.
	RoleRegistrar Role = "registrar"
)

// Valid reports whether the role is one of the supported business roles.
func (r Role) Valid() bool {
	return r == RoleStudent || r == RoleRegistrar
}

// ParseRole normalises external role input.
func ParseRole(raw string) (Role, error) {
	role := Role(strings.ToLower(strings.TrimSpace(raw)))
	if !role.Valid() {
		return "", NewFieldError("role", "must be student or registrar")
	}
	return role, nil
}

// User is an authenticated principal. PasswordHash stores a PBKDF2 digest and
// never leaves the persistence and authentication layers.
type User struct {
	ID           int64
	Email        string
	DisplayName  string
	Role         Role
	PasswordHash string
	Disabled     bool
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// IsRegistrar reports whether the user carries registrar authority.
func (u User) IsRegistrar() bool { return u.Role == RoleRegistrar }

// NormalizeEmail lowercases and trims an email address so the unique index on
// users.email behaves consistently regardless of client input.
func NormalizeEmail(raw string) (string, error) {
	email := strings.ToLower(strings.TrimSpace(raw))
	if email == "" {
		return "", NewFieldError("email", "must not be empty")
	}
	at := strings.IndexByte(email, '@')
	if at <= 0 || at == len(email)-1 {
		return "", NewFieldError("email", "must contain a local part and a domain")
	}
	if strings.ContainsAny(email, " \t\r\n") {
		return "", NewFieldError("email", "must not contain whitespace")
	}
	return email, nil
}

// Session is a revocable server side session. The opaque token handed to the
// client is never stored; only TokenDigest is persisted.
type Session struct {
	ID          int64
	UserID      int64
	TokenDigest string
	IssuedAt    time.Time
	ExpiresAt   time.Time
	RevokedAt   *time.Time
	LastSeenAt  time.Time
	UserAgent   string
}

// Revoked reports whether the session was explicitly invalidated by logout or
// by an administrative sweep.
func (s Session) Revoked() bool { return s.RevokedAt != nil }

// Expired reports whether the session lifetime elapsed at the given instant.
func (s Session) Expired(now time.Time) bool { return !now.Before(s.ExpiresAt) }

// Validate returns the precise reason a session cannot authenticate a request.
func (s Session) Validate(now time.Time) error {
	if s.Revoked() {
		return ErrSessionRevoked
	}
	if s.Expired(now) {
		return ErrSessionExpired
	}
	return nil
}

// Principal is the authenticated identity carried through the request context.
type Principal struct {
	UserID      int64
	SessionID   int64
	Email       string
	DisplayName string
	Role        Role
}

// IsRegistrarRole reports whether the principal carries registrar authority.
func (p Principal) IsRegistrarRole() bool { return p.Role == RoleRegistrar }

// CanActOnStudent reports whether the principal may operate on the target
// student record. Registrars act on everyone, students only on themselves.
func (p Principal) CanActOnStudent(studentID int64) bool {
	if p.Role == RoleRegistrar {
		return true
	}
	return p.Role == RoleStudent && p.UserID == studentID
}
