package service_test

import (
	"context"
	"errors"
	"testing"

	"github.com/vance1852/orientation-enrollment-platform/internal/domain"
	"github.com/vance1852/orientation-enrollment-platform/internal/service"
)

// TestAbandonedDropLeavesTheSeatUntouched cancels the caller before asking for a
// seat release and checks that the platform neither reports success nor changes
// the enrollment, the section counters or the background queue.
func TestAbandonedDropLeavesTheSeatUntouched(t *testing.T) {
	desk := newHarness(t)
	live := context.Background()
	actor := desk.studentPrincipal()

	taken, err := desk.enrollments.Claim(live, actor,
		service.ClaimInput{StudentID: desk.student.ID, SectionID: desk.openID})
	if err != nil {
		t.Fatalf("taking the seat that will be released later failed: %v", err)
	}
	before := desk.section(desk.openID)
	if before.SeatsTaken != 1 {
		t.Fatalf("the fixture must start with one occupied seat, got %d", before.SeatsTaken)
	}
	queuedBefore, err := desk.store.Jobs().CountJobsByState(live, domain.JobQueued)
	if err != nil {
		t.Fatalf("counting the background queue failed: %v", err)
	}

	abandoned, giveUp := context.WithCancel(live)
	giveUp()

	released, dropErr := desk.enrollments.Drop(abandoned, actor, taken.Enrollment.ID, "closed the browser")
	if dropErr == nil {
		t.Fatalf("an abandoned caller must not complete the release, got record %d in status %s",
			released.ID, released.Status)
	}
	if !errors.Is(dropErr, context.Canceled) {
		t.Fatalf("expected the abandoned call to report cancellation, got %v (code %q)",
			dropErr, domain.Code(dropErr))
	}

	kept, err := desk.enrollments.Get(live, actor, taken.Enrollment.ID)
	if err != nil {
		t.Fatalf("reading the enrollment back failed: %v", err)
	}
	if kept.Status != domain.EnrollmentEnrolled {
		t.Fatalf("the abandoned call moved the enrollment to %s", kept.Status)
	}
	if kept.ReleasedAt != nil {
		t.Fatalf("the abandoned call stamped a release time: %v", kept.ReleasedAt)
	}

	after := desk.section(desk.openID)
	if after.SeatsTaken != before.SeatsTaken {
		t.Fatalf("the seat counter moved from %d to %d without a completed release",
			before.SeatsTaken, after.SeatsTaken)
	}
	queuedAfter, err := desk.store.Jobs().CountJobsByState(live, domain.JobQueued)
	if err != nil {
		t.Fatalf("counting the background queue failed: %v", err)
	}
	if queuedAfter != queuedBefore {
		t.Fatalf("the abandoned call left %d background jobs behind", queuedAfter-queuedBefore)
	}
}
