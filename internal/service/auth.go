package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/vance1852/orientation-enrollment-platform/internal/audit"
	"github.com/vance1852/orientation-enrollment-platform/internal/domain"
	"github.com/vance1852/orientation-enrollment-platform/internal/platform/ids"
	"github.com/vance1852/orientation-enrollment-platform/internal/repository"
	"github.com/vance1852/orientation-enrollment-platform/internal/security"
)

// AuthService issues, validates and revokes server side sessions.
type AuthService struct {
	deps       Deps
	sessionTTL time.Duration
}

// NewAuthService builds the authentication use cases.
func NewAuthService(deps Deps, sessionTTL time.Duration) (*AuthService, error) {
	if err := deps.Validate(); err != nil {
		return nil, err
	}
	if sessionTTL < time.Minute {
		return nil, domain.NewFieldError("session_ttl", "must be at least one minute")
	}
	return &AuthService{deps: deps, sessionTTL: sessionTTL}, nil
}

// LoginResult carries the opaque token handed to the client exactly once.
type LoginResult struct {
	Token     string
	ExpiresAt time.Time
	Principal domain.Principal
}

// Login verifies credentials and opens a revocable session.
//
// Both an unknown address and a wrong password return ErrUnauthenticated so the
// endpoint cannot be used to enumerate accounts.
func (s *AuthService) Login(ctx context.Context, email, password, userAgent string) (LoginResult, error) {
	normalized, err := domain.NormalizeEmail(email)
	if err != nil {
		return LoginResult{}, err
	}
	if password == "" {
		return LoginResult{}, domain.NewFieldError("password", "must not be empty")
	}

	user, err := s.deps.Store.Users().FindUserByEmail(ctx, normalized)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return LoginResult{}, fmt.Errorf("login rejected for %s: %w", normalized, domain.ErrUnauthenticated)
		}
		return LoginResult{}, err
	}
	if user.Disabled {
		return LoginResult{}, fmt.Errorf("account %s is disabled: %w", normalized, domain.ErrForbidden)
	}
	matches, err := security.VerifyPassword(user.PasswordHash, password)
	if err != nil {
		return LoginResult{}, fmt.Errorf("verify password of %s: %w", normalized, err)
	}
	if !matches {
		return LoginResult{}, fmt.Errorf("login rejected for %s: %w", normalized, domain.ErrUnauthenticated)
	}

	token, err := ids.NewSessionToken()
	if err != nil {
		return LoginResult{}, err
	}
	now := s.deps.now()
	session := domain.Session{
		UserID:      user.ID,
		TokenDigest: security.TokenDigest(token),
		IssuedAt:    now,
		ExpiresAt:   now.Add(s.sessionTTL),
		LastSeenAt:  now,
		UserAgent:   userAgent,
	}

	principal := domain.Principal{
		UserID:      user.ID,
		Email:       user.Email,
		DisplayName: user.DisplayName,
		Role:        user.Role,
	}
	err = s.deps.Store.InTx(ctx, func(ctx context.Context, tx repository.Repositories) error {
		created, err := tx.Sessions().CreateSession(ctx, session)
		if err != nil {
			return err
		}
		session = created
		principal.SessionID = created.ID
		return s.deps.Audit.RecordObjectID(ctx, tx.Audit(), &principal, domain.ActionLogin,
			"session", created.ID, domain.ResultSuccess,
			fmt.Sprintf("session opened for %s until %s", user.Email, created.ExpiresAt.Format(time.RFC3339)))
	})
	if err != nil {
		return LoginResult{}, err
	}
	return LoginResult{Token: token, ExpiresAt: session.ExpiresAt, Principal: principal}, nil
}

// Authenticate resolves a bearer token into a principal. Expiry and revocation
// are reported as distinct sentinels so the client can tell a stale session from
// an explicit logout.
func (s *AuthService) Authenticate(ctx context.Context, token string) (domain.Principal, error) {
	if token == "" {
		return domain.Principal{}, fmt.Errorf("missing bearer token: %w", domain.ErrUnauthenticated)
	}
	session, err := s.deps.Store.Sessions().FindSessionByDigest(ctx, security.TokenDigest(token))
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return domain.Principal{}, fmt.Errorf("unknown bearer token: %w", domain.ErrUnauthenticated)
		}
		return domain.Principal{}, err
	}
	now := s.deps.now()
	if err := session.Validate(now); err != nil {
		return domain.Principal{}, fmt.Errorf("session %d is not usable: %w", session.ID, err)
	}
	user, err := s.deps.Store.Users().FindUserByID(ctx, session.UserID)
	if err != nil {
		return domain.Principal{}, err
	}
	if user.Disabled {
		return domain.Principal{}, fmt.Errorf("account %s is disabled: %w", user.Email, domain.ErrForbidden)
	}
	if err := s.deps.Store.Sessions().TouchSession(ctx, session.ID, now); err != nil {
		// A revoked session between the two statements is not a server fault.
		if !errors.Is(err, domain.ErrNotFound) {
			return domain.Principal{}, err
		}
		return domain.Principal{}, fmt.Errorf("session %d was revoked: %w", session.ID, domain.ErrSessionRevoked)
	}
	return domain.Principal{
		UserID:      user.ID,
		SessionID:   session.ID,
		Email:       user.Email,
		DisplayName: user.DisplayName,
		Role:        user.Role,
	}, nil
}

// Logout revokes the caller's own session and records the event.
func (s *AuthService) Logout(ctx context.Context, actor domain.Principal) error {
	if actor.SessionID <= 0 {
		return fmt.Errorf("logout without an active session: %w", domain.ErrUnauthenticated)
	}
	now := s.deps.now()
	return s.deps.Store.InTx(ctx, func(ctx context.Context, tx repository.Repositories) error {
		revoked, err := tx.Sessions().RevokeSessionsForUser(ctx, actor.UserID, now)
		if err != nil {
			return err
		}
		return s.deps.Audit.RecordObjectID(ctx, tx.Audit(), &actor, domain.ActionLogout,
			"session", actor.SessionID, domain.ResultSuccess,
			fmt.Sprintf("logout closed %d session(s) of %s", revoked, actor.Email))
	})
}

// ActiveSessionCount reports how many usable sessions a principal holds, which
// the profile endpoint surfaces so a student can spot a forgotten device.
func (s *AuthService) ActiveSessionCount(ctx context.Context, actor domain.Principal) (int, error) {
	return s.deps.Store.Sessions().CountActiveSessions(ctx, actor.UserID, s.deps.now())
}

// SweepExpiredSessions revokes sessions whose lifetime elapsed. It is driven by
// the background worker and is safe to call repeatedly.
func (s *AuthService) SweepExpiredSessions(ctx context.Context, limit int) (int, error) {
	now := s.deps.now()
	var revoked int
	err := s.deps.Store.InTx(ctx, func(ctx context.Context, tx repository.Repositories) error {
		count, err := tx.Sessions().RevokeExpiredSessions(ctx, now, limit)
		if err != nil {
			return err
		}
		revoked = count
		if count == 0 {
			return nil
		}
		return s.deps.Audit.Record(ctx, tx.Audit(), audit.Entry{
			Action:     domain.ActionSessionSwept,
			ObjectType: "session",
			ObjectID:   "batch",
			Result:     domain.ResultSuccess,
			Detail:     fmt.Sprintf("revoked %d expired sessions", count),
		})
	})
	if err != nil {
		return 0, err
	}
	return revoked, nil
}
