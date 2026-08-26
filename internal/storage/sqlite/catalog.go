package sqlite

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/vance1852/orientation-enrollment-platform/internal/domain"
)

const termColumns = `id, code, name, enrollment_opens_at, enrollment_closes_at, add_drop_closes_at, credit_limit, archived`

const sectionSelect = `
SELECT s.id, s.term_id, s.course_id, c.code, c.credits, s.code, s.status, s.capacity, s.seats_taken,
       s.waitlist_limit, s.waitlist_length, s.instructor, s.version, s.updated_at
FROM course_sections s
JOIN courses c ON c.id = s.course_id`

// sectionSortColumns is the allow list backing the sort_by query parameter.
var sectionSortColumns = map[string]string{
	"code":       "s.code",
	"course":     "c.code",
	"capacity":   "s.capacity",
	"seats_left": "(s.capacity - s.seats_taken)",
	"updated_at": "s.updated_at",
}

// FindTermByID loads a term by identifier.
func (d *dataset) FindTermByID(ctx context.Context, id int64) (domain.Term, error) {
	row := d.q.QueryRowContext(ctx, `SELECT `+termColumns+` FROM terms WHERE id = ?`, id)
	term, err := scanTerm(row)
	if err != nil {
		return domain.Term{}, notFound(fmt.Sprintf("term %d", id), err)
	}
	return term, nil
}

// FindTermByCode loads a term by its campus code, for example 2026-autumn.
func (d *dataset) FindTermByCode(ctx context.Context, code string) (domain.Term, error) {
	normalized := strings.ToLower(strings.TrimSpace(code))
	row := d.q.QueryRowContext(ctx, `SELECT `+termColumns+` FROM terms WHERE code = ?`, normalized)
	term, err := scanTerm(row)
	if err != nil {
		return domain.Term{}, notFound("term "+normalized, err)
	}
	return term, nil
}

// ListTerms returns the terms visible to the caller.
func (d *dataset) ListTerms(ctx context.Context, includeArchived bool) ([]domain.Term, error) {
	query := `SELECT ` + termColumns + ` FROM terms`
	if !includeArchived {
		query += ` WHERE archived = 0`
	}
	query += ` ORDER BY enrollment_opens_at DESC, id DESC`

	rows, err := d.q.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("list terms: %w", err)
	}
	defer func() { _ = rows.Close() }()

	terms := make([]domain.Term, 0, 4)
	for rows.Next() {
		term, err := scanTerm(rows)
		if err != nil {
			return nil, fmt.Errorf("scan term: %w", err)
		}
		terms = append(terms, term)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate terms: %w", err)
	}
	return terms, nil
}

// FindCourseByID loads a catalogue entry together with its prerequisites.
func (d *dataset) FindCourseByID(ctx context.Context, id int64) (domain.Course, error) {
	row := d.q.QueryRowContext(ctx,
		`SELECT id, code, title, credits, department, retired FROM courses WHERE id = ?`, id)
	course, err := scanCourse(row)
	if err != nil {
		return domain.Course{}, notFound(fmt.Sprintf("course %d", id), err)
	}
	prereqs, err := d.CoursePrerequisites(ctx, course.ID)
	if err != nil {
		return domain.Course{}, err
	}
	course.Prerequisites = prereqs
	return course, nil
}

// FindCourseByCode loads a catalogue entry by course code.
func (d *dataset) FindCourseByCode(ctx context.Context, code string) (domain.Course, error) {
	normalized := strings.ToUpper(strings.TrimSpace(code))
	row := d.q.QueryRowContext(ctx,
		`SELECT id, code, title, credits, department, retired FROM courses WHERE code = ?`, normalized)
	course, err := scanCourse(row)
	if err != nil {
		return domain.Course{}, notFound("course "+normalized, err)
	}
	prereqs, err := d.CoursePrerequisites(ctx, course.ID)
	if err != nil {
		return domain.Course{}, err
	}
	course.Prerequisites = prereqs
	return course, nil
}

