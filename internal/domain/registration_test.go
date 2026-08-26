package domain_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/vance1852/orientation-enrollment-platform/internal/domain"
)

func TestRegistrationStateMachine(t *testing.T) {
	cases := []struct {
		from    domain.RegistrationStatus
		to      domain.RegistrationStatus
		allowed bool
	}{
		{domain.RegistrationDraft, domain.RegistrationSubmitted, true},
		{domain.RegistrationDraft, domain.RegistrationVerified, false},
		{domain.RegistrationSubmitted, domain.RegistrationVerified, true},
		{domain.RegistrationSubmitted, domain.RegistrationRejected, true},
		{domain.RegistrationSubmitted, domain.RegistrationSubmitted, false},
		{domain.RegistrationRejected, domain.RegistrationSubmitted, true},
		{domain.RegistrationVerified, domain.RegistrationSubmitted, false},
		{domain.RegistrationVerified, domain.RegistrationRejected, false},
	}
	for _, tc := range cases {
		if got := tc.from.CanTransitionTo(tc.to); got != tc.allowed {
			t.Fatalf("CanTransitionTo(%s -> %s) = %v, want %v", tc.from, tc.to, got, tc.allowed)
		}
	}
	if !domain.RegistrationVerified.Terminal() {
		t.Fatal("a verified registration must be terminal")
	}
	if domain.RegistrationRejected.Terminal() {
		t.Fatal("a rejected registration must allow resubmission")
	}
}

func TestRegistrationSubmitValidatesThePayload(t *testing.T) {
	now := time.Date(2026, time.August, 26, 8, 0, 0, 0, time.UTC)
	base := domain.Registration{
		StudentID:      5,
		TermID:         7,
		Status:         domain.RegistrationDraft,
		ProgramCode:    "CS-BSC",
		AdvisorEmail:   "advisor@campus.example",
		DormPreference: "on_campus",
	}

	valid := base
	if err := valid.Submit(now); err != nil {
		t.Fatalf("expected the submission to be accepted, got %v", err)
	}
	if valid.Status != domain.RegistrationSubmitted {
		t.Fatalf("status = %s, want submitted", valid.Status)
	}
	if valid.SubmittedAt == nil || !valid.SubmittedAt.Equal(now) {
		t.Fatalf("submitted_at = %v, want %v", valid.SubmittedAt, now)
	}

	cases := map[string]func(r *domain.Registration){
		"empty program":     func(r *domain.Registration) { r.ProgramCode = "  " },
		"long program":      func(r *domain.Registration) { r.ProgramCode = strings.Repeat("x", 33) },
		"bad advisor email": func(r *domain.Registration) { r.AdvisorEmail = "not-an-email" },
		"bad dorm choice":   func(r *domain.Registration) { r.DormPreference = "tent" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			candidate := base
			mutate(&candidate)
			if err := candidate.Submit(now); !errors.Is(err, domain.ErrValidation) {
				t.Fatalf("expected a validation error, got %v", err)
			}
			if candidate.Status != domain.RegistrationDraft {
				t.Fatalf("a rejected submission must not change the status, got %s", candidate.Status)
			}
		})
	}
}

func TestRegistrationResubmissionClearsThePreviousDecision(t *testing.T) {
	now := time.Date(2026, time.August, 26, 8, 0, 0, 0, time.UTC)
	registration := domain.Registration{
		StudentID: 5, TermID: 7, Status: domain.RegistrationDraft,
		ProgramCode: "CS-BSC", AdvisorEmail: "advisor@campus.example", DormPreference: "undecided",
	}
	if err := registration.Submit(now); err != nil {
		t.Fatalf("first submission failed: %v", err)
	}
	if err := registration.Decide(domain.RegistrationRejected, 9, "missing transcript", now.Add(time.Hour)); err != nil {
		t.Fatalf("rejection failed: %v", err)
	}
	if registration.DecidedByUserID == nil || *registration.DecidedByUserID != 9 {
		t.Fatalf("decided_by = %v, want 9", registration.DecidedByUserID)
	}
	if registration.DecisionNote != "missing transcript" {
		t.Fatalf("decision note = %q", registration.DecisionNote)
	}

	if err := registration.Submit(now.Add(2 * time.Hour)); err != nil {
		t.Fatalf("resubmission after a rejection must be allowed, got %v", err)
	}
	if registration.DecidedAt != nil || registration.DecidedByUserID != nil || registration.DecisionNote != "" {
		t.Fatal("resubmission must clear the previous decision")
	}
}

