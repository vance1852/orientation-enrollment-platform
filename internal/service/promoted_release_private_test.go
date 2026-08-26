package service_test

import (
	"context"
	"testing"

	"github.com/vance1852/orientation-enrollment-platform/internal/domain"
	"github.com/vance1852/orientation-enrollment-platform/internal/service"
)

// TestDropAfterWaitlistPromotionStillReleasesTheSeat opens the queued record
// once, lets the promotion hand over the freed seat, and then drops the course.
// The release must go through and free the seat again.
func TestDropAfterWaitlistPromotionStillReleasesTheSeat(t *testing.T) {
	counter := newHarness(t)
	ctx := context.Background()

	occupant := counter.enrollAnotherStudent("occupant@campus.example", counter.tightID)
	queued, err := counter.enrollments.Claim(ctx, counter.studentPrincipal(),
		service.ClaimInput{StudentID: counter.student.ID, SectionID: counter.tightID})
	if err != nil {
		t.Fatalf("queueing behind the occupant failed: %v", err)
	}

	viewed, err := counter.enrollments.Get(ctx, counter.studentPrincipal(), queued.Enrollment.ID)
	if err != nil {
		t.Fatalf("reading the queued record failed: %v", err)
	}
	if viewed.Status != domain.EnrollmentWaitlisted {
		t.Fatalf("the pre-promotion view reported status %s", viewed.Status)
	}

	if _, err := counter.enrollments.Drop(ctx, counter.registrarPrincipal(), occupant.ID,
		"transferred to another programme"); err != nil {
		t.Fatalf("releasing the occupied seat failed: %v", err)
	}
	promoted, err := counter.enrollments.PromoteWaitlist(ctx, counter.tightID)
	if err != nil {
		t.Fatalf("serving the waitlist failed: %v", err)
	}
	if !promoted {
		t.Fatalf("the freed seat was not handed to the queued student")
	}

	afterPromotion, err := counter.enrollments.List(ctx, counter.studentPrincipal(), domain.EnrollmentFilter{
		TermID:   counter.term.ID,
		Statuses: []domain.EnrollmentStatus{domain.EnrollmentEnrolled},
		Page:     domain.Page{Size: 20},
	})
	if err != nil {
		t.Fatalf("listing the promoted plan failed: %v", err)
	}
	if afterPromotion.Total != 1 || afterPromotion.Items[0].ID != queued.Enrollment.ID {
		t.Fatalf("the promotion did not land on the queued record, plan holds %d records", afterPromotion.Total)
	}

	released, err := counter.enrollments.Drop(ctx, counter.studentPrincipal(), queued.Enrollment.ID,
		"schedule no longer fits")
	if err != nil {
		t.Fatalf("dropping the promoted course failed: %v (code %q)", err, domain.Code(err))
	}
	if released.Status != domain.EnrollmentDropped {
		t.Fatalf("the released record reports status %s", released.Status)
	}
	if released.ReleasedAt == nil {
		t.Fatalf("the released record carries no release time")
	}

	section := counter.section(counter.tightID)
	if section.SeatsTaken != 0 {
		t.Fatalf("the section still reports %d taken seats after the drop", section.SeatsTaken)
	}

	remaining, err := counter.enrollments.List(ctx, counter.studentPrincipal(), domain.EnrollmentFilter{
		TermID:   counter.term.ID,
		Statuses: []domain.EnrollmentStatus{domain.EnrollmentEnrolled, domain.EnrollmentWaitlisted},
		Page:     domain.Page{Size: 20},
	})
	if err != nil {
		t.Fatalf("listing the plan after the release failed: %v", err)
	}
	if remaining.Total != 0 {
		t.Fatalf("the released course is still on the plan, %d records remain", remaining.Total)
	}
}
