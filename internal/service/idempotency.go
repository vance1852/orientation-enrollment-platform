package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/vance1852/orientation-enrollment-platform/internal/domain"
	"github.com/vance1852/orientation-enrollment-platform/internal/repository"
	"github.com/vance1852/orientation-enrollment-platform/internal/security"
)

// IdempotencyTTL bounds how long a stored response snapshot can be replayed.
const IdempotencyTTL = 24 * time.Hour

// IdempotencyService replays the stored outcome of a mutating request instead of
// executing it twice.
type IdempotencyService struct {
	deps Deps
}

// NewIdempotencyService builds the replay protection use case.
func NewIdempotencyService(deps Deps) (*IdempotencyService, error) {
	if err := deps.Validate(); err != nil {
		return nil, err
	}
	return &IdempotencyService{deps: deps}, nil
}

// Outcome is the response snapshot associated with an idempotency key.
type Outcome struct {
	Status   int
	Body     string
	Replayed bool
}

// Scope identifies the key namespace. Including the method and the path stops a
// client supplied value from leaking a stored answer between two endpoints, and
// including the actor stops it from leaking between two users.
type Scope struct {
	ActorUserID int64
	Method      string
	Path        string
	Key         string
	Payload     string
}

// Fingerprint returns the digest of the request payload bound to a key.
func (s Scope) Fingerprint() string {
	return security.FingerprintPayload(s.Method, s.Path, s.Payload)
}

// Execute runs fn at most once per scope.
//
// When the key was already used with the same payload the stored snapshot is
// returned. When it was used with a different payload the call fails with
// ErrIdempotencyMismatch rather than silently returning an unrelated answer.
func (s *IdempotencyService) Execute(ctx context.Context, scope Scope, fn func(ctx context.Context) (int, string, error)) (Outcome, error) {
	if scope.Key == "" {
		status, body, err := fn(ctx)
		if err != nil {
			return Outcome{}, err
		}
		return Outcome{Status: status, Body: body}, nil
	}
	if scope.ActorUserID <= 0 {
		return Outcome{}, fmt.Errorf("idempotent request without an actor: %w", domain.ErrUnauthenticated)
	}
	fingerprint := scope.Fingerprint()

	existing, err := s.deps.Store.Idempotency().FindIdempotencyRecord(ctx, scope.ActorUserID, scope.Method, scope.Path, scope.Key)
	switch {
	case err == nil:
		if existing.RequestFingerprint != fingerprint {
			return Outcome{}, fmt.Errorf("idempotency key %q was used with a different payload: %w",
				scope.Key, domain.ErrIdempotencyMismatch)
		}
		if !s.deps.now().Before(existing.ExpiresAt) {
			return Outcome{}, fmt.Errorf("idempotency key %q expired: %w", scope.Key, domain.ErrConflict)
		}
		return Outcome{Status: existing.ResponseStatus, Body: existing.ResponseBody, Replayed: true}, nil
	case errors.Is(err, domain.ErrNotFound):
	default:
		return Outcome{}, err
	}

	status, body, err := fn(ctx)
	if err != nil {
		return Outcome{}, err
	}

	now := s.deps.now()
	record := repository.IdempotencyRecord{
		ActorUserID:        scope.ActorUserID,
		Method:             scope.Method,
		Path:               scope.Path,
		Key:                scope.Key,
		RequestFingerprint: fingerprint,
		ResponseStatus:     status,
		ResponseBody:       body,
		CreatedAt:          now,
		ExpiresAt:          now.Add(IdempotencyTTL),
	}
	if err := s.deps.Store.Idempotency().SaveIdempotencyRecord(ctx, record); err != nil {
		// Two concurrent requests with the same key can race here. The loser
		// replays the stored snapshot instead of reporting a server error.
		if errors.Is(err, domain.ErrConflict) {
			stored, findErr := s.deps.Store.Idempotency().FindIdempotencyRecord(ctx,
				scope.ActorUserID, scope.Method, scope.Path, scope.Key)
			if findErr == nil && stored.RequestFingerprint == fingerprint {
				return Outcome{Status: stored.ResponseStatus, Body: stored.ResponseBody, Replayed: true}, nil
			}
		}
		return Outcome{}, err
	}
	return Outcome{Status: status, Body: body}, nil
}

// PurgeExpired removes stale replay protection rows.
func (s *IdempotencyService) PurgeExpired(ctx context.Context) (int, error) {
	return s.deps.Store.Idempotency().PurgeIdempotencyRecords(ctx, s.deps.now())
}