// CoursePrerequisites returns the required course codes in catalogue order.
func (d *dataset) CoursePrerequisites(ctx context.Context, courseID int64) ([]string, error) {
	rows, err := d.q.QueryContext(ctx,
		`SELECT required_course_code FROM course_prerequisites WHERE course_id = ? ORDER BY ordinal, required_course_code`,
		courseID)
	if err != nil {
		return nil, fmt.Errorf("list prerequisites of course %d: %w", courseID, err)
	}
	defer func() { _ = rows.Close() }()

	var codes []string
	for rows.Next() {
		var code string
		if err := rows.Scan(&code); err != nil {
			return nil, fmt.Errorf("scan prerequisite: %w", err)
		}
		codes = append(codes, code)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate prerequisites: %w", err)
	}
	return codes, nil
}

// FindSectionByID loads one section including its weekly meeting blocks.
func (d *dataset) FindSectionByID(ctx context.Context, id int64) (domain.Section, error) {
	row := d.q.QueryRowContext(ctx, sectionSelect+` WHERE s.id = ?`, id)
	section, err := scanSection(row)
	if err != nil {
		return domain.Section{}, notFound(fmt.Sprintf("section %d", id), err)
	}
	meetings, err := d.SectionMeetings(ctx, []int64{section.ID})
	if err != nil {
		return domain.Section{}, err
	}
	section.Meetings = meetings[section.ID]
	return section, nil
}

// FindSectionForUpdate loads a section inside a write transaction. The row is
// re-read so the caller observes the version it is about to compare against.
func (d *dataset) FindSectionForUpdate(ctx context.Context, id int64) (domain.Section, error) {
	return d.FindSectionByID(ctx, id)
}

// ListSections runs the paginated, filtered and sorted catalogue query.
func (d *dataset) ListSections(ctx context.Context, filter domain.SectionFilter) (domain.PageResult[domain.Section], error) {
	page, err := filter.Page.Normalize(sectionSortColumns, "code")
	if err != nil {
		return domain.PageResult[domain.Section]{}, err
	}

	var (
		conditions []string
		args       []any
	)
	if filter.TermID > 0 {
		conditions = append(conditions, "s.term_id = ?")
		args = append(args, filter.TermID)
	}
	if code := strings.ToUpper(strings.TrimSpace(filter.CourseCode)); code != "" {
		conditions = append(conditions, "c.code = ?")
		args = append(args, code)
	}
	if dept := strings.TrimSpace(filter.Department); dept != "" {
		conditions = append(conditions, "c.department = ?")
		args = append(args, dept)
	}
	if filter.Status != "" {
		conditions = append(conditions, "s.status = ?")
		args = append(args, string(filter.Status))
	}
	if filter.OnlyOpen {
		conditions = append(conditions, "s.status = 'open'", "s.seats_taken < s.capacity")
	}
	if instructor := strings.TrimSpace(filter.Instructor); instructor != "" {
		conditions = append(conditions, "s.instructor LIKE ?")
		args = append(args, "%"+instructor+"%")
	}

	where := ""
	if len(conditions) > 0 {
		where = " WHERE " + strings.Join(conditions, " AND ")
	}

	// The count query must apply exactly the same predicates as the page query,
	// otherwise the reported total would not match the pages a client can walk.
	total, err := countRows(ctx, d.q,
		`SELECT COUNT(*) FROM course_sections s JOIN courses c ON c.id = s.course_id`+where, args...)
	if err != nil {
		return domain.PageResult[domain.Section]{}, err
	}

	query := sectionSelect + where +
		fmt.Sprintf(" ORDER BY %s %s, s.id ASC LIMIT ? OFFSET ?",
			sectionSortColumns[page.SortBy], strings.ToUpper(string(page.Order)))
	pageArgs := append(append([]any{}, args...), page.Size, page.Offset())

	rows, err := d.q.QueryContext(ctx, query, pageArgs...)
	if err != nil {
		return domain.PageResult[domain.Section]{}, fmt.Errorf("list sections: %w", err)
	}
	defer func() { _ = rows.Close() }()

	sections := make([]domain.Section, 0, page.Size)
	ids := make([]int64, 0, page.Size)
	for rows.Next() {
		section, err := scanSection(rows)
		if err != nil {
			return domain.PageResult[domain.Section]{}, fmt.Errorf("scan section: %w", err)
		}
		sections = append(sections, section)
		ids = append(ids, section.ID)
	}
	if err := rows.Err(); err != nil {
		return domain.PageResult[domain.Section]{}, fmt.Errorf("iterate sections: %w", err)
	}

	meetings, err := d.SectionMeetings(ctx, ids)
	if err != nil {
		return domain.PageResult[domain.Section]{}, err
	}
	for i := range sections {
		sections[i].Meetings = meetings[sections[i].ID]
	}
	return domain.NewPageResult(sections, total, page), nil
}

