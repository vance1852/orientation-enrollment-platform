package service_test

import (
	"context"
	"testing"

	"github.com/vance1852/orientation-enrollment-platform/internal/domain"
)

// TestAcceptedBatchItemSurvivesARejectedSibling submits one usable section
// together with an identifier that belongs to no section, then checks that the
// accepted item is really stored instead of only being reported.
func TestAcceptedBatchItemSurvivesARejectedSibling(t *testing.T) {
	stage := newHarness(t)
	ctx := context.Background()
	actor := stage.studentPrincipal()
	const unknownSectionID = int64(987654)

	results, err := stage.enrollments.BatchClaim(ctx, actor, stage.student.ID,
		[]int64{stage.heavyID, unknownSectionID})
	if err != nil {
		t.Fatalf("a plan that mixes one usable and one unknown section must still answer: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("the plan must report both submitted items, got %d", len(results))
	}

	var accepted, refused *domain.BatchItemResult
	for index := range results {
		if results[index].SectionID == stage.heavyID {
			accepted = &results[index]
			continue
		}
		refused = &results[index]
	}
	if accepted == nil || !accepted.Succeeded || accepted.Status != domain.EnrollmentEnrolled {
		t.Fatalf("the usable section must be reported as taken, got %+v", accepted)
	}
	if refused == nil || refused.Succeeded || refused.Code != "not_found" {
		t.Fatalf("the unknown section must be reported as missing, got %+v", refused)
	}

	stored, err := stage.enrollments.List(ctx, actor, domain.EnrollmentFilter{
		TermID: stage.term.ID,
		Page:   domain.Page{Size: 20},
	})
	if err != nil {
		t.Fatalf("reading back the stored term plan failed: %v", err)
	}
	if stored.Total != 1 {
		t.Fatalf("the accepted item must be persisted exactly once, found %d stored records", stored.Total)
	}
	if stored.Items[0].SectionID != stage.heavyID {
		t.Fatalf("the stored record points at section %d instead of the accepted one",
			stored.Items[0].SectionID)
	}
	if stored.Items[0].Status != domain.EnrollmentEnrolled {
		t.Fatalf("the stored record ended in status %s", stored.Items[0].Status)
	}

	section := stage.section(stage.heavyID)
	if section.SeatsTaken != 1 {
		t.Fatalf("the accepted item must keep its seat, seats_taken=%d", section.SeatsTaken)
	}
}
