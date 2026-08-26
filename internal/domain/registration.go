package domain

import (
	"strings"
	"time"
)

// RegistrationStatus is the orientation paperwork state machine. A student can
// only claim seats once a registrar has verified the submitted record.
type RegistrationStatus string

// Registration lifecycle values.
const (
	RegistrationDraft     RegistrationStatus = "draft"
	RegistrationSubmitted RegistrationStatus = "submitted"
	RegistrationVerified  RegistrationStatus = "verified"
	RegistrationRejected  RegistrationStatus = "rejected"
)

var registrationTransitions = map[RegistrationStatus][]RegistrationStatus{
	RegistrationDraft:     {RegistrationSubmitted},
	RegistrationSubmitted: {RegistrationVerified, RegistrationRejected},
	RegistrationVerified:  {},
	RegistrationRejected:  {RegistrationSubmitted},
}

// CanTransitionTo reports whether the paperwork may move to the target state.
func (s RegistrationStatus) CanTransitionTo(target RegistrationStatus) bool {
	for _, allowed := range registrationTransitions[s] {
		if allowed == target {
			return true
		}
	}
	return false
}

// Terminal reports whether no further transition is possible.
func (s RegistrationStatus) Terminal() bool {
	return len(registrationTransitions[s]) == 0
}

// Registration is the per-term orientation record of one student.
type Registration struct {
	ID              int64
	StudentID       int64
	TermID          int64
	Status          RegistrationStatus
	ProgramCode     string
	AdvisorEmail    string
	DormPreference  string
	SubmittedAt     *time.Time
	DecidedAt       *time.Time
	DecidedByUserID *int64
	DecisionNote    string
	Version         int64
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// Verified reports whether the student cleared orientation paperwork.
func (r Registration) Verified() bool { return r.Status == RegistrationVerified }

// ValidateSubmission checks the payload a student sends when submitting.
func (r Registration) ValidateSubmission() error {
	if strings.TrimSpace(r.ProgramCode) == "" {
		return NewFieldError("program_code", "must not be empty")
	}
	if len(r.ProgramCode) > 32 {
		return NewFieldError("program_code", "must be at most 32 characters")
	}
	if _, err := NormalizeEmail(r.AdvisorEmail); err != nil {
		return NewFieldError("advisor_email", "must be a valid email address")
	}
	switch strings.ToLower(strings.TrimSpace(r.DormPreference)) {
	case "on_campus", "off_campus", "undecided":
	default:
		return NewFieldError("dorm_preference", "must be on_campus, off_campus or undecided")
	}
	return nil
}

// Submit moves the paperwork into the submitted state.
func (r *Registration) Submit(now time.Time) error {
	if !r.Status.CanTransitionTo(RegistrationSubmitted) {
		return NewTransitionError("registration", string(r.Status), string(RegistrationSubmitted))
	}
	if err := r.ValidateSubmission(); err != nil {
		return err
	}
	r.Status = RegistrationSubmitted
	submitted := now.UTC()
	r.SubmittedAt = &submitted
	r.DecidedAt = nil
	r.DecidedByUserID = nil
	r.DecisionNote = ""
	r.UpdatedAt = now.UTC()
	return nil
}

// Decide applies a registrar decision to submitted paperwork.
func (r *Registration) Decide(target RegistrationStatus, decidedBy int64, note string, now time.Time) error {
	if target != RegistrationVerified && target != RegistrationRejected {
		return NewFieldError("status", "decision must be verified or rejected")
	}
	if !r.Status.CanTransitionTo(target) {
		return NewTransitionError("registration", string(r.Status), string(target))
	}
	if target == RegistrationRejected && strings.TrimSpace(note) == "" {
		return NewFieldError("decision_note", "must explain a rejection")
	}
	r.Status = target
	decided := now.UTC()
	r.DecidedAt = &decided
	r.DecidedByUserID = &decidedBy
	r.DecisionNote = strings.TrimSpace(note)
	r.UpdatedAt = now.UTC()
	return nil
}

// AcademicRecord is a completed course used to evaluate prerequisites.
type AcademicRecord struct {
	ID          int64
	StudentID   int64
	CourseCode  string
	Grade       string
	Credits     int
	CompletedAt time.Time
}

// Passing reports whether the record satisfies a prerequisite requirement.
func (a AcademicRecord) Passing() bool {
	switch strings.ToUpper(strings.TrimSpace(a.Grade)) {
	case "A", "B", "C", "D", "P":
		return true
	default:
		return false
	}
}

// MissingPrerequisites returns the required course codes that the completed
// record set does not satisfy, preserving the catalogue order.
func MissingPrerequisites(required []string, completed []AcademicRecord) []string {
	passed := make(map[string]struct{}, len(completed))
	for _, record := range completed {
		if record.Passing() {
			passed[strings.ToUpper(strings.TrimSpace(record.CourseCode))] = struct{}{}
		}
	}
	var missing []string
	for _, code := range required {
		normalized := strings.ToUpper(strings.TrimSpace(code))
		if normalized == "" {
			continue
		}
		if _, ok := passed[normalized]; !ok {
			missing = append(missing, normalized)
		}
	}
	return missing
}
