package service

import (
	"context"
	"fmt"

	"github.com/vance1852/orientation-enrollment-platform/internal/domain"
)

// CatalogService exposes the read side of terms, courses and sections.
type CatalogService struct {
	deps Deps
}

// NewCatalogService builds the catalogue use cases.
func NewCatalogService(deps Deps) (*CatalogService, error) {
	if err := deps.Validate(); err != nil {
		return nil, err
	}
	return &CatalogService{deps: deps}, nil
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
	// Hand out a copy of the meeting slice so a caller that sorts or trims the
	// result cannot disturb another response assembled from the same query.
	section.Meetings = section.CloneMeetings()
	domain.SortMeetings(section.Meetings)
	return section, nil
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