// SectionMeetings loads the weekly blocks of the given sections. Each returned
// slice is freshly allocated so callers cannot alias another section's blocks.
func (d *dataset) SectionMeetings(ctx context.Context, sectionIDs []int64) (map[int64][]domain.Meeting, error) {
	result := make(map[int64][]domain.Meeting, len(sectionIDs))
	if len(sectionIDs) == 0 {
		return result, nil
	}
	placeholders := make([]string, len(sectionIDs))
	args := make([]any, len(sectionIDs))
	for i, id := range sectionIDs {
		placeholders[i] = "?"
		args[i] = id
	}
	query := `SELECT id, section_id, weekday, start_minute, end_minute, room
              FROM section_meetings
              WHERE section_id IN (` + strings.Join(placeholders, ",") + `)
              ORDER BY section_id, weekday, start_minute`

	rows, err := d.q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list section meetings: %w", err)
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var (
			meeting domain.Meeting
			weekday int
		)
		if err := rows.Scan(&meeting.ID, &meeting.SectionID, &weekday,
			&meeting.StartMinute, &meeting.EndMinute, &meeting.Room); err != nil {
			return nil, fmt.Errorf("scan section meeting: %w", err)
		}
		meeting.Weekday = domain.Weekday(weekday)
		result[meeting.SectionID] = append(result[meeting.SectionID], meeting)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate section meetings: %w", err)
	}
	return result, nil
}

