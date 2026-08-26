package service_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/vance1852/orientation-enrollment-platform/internal/domain"
	"github.com/vance1852/orientation-enrollment-platform/internal/service"
)

// TestTwoStudentsSharingOneRequestKeyBothGetTheirSeat lets two different
// students submit the same section with the same client supplied request key,
// which is what the campus app does when it reuses a fixed key per screen.
func TestTwoStudentsSharingOneRequestKeyBothGetTheirSeat(t *testing.T) {
	hall := newHarness(t)
	ctx := context.Background()
	const sharedKey = "campus-app-enroll"

	submit := func(actor domain.Principal, studentID int64, sectionID int64) (service.Outcome, error) {
		payload := fmt.Sprintf("{\"section_id\":%d}", sectionID)
		return hall.idempotency.Execute(ctx, service.Scope{
			ActorUserID: actor.UserID,
			Method:      "POST",
			Path:        "/api/v1/enrollments",
			Key:         sharedKey,
			Payload:     payload,
		}, func(ctx context.Context) (int, string, error) {
			claimed, err := hall.enrollments.Claim(ctx, actor,
				service.ClaimInput{StudentID: studentID, SectionID: sectionID})
			if err != nil {
				return 0, "", err
			}
			body, err := json.Marshal(map[string]any{
				"enrollment_id": claimed.Enrollment.ID,
				"student_id":    claimed.Enrollment.StudentID,
				"section_id":    claimed.Enrollment.SectionID,
				"status":        string(claimed.Enrollment.Status),
			})
			if err != nil {
				return 0, "", err
			}
			return 201, string(body), nil
		})
	}

	first, err := submit(hall.studentPrincipal(), hall.student.ID, hall.openID)
	if err != nil {
		t.Fatalf("the first student could not enroll: %v", err)
	}
	if first.Replayed || first.Status != 201 {
		t.Fatalf("the first submission looks replayed: %+v", first)
	}

	second, err := submit(hall.principal(hall.otherStudnt), hall.otherStudnt.ID, hall.openID)
	if err != nil {
		t.Fatalf("the second student could not enroll: %v (code %q)", err, domain.Code(err))
	}
	if second.Replayed {
		t.Fatalf("the second student received a replayed answer: %+v", second)
	}
	if second.Body == first.Body {
		t.Fatalf("both students received the same response body: %s", second.Body)
	}

	ownPlan, err := hall.enrollments.List(ctx, hall.principal(hall.otherStudnt), domain.EnrollmentFilter{
		TermID: hall.term.ID,
		Page:   domain.Page{Size: 20},
	})
	if err != nil {
		t.Fatalf("listing the second student's plan failed: %v", err)
	}
	if ownPlan.Total != 1 {
		t.Fatalf("the second student holds %d records instead of one", ownPlan.Total)
	}
	if ownPlan.Items[0].SectionID != hall.openID || ownPlan.Items[0].Status != domain.EnrollmentEnrolled {
		t.Fatalf("the second student's record is %+v", ownPlan.Items[0])
	}

	section := hall.section(hall.openID)
	if section.SeatsTaken != 2 {
		t.Fatalf("the section reports %d taken seats for two enrolled students", section.SeatsTaken)
	}

	firstPlan, err := hall.enrollments.List(ctx, hall.studentPrincipal(), domain.EnrollmentFilter{
		TermID: hall.term.ID,
		Page:   domain.Page{Size: 20},
	})
	if err != nil {
		t.Fatalf("listing the first student's plan failed: %v", err)
	}
	if firstPlan.Total != 1 || firstPlan.Items[0].Status != domain.EnrollmentEnrolled {
		t.Fatalf("the first student's plan changed: %+v", firstPlan.Items)
	}
}
