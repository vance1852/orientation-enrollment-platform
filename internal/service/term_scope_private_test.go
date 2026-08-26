package service_test

import (
	"context"
	"testing"
	"time"

	"github.com/vance1852/orientation-enrollment-platform/internal/domain"
	"github.com/vance1852/orientation-enrollment-platform/internal/service"
)

// TestEarlierTermLoadDoesNotBlockTheNewTerm gives the student a live record in a
// previous term and then asks for a modest section in the current term. The
// credit ceiling belongs to one term, so the older record must not be counted.
func TestEarlierTermLoadDoesNotBlockTheNewTerm(t *testing.T) {
	board := newHarness(t)
	ctx := context.Background()
	moment := board.clock.Now()

	previousTermID, err := board.store.ProvisionTerm(ctx, domain.Term{
		Code:               "2025-autumn",
		Name:               "Previous autumn term",
		EnrollmentOpensAt:  moment.Add(-400 * 24 * time.Hour),
		EnrollmentClosesAt: moment.Add(-380 * 24 * time.Hour),
		AddDropClosesAt:    moment.Add(-370 * 24 * time.Hour),
		CreditLimit:        18,
	})
	if err != nil {
		t.Fatalf("provisioning the previous term failed: %v", err)
	}
	capstone, err := board.store.Catalog().FindCourseByCode(ctx, "ORI900")
	if err != nil {
		t.Fatalf("reading the heavy capstone course failed: %v", err)
	}
	previousSectionID, err := board.store.ProvisionSection(ctx, domain.Section{
		TermID:     previousTermID,
		CourseID:   capstone.ID,
		Code:       "ORI900-P",
		Status:     domain.SectionClosed,
		Capacity:   30,
		Instructor: "Student Life Office",
	}, moment.Add(-390*24*time.Hour))
	if err != nil {
		t.Fatalf("provisioning the previous section failed: %v", err)
	}
	if _, err := board.store.Enrollments().CreateEnrollment(ctx, domain.Enrollment{
		StudentID:   board.student.ID,
		TermID:      previousTermID,
		SectionID:   previousSectionID,
		CourseCode:  capstone.Code,
		Credits:     capstone.Credits,
		Status:      domain.EnrollmentEnrolled,
		RequestedAt: moment.Add(-385 * 24 * time.Hour),
		CreatedAt:   moment.Add(-385 * 24 * time.Hour),
		UpdatedAt:   moment.Add(-385 * 24 * time.Hour),
	}); err != nil {
		t.Fatalf("seeding the previous term record failed: %v", err)
	}

	claimed, err := board.enrollments.Claim(ctx, board.studentPrincipal(),
		service.ClaimInput{StudentID: board.student.ID, SectionID: board.tightID})
	if err != nil {
		t.Fatalf("a four credit section in the new term must be reachable, got %v (code %q)",
			err, domain.Code(err))
	}
	if claimed.Enrollment.Status != domain.EnrollmentEnrolled {
		t.Fatalf("the new term request ended in status %s", claimed.Enrollment.Status)
	}
	if claimed.Enrollment.TermID != board.term.ID {
		t.Fatalf("the new record was attached to term %d instead of %d",
			claimed.Enrollment.TermID, board.term.ID)
	}
	if claimed.Section.SeatsTaken != 1 {
		t.Fatalf("the new term section reports %d taken seats", claimed.Section.SeatsTaken)
	}

	currentPlan, err := board.enrollments.List(ctx, board.studentPrincipal(), domain.EnrollmentFilter{
		TermID: board.term.ID,
		Page:   domain.Page{Size: 20},
	})
	if err != nil {
		t.Fatalf("reading the current term plan failed: %v", err)
	}
	if currentPlan.Total != 1 {
		t.Fatalf("the current term plan must hold exactly the new record, found %d", currentPlan.Total)
	}
	if currentPlan.Items[0].SectionID != board.tightID {
		t.Fatalf("the current term plan points at section %d", currentPlan.Items[0].SectionID)
	}
}
