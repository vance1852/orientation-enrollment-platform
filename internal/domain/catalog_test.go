package domain_test

import (
	"testing"
	"time"

	"github.com/vance1852/orientation-enrollment-platform/internal/domain"
)

func mustLocation(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(domain.BusinessLocationName)
	if err != nil {
		return time.FixedZone("CST", 8*60*60)
	}
	return loc
}

func TestTermEnrollmentWindowUsesCampusDeadlines(t *testing.T) {
	loc := mustLocation(t)
	opens := time.Date(2026, time.August, 20, 9, 0, 0, 0, loc)
	term := domain.Term{
		Code:               "2026-autumn",
		Name:               "Autumn",
		EnrollmentOpensAt:  opens,
		EnrollmentClosesAt: opens.Add(10 * 24 * time.Hour),
		AddDropClosesAt:    opens.Add(20 * 24 * time.Hour),
		CreditLimit:        18,
	}
	if err := term.Validate(); err != nil {
		t.Fatalf("expected a valid term, got %v", err)
	}

	cases := []struct {
		name        string
		now         time.Time
		wantOpen    bool
		wantCanDrop bool
	}{
		{"before the window", opens.Add(-time.Minute), false, false},
		{"first instant of the window", opens, true, true},
		{"inside the window", opens.Add(24 * time.Hour), true, true},
		{"closing instant", term.EnrollmentClosesAt, false, true},
		{"inside add drop only", term.EnrollmentClosesAt.Add(time.Hour), false, true},
		{"after add drop", term.AddDropClosesAt, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := term.EnrollmentOpen(tc.now); got != tc.wantOpen {
				t.Fatalf("EnrollmentOpen(%s) = %v, want %v", tc.now, got, tc.wantOpen)
			}
			if got := term.DropAllowed(tc.now); got != tc.wantCanDrop {
				t.Fatalf("DropAllowed(%s) = %v, want %v", tc.now, got, tc.wantCanDrop)
			}
		})
	}
}

func TestArchivedTermRejectsEveryOperation(t *testing.T) {
	now := time.Now()
	term := domain.Term{
		EnrollmentOpensAt:  now.Add(-time.Hour),
		EnrollmentClosesAt: now.Add(time.Hour),
		AddDropClosesAt:    now.Add(2 * time.Hour),
		CreditLimit:        12,
		Archived:           true,
	}
	if term.EnrollmentOpen(now) {
		t.Fatal("an archived term must not accept enrollments")
	}
	if term.DropAllowed(now) {
		t.Fatal("an archived term must not accept drops")
	}
}

func TestTermValidateRejectsInconsistentWindows(t *testing.T) {
	base := time.Date(2026, time.August, 20, 9, 0, 0, 0, time.UTC)
	cases := map[string]domain.Term{
		"missing code": {
			EnrollmentOpensAt: base, EnrollmentClosesAt: base.Add(time.Hour),
			AddDropClosesAt: base.Add(2 * time.Hour), CreditLimit: 10,
		},
		"non positive credit limit": {
			Code: "t", EnrollmentOpensAt: base, EnrollmentClosesAt: base.Add(time.Hour),
			AddDropClosesAt: base.Add(2 * time.Hour),
		},
		"closes before opens": {
			Code: "t", EnrollmentOpensAt: base, EnrollmentClosesAt: base.Add(-time.Hour),
			AddDropClosesAt: base.Add(2 * time.Hour), CreditLimit: 10,
		},
		"add drop before closes": {
			Code: "t", EnrollmentOpensAt: base, EnrollmentClosesAt: base.Add(2 * time.Hour),
			AddDropClosesAt: base.Add(time.Hour), CreditLimit: 10,
		},
	}
	for name, term := range cases {
		t.Run(name, func(t *testing.T) {
			if err := term.Validate(); err == nil {
				t.Fatal("expected the term definition to be rejected")
			}
		})
	}
}

func TestMeetingOverlapAllowsBackToBackClasses(t *testing.T) {
	morning := domain.Meeting{Weekday: domain.Monday, StartMinute: 8 * 60, EndMinute: 9 * 60}
	adjacent := domain.Meeting{Weekday: domain.Monday, StartMinute: 9 * 60, EndMinute: 10 * 60}
	overlapping := domain.Meeting{Weekday: domain.Monday, StartMinute: 8*60 + 30, EndMinute: 9*60 + 30}
	otherDay := domain.Meeting{Weekday: domain.Tuesday, StartMinute: 8 * 60, EndMinute: 9 * 60}

	if morning.Overlaps(adjacent) {
		t.Fatal("classes that touch at a boundary minute must not collide")
	}
	if !morning.Overlaps(overlapping) {
		t.Fatal("classes sharing minutes must collide")
	}
	if morning.Overlaps(otherDay) {
		t.Fatal("classes on different weekdays must not collide")
	}
	if !morning.Overlaps(morning) {
		t.Fatal("a block must collide with itself")
	}
}

