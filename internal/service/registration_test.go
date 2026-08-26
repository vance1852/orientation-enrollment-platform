package service_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/vance1852/orientation-enrollment-platform/internal/domain"
	"github.com/vance1852/orientation-enrollment-platform/internal/service"
)

func TestSubmitRegistrationIsScopedToTheOwner(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	newcomer := h.createUser("newcomer@campus.example", domain.RoleStudent)

	_, err := h.registrations.Submit(ctx, h.studentPrincipal(), service.SubmitInput{
		StudentID: newcomer.ID, TermID: h.term.ID,
		ProgramCode: "CS-BSC", AdvisorEmail: "advisor@campus.example", DormPreference: "on_campus",
	})
	if !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("a student must not submit for someone else, got %v", err)
	}

	// A registrar may act on behalf of a student who cannot reach the portal.
	registration, err := h.registrations.Submit(ctx, h.registrarPrincipal(), service.SubmitInput{
		StudentID: newcomer.ID, TermID: h.term.ID,
		ProgramCode: "CS-BSC", AdvisorEmail: "advisor@campus.example", DormPreference: "off_campus",
	})
	if err != nil {
		t.Fatalf("a registrar submission failed: %v", err)
	}
	if registration.Status != domain.RegistrationSubmitted || registration.StudentID != newcomer.ID {
		t.Fatalf("registration = %+v", registration)
	}
}

func TestSubmitRegistrationRejectsInvalidPayloadWithoutWriting(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	newcomer := h.createUser("newcomer@campus.example", domain.RoleStudent)
	actor := domain.Principal{UserID: newcomer.ID, Role: domain.RoleStudent}

	_, err := h.registrations.Submit(ctx, actor, service.SubmitInput{
		TermID: h.term.ID, ProgramCode: "", AdvisorEmail: "advisor@campus.example", DormPreference: "on_campus",
	})
	if !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("expected a validation error, got %v", err)
	}
	if _, err := h.registrations.Get(ctx, actor, newcomer.ID, h.term.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("a rejected submission must not create a row, got %v", err)
	}
}

func TestVerifiedRegistrationCannotBeResubmitted(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	_, err := h.registrations.Submit(ctx, h.studentPrincipal(), service.SubmitInput{
		StudentID: h.student.ID, TermID: h.term.ID,
		ProgramCode: "CS-MSC", AdvisorEmail: "advisor@campus.example", DormPreference: "on_campus",
	})
	if !errors.Is(err, domain.ErrInvalidTransition) {
		t.Fatalf("expected ErrInvalidTransition, got %v", err)
	}

	current, err := h.registrations.Get(ctx, h.studentPrincipal(), h.student.ID, h.term.ID)
	if err != nil {
		t.Fatalf("reading the registration failed: %v", err)
	}
	if current.ProgramCode != "CS-BSC" {
		t.Fatalf("the stored program must be untouched, got %q", current.ProgramCode)
	}
}

func TestRejectedRegistrationCanBeResubmittedAndVerified(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	newcomer := h.createUser("newcomer@campus.example", domain.RoleStudent)
	actor := domain.Principal{UserID: newcomer.ID, Role: domain.RoleStudent}

	registration, err := h.registrations.Submit(ctx, actor, service.SubmitInput{
		TermID: h.term.ID, ProgramCode: "CS-BSC",
		AdvisorEmail: "advisor@campus.example", DormPreference: "undecided",
	})
	if err != nil {
		t.Fatalf("submission failed: %v", err)
	}
	if _, err := h.registrations.Decide(ctx, h.registrarPrincipal(), service.DecideInput{
		RegistrationID: registration.ID, Status: domain.RegistrationRejected, Note: "transcript missing",
	}); err != nil {
		t.Fatalf("rejection failed: %v", err)
	}

	resubmitted, err := h.registrations.Submit(ctx, actor, service.SubmitInput{
		TermID: h.term.ID, ProgramCode: "CS-BSC",
		AdvisorEmail: "advisor2@campus.example", DormPreference: "on_campus",
	})
	if err != nil {
		t.Fatalf("resubmission failed: %v", err)
	}
	if resubmitted.Status != domain.RegistrationSubmitted || resubmitted.DecisionNote != "" {
		t.Fatalf("resubmitted record = %+v", resubmitted)
	}
	verified, err := h.registrations.Decide(ctx, h.registrarPrincipal(), service.DecideInput{
		RegistrationID: registration.ID, Status: domain.RegistrationVerified,
	})
	if err != nil {
		t.Fatalf("verification failed: %v", err)
	}
	if !verified.Verified() || verified.Version <= resubmitted.Version {
		t.Fatalf("verified record = %+v", verified)
	}
}

