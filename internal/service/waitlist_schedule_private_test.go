package service_test

import (
	"context"
	"errors"
	"testing"

	"github.com/vance1852/orientation-enrollment-platform/internal/domain"
	"github.com/vance1852/orientation-enrollment-platform/internal/service"
)

// TestWaitlistedSectionStillOccupiesItsWeeklyBlock builds a term plan where the
// student only holds a waitlist position and then asks for a section that meets
// at an overlapping time. A queued position already reserves the weekly block,
// so the overlapping request must be refused and must leave no trace.
func TestWaitlistedSectionStillOccupiesItsWeeklyBlock(t *testing.T) {
	harness := newHarness(t)
	ctx := context.Background()

	// The Tuesday 10:00-11:40 section has a single seat. Give it away to another
	// verified student so the next applicant can only reach the waitlist.
	occupant := harness.createUser("tuesday-seat@campus.example", domain.RoleStudent)
	harness.grantPrerequisite(occupant.ID)
	harness.verifyRegistration(occupant.ID)
	seat, err := harness.enrollments.Claim(ctx, harness.principal(occupant),
		service.ClaimInput{StudentID: occupant.ID, SectionID: harness.tightID})
	if err != nil {
		t.Fatalf("handing the single seat to the first student failed: %v", err)
	}
	if seat.Enrollment.Status != domain.EnrollmentEnrolled {
		t.Fatalf("the first student must hold the seat, got status %s", seat.Enrollment.Status)
	}

	queued, err := harness.enrollments.Claim(ctx, harness.studentPrincipal(),
		service.ClaimInput{StudentID: harness.student.ID, SectionID: harness.tightID})
	if err != nil {
		t.Fatalf("queueing on the full section failed: %v", err)
	}
	if !queued.Waitlisted || queued.Enrollment.Status != domain.EnrollmentWaitlisted {
		t.Fatalf("expected a queued position on the full section, got %+v", queued.Enrollment)
	}

	// The Tuesday 10:30-12:00 section overlaps the block that is already queued.
	overlapping, overlapErr := harness.enrollments.Claim(ctx, harness.studentPrincipal(),
		service.ClaimInput{StudentID: harness.student.ID, SectionID: harness.clashID})
	if overlapErr == nil {
		t.Fatalf("the overlapping request was accepted as enrollment %d in status %s",
			overlapping.Enrollment.ID, overlapping.Enrollment.Status)
	}
	if !errors.Is(overlapErr, domain.ErrScheduleConflict) {
		t.Fatalf("expected the overlap to surface as a schedule conflict, got %v (code %q)",
			overlapErr, domain.Code(overlapErr))
	}

	overlappingSection := harness.section(harness.clashID)
	if overlappingSection.SeatsTaken != 0 {
		t.Fatalf("the refused request changed the overlapping section: seats_taken=%d",
			overlappingSection.SeatsTaken)
	}

	plan, err := harness.enrollments.List(ctx, harness.studentPrincipal(), domain.EnrollmentFilter{
		TermID: harness.term.ID,
		Page:   domain.Page{Size: 20},
	})
	if err != nil {
		t.Fatalf("reading the term plan of the queued student failed: %v", err)
	}
	if plan.Total != 1 {
		t.Fatalf("the queued student must keep exactly one term record, found %d", plan.Total)
	}
	for _, record := range plan.Items {
		if record.SectionID == harness.clashID {
			t.Fatalf("the overlapping section entered the term plan as %s", record.Status)
		}
	}
}