func TestMeetingValidateRejectsMalformedIntervals(t *testing.T) {
	cases := map[string]domain.Meeting{
		"weekday out of range": {Weekday: domain.Weekday(9), StartMinute: 60, EndMinute: 120},
		"negative start":       {Weekday: domain.Monday, StartMinute: -1, EndMinute: 120},
		"empty interval":       {Weekday: domain.Monday, StartMinute: 120, EndMinute: 120},
		"end past midnight":    {Weekday: domain.Monday, StartMinute: 1430, EndMinute: 1500},
	}
	for name, meeting := range cases {
		t.Run(name, func(t *testing.T) {
			if err := meeting.Validate(); err == nil {
				t.Fatal("expected the meeting block to be rejected")
			}
		})
	}
	valid := domain.Meeting{Weekday: domain.Friday, StartMinute: 15 * 60, EndMinute: 16*60 + 30}
	if err := valid.Validate(); err != nil {
		t.Fatalf("expected a valid meeting block, got %v", err)
	}
	if label := valid.Label(); label != "friday 15:00-16:30" {
		t.Fatalf("Label() = %q, want %q", label, "friday 15:00-16:30")
	}
}

func TestFindScheduleConflictReportsCollidingPair(t *testing.T) {
	candidate := []domain.Meeting{
		{Weekday: domain.Monday, StartMinute: 8 * 60, EndMinute: 9 * 60},
		{Weekday: domain.Wednesday, StartMinute: 10 * 60, EndMinute: 11 * 60},
	}
	held := []domain.Meeting{
		{Weekday: domain.Tuesday, StartMinute: 8 * 60, EndMinute: 9 * 60},
		{Weekday: domain.Wednesday, StartMinute: 10*60 + 30, EndMinute: 12 * 60},
	}
	got, collided, conflict := domain.FindScheduleConflict(candidate, held)
	if !conflict {
		t.Fatal("expected a schedule conflict")
	}
	if got.Weekday != domain.Wednesday || collided.Weekday != domain.Wednesday {
		t.Fatalf("expected the Wednesday pair, got %s and %s", got.Label(), collided.Label())
	}

	if _, _, conflict := domain.FindScheduleConflict(candidate, nil); conflict {
		t.Fatal("an empty held schedule cannot conflict")
	}
}

func TestSectionSeatAccountingAndMeetingIsolation(t *testing.T) {
	section := domain.Section{
		Code:       "CS210-A",
		Status:     domain.SectionOpen,
		Capacity:   3,
		SeatsTaken: 3,
		Meetings: []domain.Meeting{
			{Weekday: domain.Tuesday, StartMinute: 600, EndMinute: 700, Room: "E301"},
		},
	}
	if section.SeatsAvailable() != 0 {
		t.Fatalf("SeatsAvailable() = %d, want 0", section.SeatsAvailable())
	}
	if !section.AcceptsEnrollment() {
		t.Fatal("an open section must accept enrollment attempts")
	}
	section.SeatsTaken = 4
	if section.SeatsAvailable() != 0 {
		t.Fatalf("SeatsAvailable() must clamp at zero, got %d", section.SeatsAvailable())
	}
	for _, status := range []domain.SectionStatus{domain.SectionDraft, domain.SectionClosed, domain.SectionCancelled} {
		section.Status = status
		if section.AcceptsEnrollment() {
			t.Fatalf("section in status %s must not accept enrollment", status)
		}
	}

	clone := section.CloneMeetings()
	clone[0].Room = "mutated"
	if section.Meetings[0].Room != "E301" {
		t.Fatal("CloneMeetings must hand out an isolated copy")
	}
	var empty domain.Section
	if empty.CloneMeetings() != nil {
		t.Fatal("cloning an empty schedule must return nil")
	}
}

func TestSortMeetingsOrdersByWeekdayThenStart(t *testing.T) {
	meetings := []domain.Meeting{
		{Weekday: domain.Wednesday, StartMinute: 600, EndMinute: 700},
		{Weekday: domain.Monday, StartMinute: 800, EndMinute: 900},
		{Weekday: domain.Monday, StartMinute: 480, EndMinute: 540},
		{Weekday: domain.Monday, StartMinute: 480, EndMinute: 520},
	}
	domain.SortMeetings(meetings)
	want := []string{
		"monday 08:00-08:40",
		"monday 08:00-09:00",
		"monday 13:20-15:00",
		"wednesday 10:00-11:40",
	}
	for i, label := range want {
		if got := meetings[i].Label(); got != label {
			t.Fatalf("meeting %d = %q, want %q", i, got, label)
		}
	}
}

func TestWeekdayStringFallsBackForUnknownValues(t *testing.T) {
	if domain.Monday.String() != "monday" {
		t.Fatalf("Monday.String() = %q", domain.Monday.String())
	}
	unknown := domain.Weekday(12)
	if unknown.Valid() {
		t.Fatal("weekday 12 must be invalid")
	}
	if got := unknown.String(); got != "weekday(12)" {
		t.Fatalf("unknown weekday rendered as %q", got)
	}
}

func TestCourseValidateBoundsCredits(t *testing.T) {
	if err := (domain.Course{Code: "CS110", Credits: 4}).Validate(); err != nil {
		t.Fatalf("expected a valid course, got %v", err)
	}
	if err := (domain.Course{Credits: 4}).Validate(); err == nil {
		t.Fatal("a course without a code must be rejected")
	}
	if err := (domain.Course{Code: "CS110", Credits: 0}).Validate(); err == nil {
		t.Fatal("a course without credits must be rejected")
	}
	if err := (domain.Course{Code: "CS110", Credits: 13}).Validate(); err == nil {
		t.Fatal("a course above the credit ceiling must be rejected")
	}
}
