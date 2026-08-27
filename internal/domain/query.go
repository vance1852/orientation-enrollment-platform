package domain

import "strings"

// DefaultPageSize and MaxPageSize bound every list endpoint.
const (
	DefaultPageSize = 20
	MaxPageSize     = 100
)

// SortDirection is the normalised ordering of a list query.
type SortDirection string

// Supported sort directions.
const (
	SortAscending  SortDirection = "asc"
	SortDescending SortDirection = "desc"
)

// Page describes an offset window plus the requested ordering.
type Page struct {
	Number int
	Size   int
	SortBy string
	Order  SortDirection
}

// Normalize clamps user supplied paging input into the supported range and
// validates the sort key against the allow list of the calling repository.
func (p Page) Normalize(allowedSort map[string]string, defaultSort string) (Page, error) {
	out := p
	if out.Number <= 0 {
		out.Number = 1
	}
	if out.Size <= 0 {
		out.Size = DefaultPageSize
	}
	if out.Size > MaxPageSize {
		return Page{}, NewFieldError("page_size", "must not exceed 100")
	}
	key := strings.ToLower(strings.TrimSpace(out.SortBy))
	if key == "" {
		key = defaultSort
	}
	if _, ok := allowedSort[key]; !ok {
		return Page{}, NewFieldError("sort_by", "is not a sortable field")
	}
	out.SortBy = key
	switch SortDirection(strings.ToLower(strings.TrimSpace(string(out.Order)))) {
	case SortDescending:
		out.Order = SortDescending
	case SortAscending, "":
		out.Order = SortAscending
	default:
		return Page{}, NewFieldError("order", "must be asc or desc")
	}
	return out, nil
}

// Offset converts the page window into a SQL offset.
func (p Page) Offset() int {
	if p.Number <= 1 {
		return 0
	}
	return (p.Number - 1) * p.Size
}

// PageResult wraps a page of rows with the metadata list clients need.
type PageResult[T any] struct {
	Items      []T
	Total      int
	Page       int
	PageSize   int
	TotalPages int
}

// NewPageResult assembles the metadata from a normalised page and a total count.
func NewPageResult[T any](items []T, total int, page Page) PageResult[T] {
	if items == nil {
		items = []T{}
	}
	totalPages := 0
	if page.Size > 0 {
		totalPages = (total + page.Size - 1) / page.Size
	}
	return PageResult[T]{
		Items:      items,
		Total:      total,
		Page:       page.Number,
		PageSize:   page.Size,
		TotalPages: totalPages,
	}
}

// SectionFilter narrows a catalogue query. Zero values are ignored so the same
// struct serves the public catalogue and the registrar console.
type SectionFilter struct {
	TermID     int64
	CourseCode string
	Department string
	Status     SectionStatus
	OnlyOpen   bool
	Instructor string
	Page       Page
}

// EnrollmentFilter narrows an enrollment query.
type EnrollmentFilter struct {
	StudentID int64
	TermID    int64
	SectionID int64
	Statuses  []EnrollmentStatus
	Page      Page
}

// BatchItemResult reports the per-item outcome of a batch operation so partial
// failures stay visible to the caller.
type BatchItemResult struct {
	SectionID int64
	Status    EnrollmentStatus
	Code      string
	Message   string
	Succeeded bool
}

// BatchOutcome summarises how many items of a batch plan were applied and how
// many were rejected.
func BatchOutcome(results []BatchItemResult) (succeeded int, rejected int) {
	for _, item := range results {
		if item.Succeeded {
			succeeded++
			continue
		}
		rejected++
	}
	return succeeded, rejected
}

// BatchFullyApplied reports whether every item of a batch plan was applied.
func BatchFullyApplied(results []BatchItemResult) bool {
	_, rejected := BatchOutcome(results)
	return rejected == 0
}
