package domain_test

import (
	"errors"
	"testing"
	"time"

	"github.com/vance1852/orientation-enrollment-platform/internal/domain"
)

func TestEnrollmentStateMachineRejectsIllegalMoves(t *testing.T) {
	legal := map[domain.EnrollmentStatus][]domain.EnrollmentStatus{
		domain.EnrollmentPending:    {domain.EnrollmentEnrolled, domain.EnrollmentWaitlisted, domain.EnrollmentWithdrawn},
		domain.EnrollmentEnrolled:   {domain.EnrollmentDropped, domain.EnrollmentCompleted},
		domain.EnrollmentWaitlisted: {domain.EnrollmentEnrolled, domain.EnrollmentWithdrawn},
	}
	all := []domain.EnrollmentStatus{
		domain.EnrollmentPending, domain.EnrollmentEnrolled, domain.EnrollmentWaitlisted,
		domain.EnrollmentDropped, domain.EnrollmentWithdrawn, domain.EnrollmentCompleted,
	}
	for _, from := range all {
		for _, to := range all {
			allowed := false
			for _, candidate := range legal[from] {
				if candidate == to {
					allowed = true
				}
			}
			if got := from.CanTransitionTo(to); got != allowed {
				t.Fatalf("CanTransitionTo(%s -> %s) = %v, want %v", from, to, got, allowed)
			}
		}
	}
	for _, terminal := range []domain.EnrollmentStatus{domain.EnrollmentDropped, domain.EnrollmentWithdrawn, domain.EnrollmentCompleted} {
		if !terminal.Terminal() {
			t.Fatalf("%s must be terminal", terminal)
		}
	}
}

func TestEnrollmentTransitionStampsLifecycleTimestamps(t *testing.T) {
	now := time.Date(2026, time.August, 26, 9, 30, 0, 0, time.UTC)
	enrollment := domain.Enrollment{Status: domain.EnrollmentPending, WaitlistRank: 0}

	if err := enrollment.Transition(domain.EnrollmentWaitlisted, now, ""); err != nil {
		t.Fatalf("waitlisting a pending claim failed: %v", err)
	}
	if enrollment.DecidedAt == nil || !enrollment.DecidedAt.Equal(now) {
		t.Fatalf("decided_at = %v, want %v", enrollment.DecidedAt, now)
	}

	enrollment.WaitlistRank = 3
	if err := enrollment.Transition(domain.EnrollmentEnrolled, now.Add(time.Hour), ""); err != nil {
		t.Fatalf("promoting a waitlisted claim failed: %v", err)
	}
	if enrollment.WaitlistRank != 0 {
		t.Fatalf("waitlist rank must be cleared on promotion, got %d", enrollment.WaitlistRank)
	}

	release := now.Add(2 * time.Hour)
	if err := enrollment.Transition(domain.EnrollmentDropped, release, "  changed my plan  "); err != nil {
		t.Fatalf("dropping an enrolled claim failed: %v", err)
	}
	if enrollment.ReleasedAt == nil || !enrollment.ReleasedAt.Equal(release) {
		t.Fatalf("released_at = %v, want %v", enrollment.ReleasedAt, release)
	}
	if enrollment.ReleaseReason != "changed my plan" {
		t.Fatalf("release reason = %q, want the trimmed reason", enrollment.ReleaseReason)
	}

	err := enrollment.Transition(domain.EnrollmentEnrolled, release, "")
	if !errors.Is(err, domain.ErrInvalidTransition) {
		t.Fatalf("expected ErrInvalidTransition from a terminal state, got %v", err)
	}
	var transitionErr *domain.TransitionError
	if !errors.As(err, &transitionErr) {
		t.Fatal("expected the error chain to carry a TransitionError")
	}
	if transitionErr.From != string(domain.EnrollmentDropped) || transitionErr.To != string(domain.EnrollmentEnrolled) {
		t.Fatalf("transition error reported %s -> %s", transitionErr.From, transitionErr.To)
	}
}

