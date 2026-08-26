package audit_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/vance1852/orientation-enrollment-platform/internal/audit"
	"github.com/vance1852/orientation-enrollment-platform/internal/domain"
	"github.com/vance1852/orientation-enrollment-platform/internal/platform/logging"
)

// captureRepo records what the recorder handed to the persistence layer.
type captureRepo struct {
	events []domain.AuditEvent
	err    error
}

func (c *captureRepo) AppendAuditEvent(_ context.Context, event domain.AuditEvent) (domain.AuditEvent, error) {
	if c.err != nil {
		return domain.AuditEvent{}, c.err
	}
	event.ID = int64(len(c.events) + 1)
	c.events = append(c.events, event)
	return event, nil
}

func (c *captureRepo) ListAuditEvents(context.Context, domain.AuditFilter) (domain.PageResult[domain.AuditEvent], error) {
	return domain.NewPageResult(c.events, len(c.events), domain.Page{Number: 1, Size: len(c.events)}), nil
}

func TestRecorderStampsActorRequestAndTime(t *testing.T) {
	moment := time.Date(2026, time.August, 26, 10, 30, 0, 0, time.UTC)
	recorder := audit.NewRecorder(func() time.Time { return moment })
	repo := &captureRepo{}
	ctx := logging.WithRequestID(context.Background(), "req_audit")
	actor := domain.Principal{UserID: 42, Role: domain.RoleRegistrar}

	if err := recorder.RecordObjectID(ctx, repo, &actor, domain.ActionEnrollmentDrop,
		"enrollment", 7, domain.ResultSuccess, "seat released"); err != nil {
		t.Fatalf("recording failed: %v", err)
	}
	if len(repo.events) != 1 {
		t.Fatalf("events = %d, want 1", len(repo.events))
	}
	event := repo.events[0]
	if event.ActorUserID == nil || *event.ActorUserID != 42 {
		t.Fatalf("actor = %v", event.ActorUserID)
	}
	if event.ActorRole != string(domain.RoleRegistrar) {
		t.Fatalf("actor role = %q", event.ActorRole)
	}
	if event.ObjectType != "enrollment" || event.ObjectID != "7" {
		t.Fatalf("object = %s/%s", event.ObjectType, event.ObjectID)
	}
	if event.RequestID != "req_audit" {
		t.Fatalf("request id = %q", event.RequestID)
	}
	if !event.OccurredAt.Equal(moment) {
		t.Fatalf("occurred_at = %s, want %s", event.OccurredAt, moment)
	}
}

func TestRecorderAcceptsBackgroundWorkWithoutAnActor(t *testing.T) {
	recorder := audit.NewRecorder(nil)
	repo := &captureRepo{}

	if err := recorder.Record(context.Background(), repo, audit.Entry{
		Action:     domain.ActionJobFailed,
		ObjectType: "job",
		ObjectID:   "12",
		Result:     domain.ResultFailure,
		Detail:     "retry budget spent",
	}); err != nil {
		t.Fatalf("recording failed: %v", err)
	}
	event := repo.events[0]
	if event.ActorUserID != nil || event.ActorRole != "" {
		t.Fatalf("background work must not claim an actor: %+v", event)
	}
	if event.RequestID != "" {
		t.Fatalf("request id = %q, want empty for background work", event.RequestID)
	}
	if event.OccurredAt.IsZero() {
		t.Fatal("the default time source must stamp the event")
	}
}

func TestRecorderPropagatesStorageFailures(t *testing.T) {
	sentinel := errors.New("audit table locked")
	recorder := audit.NewRecorder(nil)
	repo := &captureRepo{err: sentinel}

	err := recorder.Record(context.Background(), repo, audit.Entry{
		Action: domain.ActionLogin, ObjectType: "session", ObjectID: "1", Result: domain.ResultSuccess,
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("expected the storage error to surface, got %v", err)
	}
}

func TestRecorderRejectsMalformedEntries(t *testing.T) {
	recorder := audit.NewRecorder(nil)
	repo := &captureRepo{}

	err := recorder.Record(context.Background(), repo, audit.Entry{
		Action: domain.ActionLogin, ObjectType: "session", ObjectID: "1", Result: "unknown",
	})
	if err == nil {
		t.Fatal("an unknown result must be rejected")
	}
	if len(repo.events) != 0 {
		t.Fatal("a rejected entry must not reach the repository")
	}
}
