package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/vance1852/orientation-enrollment-platform/internal/domain"
	"github.com/vance1852/orientation-enrollment-platform/internal/repository"
)

// EnrollmentService owns the seat allocation flow: eligibility, capacity,
// waitlist and the audit trail that follows every decision.
type EnrollmentService struct {
	deps       Deps
	maxRetries int
}

// NewEnrollmentService builds the enrollment use cases. maxRetries bounds how
// often a seat claim is replayed after an optimistic version conflict.
func NewEnrollmentService(deps Deps, maxRetries int) (*EnrollmentService, error) {
	if err := deps.Validate(); err != nil {
		return nil, err
	}
	if maxRetries < 0 || maxRetries > 32 {
		return nil, domain.NewFieldError("max_retries", "must be between 0 and 32")
	}
	return &EnrollmentService{deps: deps, maxRetries: maxRetries}, nil
}

// ClaimInput identifies the seat a caller wants.
type ClaimInput struct {
	StudentID int64
	SectionID int64
}

// ClaimResult reports the created record and the section state that produced it.
type ClaimResult struct {
	Enrollment domain.Enrollment
	Section    domain.Section
	Waitlisted bool
}

// WaitlistJobPayload is the durable payload of a waitlist promotion job.
type WaitlistJobPayload struct {
	SectionID int64 `json:"section_id"`
}

// Claim allocates a seat, or a waitlist position when the section is full.
//
// The whole decision runs inside one transaction: the eligibility reads, the
// conditional seat update, the enrollment insert and the audit entry either all
// land or none of them do. A concurrent claim that advanced the section version
// makes the transaction retry with fresh state instead of overselling.
func (s *EnrollmentService) Claim(ctx context.Context, actor domain.Principal, in ClaimInput) (ClaimResult, error) {
	req := domain.EnrollmentRequest{StudentID: in.StudentID, SectionID: in.SectionID}
	if req.StudentID <= 0 {
		req.StudentID = actor.UserID
	}
	if err := req.Validate(); err != nil {
		return ClaimResult{}, err
	}
	if !actor.CanActOnStudent(req.StudentID) {
		return ClaimResult{}, fmt.Errorf("claim a seat for student %d: %w", req.StudentID, domain.ErrForbidden)
	}

	attempts := s.maxRetries + 1
	for attempt := 1; attempt <= attempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return ClaimResult{}, fmt.Errorf("claim seat in section %d: %w", req.SectionID, err)
		}
		var result ClaimResult
		err := s.deps.Store.InTx(ctx, func(ctx context.Context, tx repository.Repositories) error {
			claimed, err := s.claimOnce(ctx, tx, actor, req)
			if err != nil {
				return err
			}
			result = claimed
			return nil
		})
		if err == nil {
			return result, nil
		}
		if errors.Is(err, domain.ErrVersionConflict) && attempt < attempts {
			s.deps.logger().Debug("retrying seat claim after version conflict",
				"section_id", req.SectionID, "attempt", attempt)
			continue
		}
		s.deps.recordRejection(ctx, actor, domain.ActionEnrollmentClaim, "section", req.SectionID, err)
		return ClaimResult{}, err
	}
	unsettled := fmt.Errorf("seat allocation for section %d did not settle after %d attempts: %w",
		req.SectionID, attempts, domain.ErrVersionConflict)
	s.deps.recordRejection(ctx, actor, domain.ActionEnrollmentClaim, "section", req.SectionID, unsettled)
	return ClaimResult{}, unsettled
}

