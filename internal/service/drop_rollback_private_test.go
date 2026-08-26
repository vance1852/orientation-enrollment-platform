package service_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/vance1852/orientation-enrollment-platform/internal/audit"
	"github.com/vance1852/orientation-enrollment-platform/internal/domain"
	"github.com/vance1852/orientation-enrollment-platform/internal/platform/logging"
	"github.com/vance1852/orientation-enrollment-platform/internal/service"
)

// TestFailedReleaseMustNotQueueAPromotion lets a release fail on its way out and
// then checks that nothing downstream was scheduled: the queue, the seat counter
// and the occupied record all have to look untouched.
func TestFailedReleaseMustNotQueueAPromotion(t *testing.T) {
	office := newHarness(t)
	ctx := context.Background()

	occupant := office.enrollAnotherStudent("occupant@campus.example", office.tightID)
	waiting, err := office.enrollments.Claim(ctx, office.studentPrincipal(),
		service.ClaimInput{StudentID: office.student.ID, SectionID: office.tightID})
	if err != nil {
		t.Fatalf("queueing the second student failed: %v", err)
	}
	if !waiting.Waitlisted {
		t.Fatalf("the second student did not land on the waitlist")
	}

	outage := errors.New("audit sink offline")
	brittle, err := service.NewEnrollmentService(service.Deps{
		Store:  &failingAuditStore{Store: office.store, err: outage},
		Clock:  office.clock,
		Audit:  audit.NewRecorder(func() time.Time { return office.clock.Now() }),
		Logger: logging.Discard(),
	}, 0)
	if err != nil {
		t.Fatalf("building the release path failed: %v", err)
	}

	queuedBefore, err := office.store.Jobs().CountJobsByState(ctx, domain.JobQueued)
	if err != nil {
		t.Fatalf("counting queued jobs failed: %v", err)
	}

	if _, dropErr := brittle.Drop(ctx, office.registrarPrincipal(), occupant.ID,
		"registrar correction during an audit outage"); !errors.Is(dropErr, outage) {
		t.Fatalf("expected the release to fail with the outage, got %v", dropErr)
	}

	queuedAfter, err := office.store.Jobs().CountJobsByState(ctx, domain.JobQueued)
	if err != nil {
		t.Fatalf("counting queued jobs failed: %v", err)
	}
	if queuedAfter != queuedBefore {
		t.Fatalf("a failed release scheduled %d extra promotion jobs", queuedAfter-queuedBefore)
	}

	section := office.section(office.tightID)
	if section.SeatsTaken != 1 {
		t.Fatalf("the seat counter moved to %d although the release failed", section.SeatsTaken)
	}
	if section.WaitlistLength != 1 {
		t.Fatalf("the waitlist length moved to %d although the release failed", section.WaitlistLength)
	}

	kept, err := office.enrollments.Get(ctx, office.registrarPrincipal(), occupant.ID)
	if err != nil {
		t.Fatalf("reading the occupied record failed: %v", err)
	}
	if kept.Status != domain.EnrollmentEnrolled || kept.ReleasedAt != nil {
		t.Fatalf("the occupied record became %s with release time %v", kept.Status, kept.ReleasedAt)
	}
}
