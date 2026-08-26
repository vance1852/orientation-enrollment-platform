// Package service holds the use cases of the orientation platform. Every
// exported method takes a context as its first argument and returns wrapped
// domain sentinels so the transport layer can map failures without inspecting
// error strings.
package service

import (
	"context"
	"fmt"
	"log/slog"
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

// writeBudget bounds a mutating use case that runs on its own deadline.
const writeBudget = 30 * time.Second

// writeContext gives a mutating use case its own budget.
//
// Releasing a seat touches the enrollment, the section counters, the job queue
// and the audit trail, so the transaction is carried out on a dedicated context
// instead of being tied to the caller.
func (d Deps) writeContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), writeBudget)
}

func (d Deps) logger() *slog.Logger {
	if d.Logger == nil {
		return slog.Default()
	}
	return d.Logger
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
