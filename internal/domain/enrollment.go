package domain

import (
	"strings"
	"time"
)

// EnrollmentStatus is the seat lifecycle a student moves through.
type EnrollmentStatus string

// Enrollment lifecycle values.
const (
	EnrollmentPending    EnrollmentStatus = "pending"
	EnrollmentEnrolled   EnrollmentStatus = "enrolled"
	EnrollmentWaitlisted EnrollmentStatus = "waitlisted"
	EnrollmentDropped    EnrollmentStatus = "dropped"
	EnrollmentWithdrawn  EnrollmentStatus = "withdrawn"
	EnrollmentCompleted  EnrollmentStatus = "completed"
)

var enrollmentTransitions = map[EnrollmentStatus][]EnrollmentStatus{
	EnrollmentPending:    {EnrollmentEnrolled, EnrollmentWaitlisted, EnrollmentWithdrawn},
	EnrollmentEnrolled:   {EnrollmentDropped, EnrollmentCompleted},
	EnrollmentWaitlisted: {EnrollmentEnrolled, EnrollmentWithdrawn},
	EnrollmentDropped:    {},
	EnrollmentWithdrawn:  {},
	EnrollmentCompleted:  {},
}

// CanTransitionTo reports whether the seat may move to the target state.
func (s EnrollmentStatus) CanTransitionTo(target EnrollmentStatus) bool {
	for _, allowed := range enrollmentTransitions[s] {
		if allowed == target {
			return true
		}
	}
	return false
}

// HoldsSeat reports whether the status occupies a graded seat.
func (s EnrollmentStatus) HoldsSeat() bool {
	return s == EnrollmentEnrolled || s == EnrollmentCompleted
}

// Active reports whether the record still participates in the term, either as a
// seat holder or as a waitlist candidate.
func (s EnrollmentStatus) Active() bool {
	return s == EnrollmentPending || s == EnrollmentEnrolled || s == EnrollmentWaitlisted
}

// Terminal reports whether the record can no longer change.
func (s EnrollmentStatus) Terminal() bool {
	return len(enrollmentTransitions[s]) == 0
}

// Enrollment is one student's claim on one section.
type Enrollment struct {
	ID            int64
	StudentID     int64
	TermID        int64
	SectionID     int64
	CourseCode    string
	Credits       int
	Status        EnrollmentStatus
	WaitlistRank  int
	RequestedAt   time.Time
	DecidedAt     *time.Time
	ReleasedAt    *time.Time
	ReleaseReason string
	Version       int64
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// Transition applies a lifecycle move and stamps the derived timestamps.
func (e *Enrollment) Transition(target EnrollmentStatus, now time.Time, reason string) error {
	if !e.Status.CanTransitionTo(target) {
		return NewTransitionError("enrollment", string(e.Status), string(target))
	}
	stamp := now.UTC()
	switch target {
	case EnrollmentEnrolled:
		e.DecidedAt = &stamp
		e.WaitlistRank = 0
	case EnrollmentWaitlisted:
		e.DecidedAt = &stamp
	case EnrollmentDropped, EnrollmentWithdrawn:
		e.ReleasedAt = &stamp
		e.ReleaseReason = strings.TrimSpace(reason)
		e.WaitlistRank = 0
	case EnrollmentCompleted:
		e.DecidedAt = &stamp
	}
	e.Status = target
	e.UpdatedAt = stamp
	return nil
}

// SeatDelta reports how the section seat counter must change for a move from
// the current status to the target status.
func SeatDelta(from, to EnrollmentStatus) int {
	before := 0
	if from.HoldsSeat() {
		before = 1
	}
	after := 0
	if to.HoldsSeat() {
		after = 1
	}
	return after - before
}

// EnrollmentRequest is the validated input of a seat claim.
type EnrollmentRequest struct {
	StudentID int64
	SectionID int64
	Reason    string
}

// Validate checks the identifiers before any database work happens.
func (r EnrollmentRequest) Validate() error {
	if r.StudentID <= 0 {
		return NewFieldError("student_id", "must be a positive identifier")
	}
	if r.SectionID <= 0 {
		return NewFieldError("section_id", "must be a positive identifier")
	}
	return nil
}

// CreditLoad sums the credits of the records that still occupy the term plan.
func CreditLoad(records []Enrollment) int {
	total := 0
	for _, record := range records {
		if record.Status.Active() {
			total += record.Credits
		}
	}
	return total
}

// EligibilityInput carries everything the eligibility rules need. Keeping it in
// the domain package lets the rules be tested without a database.
type EligibilityInput struct {
	Now              time.Time
	Term             Term
	Section          Section
	Registration     Registration
	CoursePrereqs    []string
	CompletedCourses []AcademicRecord
	HeldEnrollments  []Enrollment
	HeldMeetings     []Meeting
}

// EligibilityDecision reports whether a seat claim may proceed and, when the
// section is full, whether the student should be waitlisted instead.
type EligibilityDecision struct {
	Waitlist     bool
	CreditsAfter int
}

// CheckEnrollmentEligibility runs every cross-entity rule in a fixed order so
// callers always receive the most actionable failure first.
func CheckEnrollmentEligibility(in EligibilityInput) (EligibilityDecision, error) {
	if !in.Term.EnrollmentOpen(in.Now) {
		return EligibilityDecision{}, ErrEnrollmentWindowClosed
	}
	if !in.Registration.Verified() {
		return EligibilityDecision{}, ErrRegistrationIncomplete
	}
	if !in.Section.AcceptsEnrollment() {
		return EligibilityDecision{}, NewConflictError(ErrConflict, "section "+in.Section.Code,
			"status "+string(in.Section.Status))
	}
	for _, held := range in.HeldEnrollments {
		if !held.Status.Active() {
			continue
		}
		if held.SectionID == in.Section.ID {
			return EligibilityDecision{}, ErrDuplicateEnrollment
		}
		if held.CourseCode != "" && held.CourseCode == in.Section.CourseCode {
			return EligibilityDecision{}, NewConflictError(ErrDuplicateEnrollment,
				"course "+in.Section.CourseCode, "section "+held.CourseCode)
		}
	}
	if missing := MissingPrerequisites(in.CoursePrereqs, in.CompletedCourses); len(missing) > 0 {
		return EligibilityDecision{}, NewConflictError(ErrPrerequisiteMissing,
			"course "+in.Section.CourseCode, strings.Join(missing, ","))
	}
	creditsAfter := CreditLoad(in.HeldEnrollments) + in.Section.CourseCredits
	if creditsAfter > in.Term.CreditLimit {
		return EligibilityDecision{}, ErrCreditLimitExceeded
	}
	if candidate, held, conflict := FindScheduleConflict(in.Section.Meetings, in.HeldMeetings); conflict {
		return EligibilityDecision{}, NewConflictError(ErrScheduleConflict,
			candidate.Label(), held.Label())
	}
	decision := EligibilityDecision{CreditsAfter: creditsAfter}
	if in.Section.SeatsAvailable() <= 0 {
		if in.Section.WaitlistLimit <= 0 || in.Section.WaitlistLength >= in.Section.WaitlistLimit {
			return EligibilityDecision{}, ErrWaitlistFull
		}
		decision.Waitlist = true
	}
	return decision, nil
}