func TestSeatDeltaOnlyCountsGradedSeats(t *testing.T) {
	cases := []struct {
		from domain.EnrollmentStatus
		to   domain.EnrollmentStatus
		want int
	}{
		{domain.EnrollmentPending, domain.EnrollmentEnrolled, 1},
		{domain.EnrollmentPending, domain.EnrollmentWaitlisted, 0},
		{domain.EnrollmentWaitlisted, domain.EnrollmentEnrolled, 1},
		{domain.EnrollmentEnrolled, domain.EnrollmentDropped, -1},
		{domain.EnrollmentEnrolled, domain.EnrollmentCompleted, 0},
		{domain.EnrollmentWaitlisted, domain.EnrollmentWithdrawn, 0},
	}
	for _, tc := range cases {
		if got := domain.SeatDelta(tc.from, tc.to); got != tc.want {
			t.Fatalf("SeatDelta(%s, %s) = %d, want %d", tc.from, tc.to, got, tc.want)
		}
	}
}

func TestCreditLoadIgnoresReleasedRecords(t *testing.T) {
	records := []domain.Enrollment{
		{Status: domain.EnrollmentEnrolled, Credits: 4},
		{Status: domain.EnrollmentWaitlisted, Credits: 3},
		{Status: domain.EnrollmentPending, Credits: 2},
		{Status: domain.EnrollmentDropped, Credits: 4},
		{Status: domain.EnrollmentWithdrawn, Credits: 4},
		{Status: domain.EnrollmentCompleted, Credits: 4},
	}
	if got := domain.CreditLoad(records); got != 9 {
		t.Fatalf("CreditLoad() = %d, want 9", got)
	}
}

func TestEnrollmentRequestValidation(t *testing.T) {
	if err := (domain.EnrollmentRequest{StudentID: 1, SectionID: 2}).Validate(); err != nil {
		t.Fatalf("expected a valid request, got %v", err)
	}
	if err := (domain.EnrollmentRequest{SectionID: 2}).Validate(); !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("expected a validation error for a missing student, got %v", err)
	}
	if err := (domain.EnrollmentRequest{StudentID: 1}).Validate(); !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("expected a validation error for a missing section, got %v", err)
	}
}

// eligibilityFixture builds an input where every rule passes, so each test can
// break exactly one precondition.
func eligibilityFixture() domain.EligibilityInput {
	now := time.Date(2026, time.August, 26, 10, 0, 0, 0, time.UTC)
	return domain.EligibilityInput{
		Now: now,
		Term: domain.Term{
			ID:                 7,
			Code:               "2026-autumn",
			EnrollmentOpensAt:  now.Add(-24 * time.Hour),
			EnrollmentClosesAt: now.Add(24 * time.Hour),
			AddDropClosesAt:    now.Add(48 * time.Hour),
			CreditLimit:        10,
		},
		Section: domain.Section{
			ID:            42,
			TermID:        7,
			Code:          "CS210-A",
			CourseCode:    "CS210",
			CourseCredits: 4,
			Status:        domain.SectionOpen,
			Capacity:      2,
			SeatsTaken:    1,
			WaitlistLimit: 2,
			Meetings: []domain.Meeting{
				{Weekday: domain.Tuesday, StartMinute: 600, EndMinute: 700},
			},
		},
		Registration:  domain.Registration{StudentID: 5, TermID: 7, Status: domain.RegistrationVerified},
		CoursePrereqs: []string{"CS110"},
		CompletedCourses: []domain.AcademicRecord{
			{CourseCode: "CS110", Grade: "B", Credits: 4},
		},
		HeldEnrollments: []domain.Enrollment{
			{SectionID: 41, CourseCode: "MATH101", Status: domain.EnrollmentEnrolled, Credits: 4},
		},
		HeldMeetings: []domain.Meeting{
			{Weekday: domain.Monday, StartMinute: 480, EndMinute: 580},
		},
	}
}

func TestCheckEnrollmentEligibilityAcceptsAQualifiedStudent(t *testing.T) {
	decision, err := domain.CheckEnrollmentEligibility(eligibilityFixture())
	if err != nil {
		t.Fatalf("expected the claim to be eligible, got %v", err)
	}
	if decision.Waitlist {
		t.Fatal("a section with a free seat must not waitlist the student")
	}
	if decision.CreditsAfter != 8 {
		t.Fatalf("CreditsAfter = %d, want 8", decision.CreditsAfter)
	}
}