// ClaimSeat performs the conditional seat allocation. The predicate carries both
// the optimistic version and the capacity bound, so two concurrent requests can
// never push seats_taken past capacity even if they both read the same version.
func (d *dataset) ClaimSeat(ctx context.Context, sectionID int64, expectedVersion int64, at time.Time) (domain.Section, error) {
	res, err := d.q.ExecContext(ctx, `
        UPDATE course_sections
        SET seats_taken = seats_taken + 1,
            version = version + 1,
            updated_at = ?
        WHERE id = ? AND version = ? AND status = 'open' AND seats_taken < capacity`,
		formatTime(at), sectionID, expectedVersion)
	if err != nil {
		return domain.Section{}, fmt.Errorf("claim seat in section %d: %w", sectionID, err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return domain.Section{}, fmt.Errorf("claim seat rows: %w", err)
	}
	if affected == 0 {
		return d.explainSeatFailure(ctx, sectionID, expectedVersion)
	}
	return d.FindSectionByID(ctx, sectionID)
}

// ReleaseSeat returns a seat to the pool under the same optimistic predicate.
func (d *dataset) ReleaseSeat(ctx context.Context, sectionID int64, expectedVersion int64, at time.Time) (domain.Section, error) {
	res, err := d.q.ExecContext(ctx, `
        UPDATE course_sections
        SET seats_taken = seats_taken - 1,
            version = version + 1,
            updated_at = ?
        WHERE id = ? AND version = ? AND seats_taken > 0`,
		formatTime(at), sectionID, expectedVersion)
	if err != nil {
		return domain.Section{}, fmt.Errorf("release seat in section %d: %w", sectionID, err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return domain.Section{}, fmt.Errorf("release seat rows: %w", err)
	}
	if affected == 0 {
		current, findErr := d.FindSectionByID(ctx, sectionID)
		if findErr != nil {
			return domain.Section{}, findErr
		}
		if current.Version != expectedVersion {
			return domain.Section{}, fmt.Errorf("section %d moved to version %d: %w",
				sectionID, current.Version, domain.ErrVersionConflict)
		}
		return domain.Section{}, fmt.Errorf("section %d holds no seat to release: %w",
			sectionID, domain.ErrConflict)
	}
	return d.FindSectionByID(ctx, sectionID)
}

// AdjustWaitlistLength moves the waitlist counter by delta while keeping it
// within the configured limit.
func (d *dataset) AdjustWaitlistLength(ctx context.Context, sectionID int64, delta int, at time.Time) (domain.Section, error) {
	if delta == 0 {
		return d.FindSectionByID(ctx, sectionID)
	}
	res, err := d.q.ExecContext(ctx, `
        UPDATE course_sections
        SET waitlist_length = waitlist_length + ?,
            version = version + 1,
            updated_at = ?
        WHERE id = ? AND waitlist_length + ? >= 0 AND waitlist_length + ? <= waitlist_limit`,
		delta, formatTime(at), sectionID, delta, delta)
	if err != nil {
		return domain.Section{}, fmt.Errorf("adjust waitlist of section %d: %w", sectionID, err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return domain.Section{}, fmt.Errorf("adjust waitlist rows: %w", err)
	}
	if affected == 0 {
		return domain.Section{}, fmt.Errorf("section %d waitlist cannot move by %d: %w",
			sectionID, delta, domain.ErrWaitlistFull)
	}
	return d.FindSectionByID(ctx, sectionID)
}

// explainSeatFailure turns a zero row update into the precise business error so
// the HTTP layer can distinguish a retryable version clash from a full section.
func (d *dataset) explainSeatFailure(ctx context.Context, sectionID int64, expectedVersion int64) (domain.Section, error) {
	current, err := d.FindSectionByID(ctx, sectionID)
	if err != nil {
		return domain.Section{}, err
	}
	if current.Version != expectedVersion {
		return domain.Section{}, fmt.Errorf("section %d moved to version %d: %w",
			sectionID, current.Version, domain.ErrVersionConflict)
	}
	if !current.AcceptsEnrollment() {
		return domain.Section{}, domain.NewConflictError(domain.ErrConflict,
			"section "+current.Code, "status "+string(current.Status))
	}
	return domain.Section{}, fmt.Errorf("section %s has %d of %d seats taken: %w",
		current.Code, current.SeatsTaken, current.Capacity, domain.ErrCapacityExhausted)
}

func scanTerm(row rowScanner) (domain.Term, error) {
	var (
		term     domain.Term
		opensAt  string
		closesAt string
		addDrop  string
		archived int
	)
	if err := row.Scan(&term.ID, &term.Code, &term.Name, &opensAt, &closesAt, &addDrop,
		&term.CreditLimit, &archived); err != nil {
		return domain.Term{}, err
	}
	term.Archived = archived != 0

	var err error
	if term.EnrollmentOpensAt, err = parseTime(opensAt); err != nil {
		return domain.Term{}, err
	}
	if term.EnrollmentClosesAt, err = parseTime(closesAt); err != nil {
		return domain.Term{}, err
	}
	if term.AddDropClosesAt, err = parseTime(addDrop); err != nil {
		return domain.Term{}, err
	}
	return term, nil
}

func scanCourse(row rowScanner) (domain.Course, error) {
	var (
		course  domain.Course
		retired int
	)
	if err := row.Scan(&course.ID, &course.Code, &course.Title, &course.Credits,
		&course.Department, &retired); err != nil {
		return domain.Course{}, err
	}
	course.Retired = retired != 0
	return course, nil
}

func scanSection(row rowScanner) (domain.Section, error) {
	var (
		section   domain.Section
		status    string
		updatedAt string
	)
	if err := row.Scan(&section.ID, &section.TermID, &section.CourseID, &section.CourseCode,
		&section.CourseCredits, &section.Code, &status, &section.Capacity, &section.SeatsTaken,
		&section.WaitlistLimit, &section.WaitlistLength, &section.Instructor, &section.Version,
		&updatedAt); err != nil {
		return domain.Section{}, err
	}
	section.Status = domain.SectionStatus(status)

	var err error
	if section.UpdatedAt, err = parseTime(updatedAt); err != nil {
		return domain.Section{}, err
	}
	return section, nil
}