func (s *EnrollmentService) claimOnce(ctx context.Context, tx repository.Repositories,
	actor domain.Principal, req domain.EnrollmentRequest) (ClaimResult, error) {
	section, err := tx.Catalog().FindSectionForUpdate(ctx, req.SectionID)
	if err != nil {
		return ClaimResult{}, err
	}
	term, err := tx.Catalog().FindTermByID(ctx, section.TermID)
	if err != nil {
		return ClaimResult{}, err
	}
	registration, err := tx.Registrations().FindRegistration(ctx, req.StudentID, term.ID)
	if err != nil {
		if !errors.Is(err, domain.ErrNotFound) {
			return ClaimResult{}, err
		}
		registration = domain.Registration{StudentID: req.StudentID, TermID: term.ID, Status: domain.RegistrationDraft}
	}
	prereqs, err := tx.Catalog().CoursePrerequisites(ctx, section.CourseID)
	if err != nil {
		return ClaimResult{}, err
	}
	completed, err := tx.Registrations().StudentAcademicRecords(ctx, req.StudentID)
	if err != nil {
		return ClaimResult{}, err
	}
	held, err := tx.Enrollments().ActiveEnrollmentsForStudent(ctx, req.StudentID, term.ID)
	if err != nil {
		return ClaimResult{}, err
	}
	heldMeetings, err := tx.Enrollments().ActiveMeetingsForStudent(ctx, req.StudentID, term.ID)
	if err != nil {
		return ClaimResult{}, err
	}

	now := s.deps.now()
	decision, err := domain.CheckEnrollmentEligibility(domain.EligibilityInput{
		Now:              now,
		Term:             term,
		Section:          section,
		Registration:     registration,
		CoursePrereqs:    prereqs,
		CompletedCourses: completed,
		HeldEnrollments:  held,
		HeldMeetings:     heldMeetings,
	})
	if err != nil {
		return ClaimResult{}, err
	}

	enrollment := domain.Enrollment{
		StudentID:   req.StudentID,
		TermID:      term.ID,
		SectionID:   section.ID,
		CourseCode:  section.CourseCode,
		Credits:     section.CourseCredits,
		Status:      domain.EnrollmentPending,
		RequestedAt: now,
		CreatedAt:   now,
		UpdatedAt:   now,
	}

	if decision.Waitlist {
		rank, err := tx.Enrollments().NextWaitlistRank(ctx, section.ID)
		if err != nil {
			return ClaimResult{}, err
		}
		if err := enrollment.Transition(domain.EnrollmentWaitlisted, now, ""); err != nil {
			return ClaimResult{}, err
		}
		enrollment.WaitlistRank = rank
		created, err := tx.Enrollments().CreateEnrollment(ctx, enrollment)
		if err != nil {
			return ClaimResult{}, err
		}
		updatedSection, err := tx.Catalog().AdjustWaitlistLength(ctx, section.ID, 1, now)
		if err != nil {
			return ClaimResult{}, err
		}
		if err := s.deps.Audit.RecordObjectID(ctx, tx.Audit(), &actor, domain.ActionEnrollmentWaitlist,
			"enrollment", created.ID, domain.ResultSuccess,
			fmt.Sprintf("student %d waitlisted at rank %d in %s", req.StudentID, rank, section.Code)); err != nil {
			return ClaimResult{}, err
		}
		return ClaimResult{Enrollment: created, Section: updatedSection, Waitlisted: true}, nil
	}

	updatedSection, err := tx.Catalog().ClaimSeat(ctx, section.ID, section.Version, now)
	if err != nil {
		return ClaimResult{}, err
	}
	if err := enrollment.Transition(domain.EnrollmentEnrolled, now, ""); err != nil {
		return ClaimResult{}, err
	}
	created, err := tx.Enrollments().CreateEnrollment(ctx, enrollment)
	if err != nil {
		return ClaimResult{}, err
	}
	if err := s.deps.Audit.RecordObjectID(ctx, tx.Audit(), &actor, domain.ActionEnrollmentClaim,
		"enrollment", created.ID, domain.ResultSuccess,
		fmt.Sprintf("student %d enrolled in %s, %d of %d seats taken",
			req.StudentID, section.Code, updatedSection.SeatsTaken, updatedSection.Capacity)); err != nil {
		return ClaimResult{}, err
	}
	return ClaimResult{Enrollment: created, Section: updatedSection}, nil
}

