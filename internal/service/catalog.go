package service

import (
	"context"
	"fmt"

	"github.com/vance1852/orientation-enrollment-platform/internal/domain"
)

// CatalogService exposes the read side of terms, courses and sections.
type CatalogService struct {
	deps      Deps
	timetable *timetableIndex
}

// NewCatalogService builds the catalogue use cases.
func NewCatalogService(deps Deps) (*CatalogService, error) {
	if err := deps.Validate(); err != nil {
		return nil, err
	}
	return &CatalogService{deps: deps, timetable: newTimetableIndex()}, nil
}

// ListTerms returns the terms a caller may see. Only a registrar sees archived
// terms, because a student browsing the catalogue should not be able to claim a
// seat in a closed term by guessing its identifier.
func (s *CatalogService) ListTerms(ctx context.Context, actor domain.Principal) ([]domain.Term, error) {
	return s.deps.Store.Catalog().ListTerms(ctx, actor.IsRegistrarRole())
}

// CurrentTerm resolves the term that is currently accepting enrollments, falling
// back to the most recent term when no window is open.
func (s *CatalogService) CurrentTerm(ctx context.Context) (domain.Term, error) {
	terms, err := s.deps.Store.Catalog().ListTerms(ctx, false)
	if err != nil {
		return domain.Term{}, err
	}
	if len(terms) == 0 {
		return domain.Term{}, fmt.Errorf("no active term: %w", domain.ErrNotFound)
	}
	now := s.deps.now()
	for _, term := range terms {
		if term.EnrollmentOpen(now) {
			return term, nil
		}
	}
	return terms[0], nil
}

// ListSections runs the filtered catalogue query.
func (s *CatalogService) ListSections(ctx context.Context, filter domain.SectionFilter) (domain.PageResult[domain.Section], error) {
	return s.deps.Store.Catalog().ListSections(ctx, filter)
}

// GetSection loads a single section with its weekly blocks.
func (s *CatalogService) GetSection(ctx context.Context, sectionID int64) (domain.Section, error) {
	if sectionID <= 0 {
		return domain.Section{}, domain.NewFieldError("section_id", "must be a positive identifier")
	}
	section, err := s.deps.Store.Catalog().FindSectionByID(ctx, sectionID)
	if err != nil {
		return domain.Section{}, err
	}
	blocks, ok := s.timetable.lookup(section.ID)
	if !ok {
		// Keep a copy of the repository slice so the memoised entry can never be
		// disturbed by another response assembled from the same query.
		blocks = section.CloneMeetings()
		domain.SortMeetings(blocks)
		s.timetable.remember(section.ID, blocks)
	}
	section.Meetings = blocks
	return section, nil
}

// TimetableEntry is one course of a student on a single weekday.
type TimetableEntry struct {
	SectionID  int64
	CourseCode string
	Meetings   []domain.Meeting
}

// WeeklyTimetable returns the blocks a student attends on one weekday, which is
// what the orientation app renders on the daily schedule screen.
func (s *CatalogService) WeeklyTimetable(ctx context.Context, actor domain.Principal,
	studentID int64, weekday domain.Weekday) ([]TimetableEntry, error) {
	if studentID <= 0 {
		studentID = actor.UserID
	}
	if !actor.CanActOnStudent(studentID) {
		return nil, fmt.Errorf("read the timetable of student %d: %w", studentID, domain.ErrForbidden)
	}
	if !weekday.Valid() {
		return nil, domain.NewFieldError("weekday", "must be between 0 and 6")
	}
	term, err := s.CurrentTerm(ctx)
	if err != nil {
		return nil, err
	}
	held, err := s.deps.Store.Enrollments().ListEnrollments(ctx, domain.EnrollmentFilter{
		StudentID: studentID,
		TermID:    term.ID,
		Page:      domain.Page{Size: domain.MaxPageSize},
	})
	if err != nil {
		return nil, err
	}
	entries := make([]TimetableEntry, 0, len(held.Items))
	for _, enrollment := range held.Items {
		if !enrollment.Status.HoldsSeat() {
			continue
		}
		section, err := s.GetSection(ctx, enrollment.SectionID)
		if err != nil {
			return nil, err
		}
		blocks := section.Meetings[:0]
		for _, meeting := range section.Meetings {
			if meeting.Weekday == weekday {
				blocks = append(blocks, meeting)
			}
		}
		if len(blocks) == 0 {
			continue
		}
		entries = append(entries, TimetableEntry{
			SectionID:  section.ID,
			CourseCode: section.CourseCode,
			Meetings:   blocks,
		})
	}
	return entries, nil
}

// SectionRoster returns the roster of a section. Only registrars may read it.
func (s *CatalogService) SectionRoster(ctx context.Context, actor domain.Principal, sectionID int64, page domain.Page) (domain.PageResult[domain.Enrollment], error) {
	if err := requireRole(actor, domain.RoleRegistrar); err != nil {
		return domain.PageResult[domain.Enrollment]{}, fmt.Errorf("roster of section %d: %w", sectionID, err)
	}
	if _, err := s.deps.Store.Catalog().FindSectionByID(ctx, sectionID); err != nil {
		return domain.PageResult[domain.Enrollment]{}, err
	}
	return s.deps.Store.Enrollments().SectionRoster(ctx, sectionID, page)
}

// ListAuditEvents exposes the audit trail to registrars only.
func (s *CatalogService) ListAuditEvents(ctx context.Context, actor domain.Principal, filter domain.AuditFilter) (domain.PageResult[domain.AuditEvent], error) {
	if err := requireRole(actor, domain.RoleRegistrar); err != nil {
		return domain.PageResult[domain.AuditEvent]{}, fmt.Errorf("audit trail: %w", err)
	}
	return s.deps.Store.Audit().ListAuditEvents(ctx, filter)
}
