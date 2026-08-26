// Package audit turns business outcomes into durable trail entries.
//
// The recorder deliberately takes a repository handle per call so it can join
// the transaction of the write it describes: if the audit insert fails, the
// business write is rolled back with it.
package audit

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/vance1852/orientation-enrollment-platform/internal/domain"
	"github.com/vance1852/orientation-enrollment-platform/internal/platform/logging"
	"github.com/vance1852/orientation-enrollment-platform/internal/repository"
)

// Entry is the caller facing description of one audited outcome.
type Entry struct {
	Actor      *domain.Principal
	Action     domain.AuditAction
	ObjectType string
	ObjectID   string
	Result     domain.AuditResult
	Detail     string
}

// Recorder appends audit entries and mirrors them into the structured log.
type Recorder struct {
	now func() time.Time
}

// NewRecorder builds a recorder using the given time source.
func NewRecorder(now func() time.Time) *Recorder {
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &Recorder{now: now}
}

// Record appends one entry through the supplied repository, which may belong to
// an open transaction.
func (r *Recorder) Record(ctx context.Context, repo repository.AuditRepository, entry Entry) error {
	event := domain.AuditEvent{
		Action:     entry.Action,
		ObjectType: entry.ObjectType,
		ObjectID:   entry.ObjectID,
		Result:     entry.Result,
		RequestID:  logging.RequestID(ctx),
		Detail:     entry.Detail,
		OccurredAt: r.now().UTC(),
	}
	if entry.Actor != nil {
		actorID := entry.Actor.UserID
		event.ActorUserID = &actorID
		event.ActorRole = string(entry.Actor.Role)
	}
	// Validating here keeps a malformed entry from reaching the database and
	// aborting the business transaction it is attached to.
	if err := event.Validate(); err != nil {
		return fmt.Errorf("audit entry %s is invalid: %w", entry.Action, err)
	}
	if _, err := repo.AppendAuditEvent(ctx, event); err != nil {
		return fmt.Errorf("record audit event %s: %w", entry.Action, err)
	}
	return nil
}

// RecordObjectID is a convenience wrapper for numeric object identifiers.
func (r *Recorder) RecordObjectID(ctx context.Context, repo repository.AuditRepository, actor *domain.Principal,
	action domain.AuditAction, objectType string, objectID int64, result domain.AuditResult, detail string) error {
	return r.Record(ctx, repo, Entry{
		Actor:      actor,
		Action:     action,
		ObjectType: objectType,
		ObjectID:   strconv.FormatInt(objectID, 10),
		Result:     result,
		Detail:     detail,
	})
}