// Drop releases a seat or a waitlist position.
//
// Releasing a graded seat also enqueues a durable promotion job, so the freed
// seat is offered to the waitlist even if the process stops right after commit.
func (s *EnrollmentService) Drop(ctx context.Context, actor domain.Principal, enrollmentID int64, reason string) (domain.Enrollment, error) {
	if enrollmentID <= 0 {
		return domain.Enrollment{}, domain.NewFieldError("enrollment_id", "must be a positive identifier")
	}
	now := s.deps.now()

	var result domain.Enrollment
	err := s.deps.Store.InTx(ctx, func(ctx context.Context, tx repository.Repositories) error {
		enrollment, err := tx.Enrollments().FindEnrollmentByID(ctx, enrollmentID)
		if err != nil {
			return err
		}
		if !actor.CanActOnStudent(enrollment.StudentID) {
			return fmt.Errorf("drop enrollment %d: %w", enrollmentID, domain.ErrForbidden)
		}
		term, err := tx.Catalog().FindTermByID(ctx, enrollment.TermID)
		if err != nil {
			return err
		}
		if !actor.IsRegistrarRole() && !term.DropAllowed(now) {
			return fmt.Errorf("add/drop window of term %s is closed: %w", term.Code, domain.ErrEnrollmentWindowClosed)
		}
		section, err := tx.Catalog().FindSectionForUpdate(ctx, enrollment.SectionID)
		if err != nil {
			return err
		}

		target := domain.EnrollmentWithdrawn
		if enrollment.Status.HoldsSeat() {
			target = domain.EnrollmentDropped
		}
		previous := enrollment.Status
		expectedVersion := enrollment.Version
		if err := enrollment.Transition(target, now, reason); err != nil {
			return err
		}
		saved, err := tx.Enrollments().UpdateEnrollment(ctx, enrollment, expectedVersion)
		if err != nil {
			return err
		}

		sectionVersion := section.Version
		if domain.SeatDelta(previous, target) < 0 {
			released, err := tx.Catalog().ReleaseSeat(ctx, section.ID, sectionVersion, now)
			if err != nil {
				return err
			}
			sectionVersion = released.Version
			if released.WaitlistLength > 0 {
				if err := s.enqueueWaitlistPromotion(ctx, tx, section.ID, now); err != nil {
					return err
				}
			}
		}
		if previous == domain.EnrollmentWaitlisted {
			if _, err := tx.Catalog().AdjustWaitlistLength(ctx, section.ID, -1, now); err != nil {
				return err
			}
		}

		result = saved
		return s.deps.Audit.RecordObjectID(ctx, tx.Audit(), &actor, domain.ActionEnrollmentDrop,
			"enrollment", saved.ID, domain.ResultSuccess,
			fmt.Sprintf("enrollment moved from %s to %s in %s", previous, target, section.Code))
	})
	if err != nil {
		s.deps.recordRejection(ctx, actor, domain.ActionEnrollmentDrop, "enrollment", enrollmentID, err)
		return domain.Enrollment{}, err
	}
	return result, nil
}

// PromoteWaitlist offers a free seat to the head of the section waitlist. It is
// idempotent and reports false when there is nothing to promote.
func (s *EnrollmentService) PromoteWaitlist(ctx context.Context, sectionID int64) (bool, error) {
	if sectionID <= 0 {
		return false, domain.NewFieldError("section_id", "must be a positive identifier")
	}
	now := s.deps.now()
	promoted := false
	err := s.deps.Store.InTx(ctx, func(ctx context.Context, tx repository.Repositories) error {
		section, err := tx.Catalog().FindSectionForUpdate(ctx, sectionID)
		if err != nil {
			return err
		}
		if !section.AcceptsEnrollment() || section.SeatsAvailable() <= 0 {
			return nil
		}
		head, err := tx.Enrollments().HeadOfWaitlist(ctx, sectionID)
		if err != nil {
			if errors.Is(err, domain.ErrNotFound) {
				return nil
			}
			return err
		}
		if _, err := tx.Catalog().ClaimSeat(ctx, section.ID, section.Version, now); err != nil {
			return err
		}
		expectedVersion := head.Version
		if err := head.Transition(domain.EnrollmentEnrolled, now, ""); err != nil {
			return err
		}
		saved, err := tx.Enrollments().UpdateEnrollment(ctx, head, expectedVersion)
		if err != nil {
			return err
		}
		if _, err := tx.Catalog().AdjustWaitlistLength(ctx, section.ID, -1, now); err != nil {
			return err
		}
		promoted = true
		return s.deps.Audit.RecordObjectID(ctx, tx.Audit(), nil, domain.ActionEnrollmentPromote,
			"enrollment", saved.ID, domain.ResultSuccess,
			fmt.Sprintf("student %d promoted from the waitlist of %s", saved.StudentID, section.Code))
	})
	if err != nil {
		return false, err
	}
	return promoted, nil
}

