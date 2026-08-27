package service_test

import (
	"context"
	"testing"

	"github.com/vance1852/orientation-enrollment-platform/internal/domain"
	"github.com/vance1852/orientation-enrollment-platform/internal/service"
)

// TestWeekdayViewKeepsEveryMeetingBlockOfTheSection opens the daily schedule of
// one weekday and then checks that the other weekday of the very same course is
// still there, both on the detail view and on its own daily schedule.
func TestWeekdayViewKeepsEveryMeetingBlockOfTheSection(t *testing.T) {
	desk := newHarness(t)
	ctx := context.Background()

	foundation := desk.section(desk.openID)
	labID, err := desk.store.ProvisionSection(ctx, domain.Section{
		TermID: desk.term.ID, CourseID: foundation.CourseID, Code: "CS110-LAB",
		Status: domain.SectionOpen, Capacity: 5, Instructor: "Prof. Meng",
		Meetings: []domain.Meeting{
			{Weekday: domain.Wednesday, StartMinute: 480, EndMinute: 580, Room: "L101"},
			{Weekday: domain.Thursday, StartMinute: 600, EndMinute: 700, Room: "L102"},
		},
	}, desk.clock.Now())
	if err != nil {
		t.Fatalf("provisioning the lab section failed: %v", err)
	}

	if _, err := desk.enrollments.Claim(ctx, desk.studentPrincipal(),
		service.ClaimInput{StudentID: desk.student.ID, SectionID: labID}); err != nil {
		t.Fatalf("enrolling into the lab section failed: %v (code %q)", err, domain.Code(err))
	}

	detail, err := desk.catalog.GetSection(ctx, labID)
	if err != nil {
		t.Fatalf("reading the section detail failed: %v", err)
	}
	if len(detail.Meetings) != 2 {
		t.Fatalf("the section detail starts with %d blocks: %+v", len(detail.Meetings), detail.Meetings)
	}

	thursday, err := desk.catalog.WeeklyTimetable(ctx, desk.studentPrincipal(), desk.student.ID, domain.Thursday)
	if err != nil {
		t.Fatalf("reading the thursday schedule failed: %v", err)
	}
	if len(thursday) != 1 || len(thursday[0].Meetings) != 1 ||
		thursday[0].Meetings[0].Weekday != domain.Thursday {
		t.Fatalf("the thursday schedule is %+v", thursday)
	}

	wednesday, err := desk.catalog.WeeklyTimetable(ctx, desk.studentPrincipal(), desk.student.ID, domain.Wednesday)
	if err != nil {
		t.Fatalf("reading the wednesday schedule failed: %v", err)
	}
	if len(wednesday) != 1 {
		t.Fatalf("the wednesday schedule lost the lab course: %+v", wednesday)
	}
	if len(wednesday[0].Meetings) != 1 || wednesday[0].Meetings[0].Room != "L101" {
		t.Fatalf("the wednesday blocks are %+v", wednesday[0].Meetings)
	}

	again, err := desk.catalog.GetSection(ctx, labID)
	if err != nil {
		t.Fatalf("re-reading the section detail failed: %v", err)
	}
	if len(again.Meetings) != 2 {
		t.Fatalf("the section detail now reports %d blocks: %+v", len(again.Meetings), again.Meetings)
	}
	rooms := map[string]int{}
	for _, meeting := range again.Meetings {
		rooms[meeting.Room]++
	}
	if rooms["L101"] != 1 || rooms["L102"] != 1 {
		t.Fatalf("the section detail blocks are %+v", again.Meetings)
	}
}
