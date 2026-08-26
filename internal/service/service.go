// Package service holds the use cases of the orientation platform. Every
// exported method takes a context as its first argument and returns wrapped
// domain sentinels so the transport layer can map failures without inspecting
// error strings.
package service

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/vance1852/orientation-enrollment-platform/internal/audit"
	"github.com/vance1852/orientation-enrollment-platform/internal/domain"
	"github.com/vance1852/orientation-enrollment-platform/internal/platform/clock"
	"github.com/vance1852/orientation-enrollment-platform/internal/repository"
)

type principalKey struct{}

// WithPrincipal stores the authenticated identity in the context.
func WithPrincipal(ctx context.Context, principal domain.Principal) context.Context {
	return context.WithValue(ctx, principalKey{}, principal)
}

// PrincipalFrom reads the authenticated identity from the context.
func PrincipalFrom(ctx context.Context) (domain.Principal, bool) {
	principal, ok := ctx.Value(principalKey{}).(domain.Principal)
	return principal, ok
}

// Deps is the shared dependency set of every service in this package.
type Deps struct {
	Store  repository.Store
	Clock  clock.Clock
	Audit  *audit.Recorder
	Logger *slog.Logger
}

// Validate reports missing dependencies instead of failing later with a nil
// pointer dereference deep inside a request.
func (d Deps) Validate() error {
	if d.Store == nil {
		return domain.NewFieldError("deps.store", "must not be nil")
	}
	if d.Clock == nil {
		return domain.NewFieldError("deps.clock", "must not be nil")
	}
	if d.Audit == nil {
		return domain.NewFieldError("deps.audit", "must not be nil")
	}
	return nil
}

func (d Deps) now() time.Time {
	return d.Clock.Now().UTC()
}

func (d Deps) logger() *slog.Logger {
	if d.Logger == nil {
		return slog.Default()
	}
	return d.Logger
}

// idempotencyReservation is the in-process copy of a stored response snapshot.
type idempotencyReservation struct {
	fingerprint string
	status      int
	body        string
	expiresAt   time.Time
}

// idempotencyReservations lets a warm process replay a snapshot without paying
// for another database round trip.
type idempotencyReservations struct {
	mu      sync.RWMutex
	entries map[string]idempotencyReservation
}

func newIdempotencyReservations() *idempotencyReservations {
	return &idempotencyReservations{entries: make(map[string]idempotencyReservation)}
}

// load returns a reservation that is still inside its lifetime.
func (r *idempotencyReservations) load(key string, now time.Time) (idempotencyReservation, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	entry, ok := r.entries[key]
	if !ok || !now.Before(entry.expiresAt) {
		return idempotencyReservation{}, false
	}
	return entry, true
}

// store keeps the snapshot of a completed mutation.
func (r *idempotencyReservations) store(key string, entry idempotencyReservation) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.entries[key] = entry
}

// requireRole rejects a principal that lacks the required business role.
func requireRole(actor domain.Principal, role domain.Role) error {
	if actor.Role != role {
		return domain.ErrForbidden
	}
	return nil
}

// recordRejection appends a rejected outcome to the audit trail in its own
// transaction.
//
// The rejected attempt must survive even though the business transaction that
// produced it was rolled back, so it cannot share that transaction. Failing to
// write the trail entry is logged and never masks the original business error.
func (d Deps) recordRejection(ctx context.Context, actor domain.Principal, action domain.AuditAction,
	objectType string, objectID int64, cause error) {
	if cause == nil {
		return
	}
	code := domain.Code(cause)
	if code == "internal_error" {
		return
	}
	err := d.Store.InTx(ctx, func(ctx context.Context, tx repository.Repositories) error {
		return d.Audit.RecordObjectID(ctx, tx.Audit(), &actor, action, objectType, objectID,
			domain.ResultRejected, fmt.Sprintf("%s: %s", code, cause.Error()))
	})
	if err != nil {
		d.logger().Warn("recording a rejected outcome failed",
			"action", string(action), "object_type", objectType, "error", err.Error())
	}
}