func TestCheckEnrollmentEligibilityRejectsEachPrecondition(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*domain.EligibilityInput)
		wantErr error
	}{
		{
			name:    "enrollment window already closed",
			mutate:  func(in *domain.EligibilityInput) { in.Now = in.Term.EnrollmentClosesAt },
			wantErr: domain.ErrEnrollmentWindowClosed,
		},
		{
			name:    "orientation paperwork not verified",
			mutate:  func(in *domain.EligibilityInput) { in.Registration.Status = domain.RegistrationSubmitted },
			wantErr: domain.ErrRegistrationIncomplete,
		},
		{
			name:    "section no longer open",
			mutate:  func(in *domain.EligibilityInput) { in.Section.Status = domain.SectionClosed },
			wantErr: domain.ErrConflict,
		},
		{
			name: "already holds the same section",
			mutate: func(in *domain.EligibilityInput) {
				in.HeldEnrollments = append(in.HeldEnrollments, domain.Enrollment{
					SectionID: in.Section.ID, CourseCode: in.Section.CourseCode,
					Status: domain.EnrollmentWaitlisted, Credits: 4,
				})
			},
			wantErr: domain.ErrDuplicateEnrollment,
		},
		{
			name: "already holds another section of the same course",
			mutate: func(in *domain.EligibilityInput) {
				in.HeldEnrollments = []domain.Enrollment{{
					SectionID: 99, CourseCode: in.Section.CourseCode,
					Status: domain.EnrollmentEnrolled, Credits: 4,
				}}
			},
			wantErr: domain.ErrDuplicateEnrollment,
		},
		{
			name:    "prerequisite missing",
			mutate:  func(in *domain.EligibilityInput) { in.CompletedCourses = nil },
			wantErr: domain.ErrPrerequisiteMissing,
		},
		{
			name: "prerequisite failed",
			mutate: func(in *domain.EligibilityInput) {
				in.CompletedCourses = []domain.AcademicRecord{{CourseCode: "CS110", Grade: "F"}}
			},
			wantErr: domain.ErrPrerequisiteMissing,
		},
		{
			name:    "credit ceiling reached",
			mutate:  func(in *domain.EligibilityInput) { in.Term.CreditLimit = 6 },
			wantErr: domain.ErrCreditLimitExceeded,
		},
		{
			name: "weekly schedule collides",
			mutate: func(in *domain.EligibilityInput) {
				in.HeldMeetings = []domain.Meeting{{Weekday: domain.Tuesday, StartMinute: 650, EndMinute: 750}}
			},
			wantErr: domain.ErrScheduleConflict,
		},
		{
			name: "waitlist already full",
			mutate: func(in *domain.EligibilityInput) {
				in.Section.SeatsTaken = in.Section.Capacity
				in.Section.WaitlistLength = in.Section.WaitlistLimit
			},
			wantErr: domain.ErrWaitlistFull,
		},
		{
			name: "section without a waitlist",
			mutate: func(in *domain.EligibilityInput) {
				in.Section.SeatsTaken = in.Section.Capacity
				in.Section.WaitlistLimit = 0
			},
			wantErr: domain.ErrWaitlistFull,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := eligibilityFixture()
			tc.mutate(&in)
			if _, err := domain.CheckEnrollmentEligibility(in); !errors.Is(err, tc.wantErr) {
				t.Fatalf("expected %v, got %v", tc.wantErr, err)
			}
		})
	}
}

func TestCheckEnrollmentEligibilityWaitlistsWhenSeatsAreGone(t *testing.T) {
	in := eligibilityFixture()
	in.Section.SeatsTaken = in.Section.Capacity
	in.Section.WaitlistLength = 1

	decision, err := domain.CheckEnrollmentEligibility(in)
	if err != nil {
		t.Fatalf("a full section with waitlist room must be accepted, got %v", err)
	}
	if !decision.Waitlist {
		t.Fatal("expected the student to be waitlisted")
	}
}

func TestScheduleConflictErrorNamesBothBlocks(t *testing.T) {
	in := eligibilityFixture()
	in.HeldMeetings = []domain.Meeting{{Weekday: domain.Tuesday, StartMinute: 650, EndMinute: 750}}

	_, err := domain.CheckEnrollmentEligibility(in)
	var conflict *domain.ConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("expected a ConflictError, got %v", err)
	}
	if conflict.Subject != "tuesday 10:00-11:40" || conflict.Colliding != "tuesday 10:50-12:30" {
		t.Fatalf("conflict reported %q against %q", conflict.Subject, conflict.Colliding)
	}
}