func TestRegistrationDecideGuardsInputs(t *testing.T) {
	now := time.Date(2026, time.August, 26, 8, 0, 0, 0, time.UTC)
	submitted := domain.Registration{Status: domain.RegistrationSubmitted}

	candidate := submitted
	if err := candidate.Decide(domain.RegistrationDraft, 1, "note", now); !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("only verified or rejected may be decided, got %v", err)
	}

	candidate = submitted
	if err := candidate.Decide(domain.RegistrationRejected, 1, "   ", now); !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("a rejection needs a note, got %v", err)
	}

	candidate = submitted
	if err := candidate.Decide(domain.RegistrationVerified, 1, "", now); err != nil {
		t.Fatalf("verification without a note must be allowed, got %v", err)
	}
	if !candidate.Verified() {
		t.Fatal("expected the record to report verified")
	}

	if err := candidate.Decide(domain.RegistrationRejected, 1, "changed my mind", now); !errors.Is(err, domain.ErrInvalidTransition) {
		t.Fatalf("a verified record must not be re-decided, got %v", err)
	}
}

func TestAcademicRecordPassingGrades(t *testing.T) {
	passing := []string{"A", "b", " C ", "D", "P"}
	for _, grade := range passing {
		if !(domain.AcademicRecord{Grade: grade}).Passing() {
			t.Fatalf("grade %q must count as passing", grade)
		}
	}
	for _, grade := range []string{"F", "W", "I", ""} {
		if (domain.AcademicRecord{Grade: grade}).Passing() {
			t.Fatalf("grade %q must not count as passing", grade)
		}
	}
}

func TestMissingPrerequisitesPreservesCatalogueOrder(t *testing.T) {
	completed := []domain.AcademicRecord{
		{CourseCode: "cs110", Grade: "B"},
		{CourseCode: "MATH101", Grade: "F"},
	}
	missing := domain.MissingPrerequisites([]string{"MATH101", " ", "cs110", "PHYS100"}, completed)
	if len(missing) != 2 {
		t.Fatalf("missing = %v, want two entries", missing)
	}
	if missing[0] != "MATH101" || missing[1] != "PHYS100" {
		t.Fatalf("missing = %v, want [MATH101 PHYS100]", missing)
	}
	if got := domain.MissingPrerequisites(nil, completed); got != nil {
		t.Fatalf("a course without prerequisites must report nothing missing, got %v", got)
	}
}

func TestNormalizeEmailAndRole(t *testing.T) {
	email, err := domain.NormalizeEmail("  Student@Campus.Example ")
	if err != nil {
		t.Fatalf("normalising a valid address failed: %v", err)
	}
	if email != "student@campus.example" {
		t.Fatalf("email = %q", email)
	}
	for _, invalid := range []string{"", "  ", "no-at-sign", "@domain", "local@", "a b@c.d"} {
		if _, err := domain.NormalizeEmail(invalid); !errors.Is(err, domain.ErrValidation) {
			t.Fatalf("address %q must be rejected, got %v", invalid, err)
		}
	}

	role, err := domain.ParseRole(" Registrar ")
	if err != nil || role != domain.RoleRegistrar {
		t.Fatalf("ParseRole = %v, %v", role, err)
	}
	if _, err := domain.ParseRole("dean"); !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("an unknown role must be rejected, got %v", err)
	}
}

func TestPrincipalOwnershipRules(t *testing.T) {
	student := domain.Principal{UserID: 5, Role: domain.RoleStudent}
	registrar := domain.Principal{UserID: 9, Role: domain.RoleRegistrar}

	if !student.CanActOnStudent(5) {
		t.Fatal("a student must act on their own record")
	}
	if student.CanActOnStudent(6) {
		t.Fatal("a student must not act on another record")
	}
	if !registrar.CanActOnStudent(6) {
		t.Fatal("a registrar must act on any record")
	}
	if student.IsRegistrarRole() || !registrar.IsRegistrarRole() {
		t.Fatal("registrar detection is wrong")
	}
	if (domain.User{Role: domain.RoleRegistrar}).IsRegistrar() != true {
		t.Fatal("User.IsRegistrar is wrong")
	}
}

func TestSessionValidationDistinguishesRevokedFromExpired(t *testing.T) {
	now := time.Date(2026, time.August, 26, 12, 0, 0, 0, time.UTC)
	live := domain.Session{IssuedAt: now.Add(-time.Hour), ExpiresAt: now.Add(time.Hour)}
	if err := live.Validate(now); err != nil {
		t.Fatalf("a live session must validate, got %v", err)
	}

	expired := live
	expired.ExpiresAt = now
	if err := expired.Validate(now); !errors.Is(err, domain.ErrSessionExpired) {
		t.Fatalf("expected ErrSessionExpired, got %v", err)
	}

	revokedAt := now.Add(-time.Minute)
	revoked := live
	revoked.RevokedAt = &revokedAt
	if err := revoked.Validate(now); !errors.Is(err, domain.ErrSessionRevoked) {
		t.Fatalf("expected ErrSessionRevoked, got %v", err)
	}
	if !revoked.Revoked() {
		t.Fatal("Revoked() must report true")
	}
}
