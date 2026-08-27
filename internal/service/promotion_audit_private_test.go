package service_test

import (
	"context"
	"testing"

	"github.com/vance1852/orientation-enrollment-platform/internal/domain"
	"github.com/vance1852/orientation-enrollment-platform/internal/service"
)

// TestPromotionAuditMustFollowTheRealSeatMove frees a seat, then closes the
// section before the waitlist is served. The promotion cannot happen, so the
// trail must stay empty and the candidate must keep the queue position.
func TestPromotionAuditMustFollowTheRealSeatMove(t *testing.T) {
	desk := newHarness(t)
	ctx := context.Background()

	seatHolder := desk.enrollAnotherStudent("seat-holder@campus.example", desk.tightID)
	queued, err := desk.enrollments.Claim(ctx, desk.studentPrincipal(),
		service.ClaimInput{StudentID: desk.student.ID, SectionID: desk.tightID})
	if err != nil {
		t.Fatalf("queueing the second student failed: %v", err)
	}
	if !queued.Waitlisted || queued.Enrollment.WaitlistRank != 1 {
		t.Fatalf("expected a waitlist entry at rank 1, got waitlisted=%v rank=%d",
			queued.Waitlisted, queued.Enrollment.WaitlistRank)
	}

	if _, err := desk.enrollments.Drop(ctx, desk.registrarPrincipal(), seatHolder.ID,
		"left the programme"); err != nil {
		t.Fatalf("releasing the seat failed: %v", err)
	}
	if err := desk.store.SetSectionStatus(ctx, desk.tightID, domain.SectionClosed,
		desk.clock.Now()); err != nil {
		t.Fatalf("closing the section failed: %v", err)
	}

	promoted, err := desk.enrollments.PromoteWaitlist(ctx, desk.tightID)
	if err != nil {
		t.Fatalf("serving the waitlist of a closed section must not error, got %v", err)
	}
	if promoted {
		t.Fatalf("a closed section must not hand out the freed seat")
	}

	trail, err := desk.catalog.ListAuditEvents(ctx, desk.registrarPrincipal(), domain.AuditFilter{
		Action: string(domain.ActionEnrollmentPromote),
		Page:   domain.Page{Size: domain.MaxPageSize},
	})
	if err != nil {
		t.Fatalf("reading the promotion trail failed: %v", err)
	}
	for _, event := range trail.Items {
		if event.Result == domain.ResultSuccess {
			t.Fatalf("the trail claims a promotion that never happened: object %s/%s detail %q",
				event.ObjectType, event.ObjectID, event.Detail)
		}
	}

	stillQueued, err := desk.enrollments.Get(ctx, desk.studentPrincipal(), queued.Enrollment.ID)
	if err != nil {
		t.Fatalf("reading the queued record failed: %v", err)
	}
	if stillQueued.Status != domain.EnrollmentWaitlisted || stillQueued.WaitlistRank != 1 {
		t.Fatalf("the candidate moved to %s at rank %d without a seat",
			stillQueued.Status, stillQueued.WaitlistRank)
	}

	section := desk.section(desk.tightID)
	if section.SeatsTaken != 0 {
		t.Fatalf("the closed section reports %d taken seats", section.SeatsTaken)
	}
	if section.WaitlistLength != 1 {
		t.Fatalf("the waitlist length dropped to %d while the candidate is still queued",
			section.WaitlistLength)
	}
}