func TestDecideRequiresRegistrarRoleAndRecordsRejections(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	newcomer := h.createUser("newcomer@campus.example", domain.RoleStudent)
	actor := domain.Principal{UserID: newcomer.ID, Role: domain.RoleStudent}

	registration, err := h.registrations.Submit(ctx, actor, service.SubmitInput{
		TermID: h.term.ID, ProgramCode: "CS-BSC",
		AdvisorEmail: "advisor@campus.example", DormPreference: "undecided",
	})
	if err != nil {
		t.Fatalf("submission failed: %v", err)
	}

	if _, err := h.registrations.Decide(ctx, actor, service.DecideInput{
		RegistrationID: registration.ID, Status: domain.RegistrationVerified,
	}); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("a student must not decide, got %v", err)
	}
	if _, err := h.registrations.Decide(ctx, h.registrarPrincipal(), service.DecideInput{
		RegistrationID: registration.ID, Status: domain.RegistrationRejected,
	}); !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("a rejection without a note must fail, got %v", err)
	}
	if _, err := h.registrations.Decide(ctx, h.registrarPrincipal(), service.DecideInput{
		RegistrationID: 99999, Status: domain.RegistrationVerified,
	}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("deciding a missing record must fail, got %v", err)
	}

	// Verify twice: the second attempt is an illegal transition and must be
	// recorded as a rejected audit entry.
	if _, err := h.registrations.Decide(ctx, h.registrarPrincipal(), service.DecideInput{
		RegistrationID: registration.ID, Status: domain.RegistrationVerified,
	}); err != nil {
		t.Fatalf("the first verification failed: %v", err)
	}
	before := h.auditCount(domain.ActionRegistrationDecide, domain.ResultRejected)
	if _, err := h.registrations.Decide(ctx, h.registrarPrincipal(), service.DecideInput{
		RegistrationID: registration.ID, Status: domain.RegistrationVerified,
	}); !errors.Is(err, domain.ErrInvalidTransition) {
		t.Fatalf("a repeated verification must fail, got %v", err)
	}
	if after := h.auditCount(domain.ActionRegistrationDecide, domain.ResultRejected); after != before+1 {
		t.Fatalf("rejected decisions audited = %d, want %d", after, before+1)
	}
}

func TestListRegistrationsIsRegistrarOnlyAndPaginates(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	if _, err := h.registrations.List(ctx, h.studentPrincipal(), h.term.ID, "", domain.Page{}); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("a student must not read the queue, got %v", err)
	}

	verified, err := h.registrations.List(ctx, h.registrarPrincipal(), h.term.ID,
		domain.RegistrationVerified, domain.Page{Number: 1, Size: 1})
	if err != nil {
		t.Fatalf("listing failed: %v", err)
	}
	if verified.Total != 2 || len(verified.Items) != 1 || verified.TotalPages != 2 {
		t.Fatalf("verified page = %+v with %d items", verified, len(verified.Items))
	}

	submitted, err := h.registrations.List(ctx, h.registrarPrincipal(), h.term.ID,
		domain.RegistrationSubmitted, domain.Page{Size: 10})
	if err != nil {
		t.Fatalf("listing failed: %v", err)
	}
	if submitted.Total != 0 {
		t.Fatalf("submitted total = %d, want 0", submitted.Total)
	}
}

func TestSubmitRegistrationRejectsArchivedTerms(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	newcomer := h.createUser("newcomer@campus.example", domain.RoleStudent)

	archivedID, err := h.store.ProvisionTerm(ctx, domain.Term{
		Code: "2020-spring", Name: "Archived",
		EnrollmentOpensAt:  h.clock.Now().Add(-1000 * 24 * time.Hour),
		EnrollmentClosesAt: h.clock.Now().Add(-900 * 24 * time.Hour),
		AddDropClosesAt:    h.clock.Now().Add(-890 * 24 * time.Hour),
		CreditLimit:        18,
		Archived:           true,
	})
	if err != nil {
		t.Fatalf("provisioning the archived term failed: %v", err)
	}
	_, err = h.registrations.Submit(ctx, domain.Principal{UserID: newcomer.ID, Role: domain.RoleStudent},
		service.SubmitInput{TermID: archivedID, ProgramCode: "CS-BSC",
			AdvisorEmail: "advisor@campus.example", DormPreference: "on_campus"})
	if !errors.Is(err, domain.ErrEnrollmentWindowClosed) {
		t.Fatalf("expected ErrEnrollmentWindowClosed, got %v", err)
	}
}

func TestCatalogueReadsRespectRoles(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	studentTerms, err := h.catalog.ListTerms(ctx, h.studentPrincipal())
	if err != nil {
		t.Fatalf("listing terms failed: %v", err)
	}
	if len(studentTerms) != 1 {
		t.Fatalf("a student must see the single active term, got %d", len(studentTerms))
	}

	current, err := h.catalog.CurrentTerm(ctx)
	if err != nil {
		t.Fatalf("resolving the current term failed: %v", err)
	}
	if current.ID != h.term.ID {
		t.Fatalf("current term = %+v", current)
	}

	section, err := h.catalog.GetSection(ctx, h.tightID)
	if err != nil {
		t.Fatalf("reading the section failed: %v", err)
	}
	if len(section.Meetings) != 1 || section.CourseCode != "CS210" {
		t.Fatalf("section = %+v", section)
	}
	if _, err := h.catalog.GetSection(ctx, 0); !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("a zero identifier must be rejected, got %v", err)
	}

	if _, err := h.catalog.SectionRoster(ctx, h.studentPrincipal(), h.tightID, domain.Page{}); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("a student must not read a roster, got %v", err)
	}
	if _, err := h.catalog.SectionRoster(ctx, h.registrarPrincipal(), 99999, domain.Page{}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("a missing section must report not found, got %v", err)
	}
	if _, err := h.catalog.ListAuditEvents(ctx, h.studentPrincipal(), domain.AuditFilter{}); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("a student must not read the audit trail, got %v", err)
	}
	trail, err := h.catalog.ListAuditEvents(ctx, h.registrarPrincipal(), domain.AuditFilter{Page: domain.Page{Size: 50}})
	if err != nil {
		t.Fatalf("reading the trail failed: %v", err)
	}
	if trail.Total == 0 {
		t.Fatal("the fixture already produced audit entries")
	}
}
