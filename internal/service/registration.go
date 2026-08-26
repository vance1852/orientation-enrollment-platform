package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/vance1852/orientation-enrollment-platform/internal/domain"
	"github.com/vance1852/orientation-enrollment-platform/internal/repository"
)

// RegistrationService drives the orientation paperwork state machine.
type RegistrationService struct {
	deps Deps
}

// NewRegistrationService builds the registration use cases.
func NewRegistrationService(deps Deps) (*RegistrationService, error) {
	if err := deps.Validate(); err != nil {
		return nil, err
	}
	return &RegistrationService{deps: deps}, nil
}

// SubmitInput is the payload a student sends when handing in the paperwork.
type SubmitInput struct {
	StudentID      int64
	TermID         int64
	ProgramCode    string
	AdvisorEmail   string
	DormPreference string
}

// Submit creates or resubmits the orientation record of a student.
//
// A verified record is terminal: resubmitting it is rejected by the state
// machine rather than silently overwriting a registrar decision.
func (s *RegistrationService) Submit(ctx context.Context, actor domain.Principal, in SubmitInput) (domain.Registration, error) {
	if in.StudentID <= 0 {
		in.StudentID = actor.UserID
	}
	if !actor.CanActOnStudent(in.StudentID) {
		return domain.Registration{}, fmt.Errorf("submit registration for student %d: %w", in.StudentID, domain.ErrForbidden)
	}
	term, err := s.deps.Store.Catalog().FindTermByID(ctx, in.TermID)
	if err != nil {
		return domain.Registration{}, err
	}
	now := s.deps.now()
	if term.Archived {
		return domain.Registration{}, fmt.Errorf("term %s is archived: %w", term.Code, domain.ErrEnrollmentWindowClosed)
	}

	var result domain.Registration
	err = s.deps.Store.InTx(ctx, func(ctx context.Context, tx repository.Repositories) error {
		registration, err := tx.Registrations().FindRegistration(ctx, in.StudentID, in.TermID)
		switch {
		case err == nil:
		case errors.Is(err, domain.ErrNotFound):
			registration = domain.Registration{
				StudentID: in.StudentID,
				TermID:    in.TermID,
				Status:    domain.RegistrationDraft,
				CreatedAt: now,
				UpdatedAt: now,
			}
		default:
			return err
		}

		registration.ProgramCode = in.ProgramCode
		registration.AdvisorEmail = in.AdvisorEmail
		registration.DormPreference = in.DormPreference
		if err := registration.Submit(now); err != nil {
			return err
		}
		saved, err := tx.Registrations().UpsertRegistration(ctx, registration)
		if err != nil {
			return err
		}
		result = saved
		return s.deps.Audit.RecordObjectID(ctx, tx.Audit(), &actor, domain.ActionRegistrationSubmit,
			"registration", saved.ID, domain.ResultSuccess,
			fmt.Sprintf("student %d submitted program %s for term %s", in.StudentID, in.ProgramCode, term.Code))
	})
	if err != nil {
		return domain.Registration{}, err
	}
	return result, nil
}

// DecideInput is the registrar decision payload.
type DecideInput struct {
	RegistrationID int64
	Status         domain.RegistrationStatus
	Note           string
}

// Decide verifies or rejects submitted paperwork. Only registrars may call it.
func (s *RegistrationService) Decide(ctx context.Context, actor domain.Principal, in DecideInput) (domain.Registration, error) {
	if err := requireRole(actor, domain.RoleRegistrar); err != nil {
		return domain.Registration{}, fmt.Errorf("decide registration %d: %w", in.RegistrationID, err)
	}
	now := s.deps.now()

	var result domain.Registration
	err := s.deps.Store.InTx(ctx, func(ctx context.Context, tx repository.Repositories) error {
		registration, err := tx.Registrations().FindRegistrationByID(ctx, in.RegistrationID)
		if err != nil {
			return err
		}
		if err := registration.Decide(in.Status, actor.UserID, in.Note, now); err != nil {
			return err
		}
		saved, err := tx.Registrations().UpsertRegistration(ctx, registration)
		if err != nil {
			return err
		}
		result = saved
		return s.deps.Audit.RecordObjectID(ctx, tx.Audit(), &actor, domain.ActionRegistrationDecide,
			"registration", saved.ID, domain.ResultSuccess,
			fmt.Sprintf("registration moved to %s", saved.Status))
	})
	if err != nil {
		s.deps.recordRejection(ctx, actor, domain.ActionRegistrationDecide, "registration", in.RegistrationID, err)
		return domain.Registration{}, err
	}
	return result, nil
}

// Get returns one registration, enforcing the ownership rule.
func (s *RegistrationService) Get(ctx context.Context, actor domain.Principal, studentID, termID int64) (domain.Registration, error) {
	if studentID <= 0 {
		studentID = actor.UserID
	}
	if !actor.CanActOnStudent(studentID) {
		return domain.Registration{}, fmt.Errorf("read registration of student %d: %w", studentID, domain.ErrForbidden)
	}
	return s.deps.Store.Registrations().FindRegistration(ctx, studentID, termID)
}

// List returns the registrar work queue.
func (s *RegistrationService) List(ctx context.Context, actor domain.Principal, termID int64, status domain.RegistrationStatus, page domain.Page) (domain.PageResult[domain.Registration], error) {
	if err := requireRole(actor, domain.RoleRegistrar); err != nil {
		return domain.PageResult[domain.Registration]{}, fmt.Errorf("list registrations: %w", err)
	}
	return s.deps.Store.Registrations().ListRegistrations(ctx, termID, status, page)
}