// BatchClaim claims several sections for one student and reports a per item
// outcome, so a single ineligible section does not hide the successful ones.
func (s *EnrollmentService) BatchClaim(ctx context.Context, actor domain.Principal, studentID int64, sectionIDs []int64) ([]domain.BatchItemResult, error) {
	if len(sectionIDs) == 0 {
		return nil, domain.NewFieldError("section_ids", "must contain at least one section")
	}
	if len(sectionIDs) > 20 {
		return nil, domain.NewFieldError("section_ids", "must contain at most 20 sections")
	}
	if studentID <= 0 {
		studentID = actor.UserID
	}
	if !actor.CanActOnStudent(studentID) {
		return nil, fmt.Errorf("batch claim for student %d: %w", studentID, domain.ErrForbidden)
	}

	results := make([]domain.BatchItemResult, 0, len(sectionIDs))
	for _, sectionID := range sectionIDs {
		if err := ctx.Err(); err != nil {
			return results, fmt.Errorf("batch claim interrupted: %w", err)
		}
		claimed, err := s.Claim(ctx, actor, ClaimInput{StudentID: studentID, SectionID: sectionID})
		if err != nil {
			results = append(results, domain.BatchItemResult{
				SectionID: sectionID,
				Code:      domain.Code(err),
				Message:   err.Error(),
			})
			continue
		}
		results = append(results, domain.BatchItemResult{
			SectionID: sectionID,
			Status:    claimed.Enrollment.Status,
			Succeeded: true,
			Code:      "ok",
			Message:   fmt.Sprintf("enrollment %d created", claimed.Enrollment.ID),
		})
	}
	return results, nil
}

// List returns the enrollments visible to the caller.
func (s *EnrollmentService) List(ctx context.Context, actor domain.Principal, filter domain.EnrollmentFilter) (domain.PageResult[domain.Enrollment], error) {
	if !actor.IsRegistrarRole() {
		filter.StudentID = actor.UserID
	}
	return s.deps.Store.Enrollments().ListEnrollments(ctx, filter)
}

// Get returns one enrollment, enforcing the ownership rule.
func (s *EnrollmentService) Get(ctx context.Context, actor domain.Principal, enrollmentID int64) (domain.Enrollment, error) {
	enrollment, err := s.deps.Store.Enrollments().FindEnrollmentByID(ctx, enrollmentID)
	if err != nil {
		return domain.Enrollment{}, err
	}
	if !actor.CanActOnStudent(enrollment.StudentID) {
		return domain.Enrollment{}, fmt.Errorf("read enrollment %d: %w", enrollmentID, domain.ErrForbidden)
	}
	return enrollment, nil
}

func (s *EnrollmentService) enqueueWaitlistPromotion(ctx context.Context, tx repository.Repositories, sectionID int64, now time.Time) error {
	payload, err := json.Marshal(WaitlistJobPayload{SectionID: sectionID})
	if err != nil {
		return fmt.Errorf("encode waitlist job payload: %w", err)
	}
	_, err = tx.Jobs().EnqueueJob(ctx, domain.Job{
		Kind:        domain.JobPromoteWaitlist,
		Payload:     string(payload),
		State:       domain.JobQueued,
		MaxAttempts: domain.MaxJobAttempts,
		RunAfter:    now,
		CreatedAt:   now,
		UpdatedAt:   now,
	})
	return err
}
