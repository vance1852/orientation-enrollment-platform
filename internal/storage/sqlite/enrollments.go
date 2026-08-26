package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/vance1852/orientation-enrollment-platform/internal/domain"
)

const enrollmentColumns = `id, student_id, term_id, section_id, course_code, credits, status,
       waitlist_rank, requested_at, decided_at, released_at, release_reason, version, created_at, updated_at`

var enrollmentSortColumns = map[string]string{
	"requested_at":  "requested_at",
	"updated_at":    "updated_at",
	"status":        "status",
	"course_code":   "course_code",
	"waitlist_rank": "waitlist_rank",
	"id":            "id",
}

var rosterSortColumns = map[string]string{
	"waitlist_rank": "waitlist_rank",
	"requested_at":  "requested_at",
	"status":        "status",
	"id":            "id",
}

// CreateEnrollment inserts a seat claim. The partial unique index rejects a
// second active claim for the same student and section.
func (d *dataset) CreateEnrollment(ctx context.Context, enrollment domain.Enrollment) (domain.Enrollment, error) {
	res, err := d.q.ExecContext(ctx, `
        INSERT INTO enrollments
            (student_id, term_id, section_id, course_code, credits, status, waitlist_rank,
             requested_at, decided_at, released_at, release_reason, version, created_at, updated_at)
        VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		enrollment.StudentID, enrollment.TermID, enrollment.SectionID, enrollment.CourseCode,
		enrollment.Credits, string(enrollment.Status), enrollment.WaitlistRank,
		formatTime(enrollment.RequestedAt), formatNullableTime(enrollment.DecidedAt),
		formatNullableTime(enrollment.ReleasedAt), enrollment.ReleaseReason,
		1, formatTime(enrollment.CreatedAt), formatTime(enrollment.UpdatedAt))
	if err != nil {
		if isUniqueViolation(err) {
			return domain.Enrollment{}, fmt.Errorf(
				"student %d already holds section %d: %w",
				enrollment.StudentID, enrollment.SectionID, domain.ErrDuplicateEnrollment)
		}
		return domain.Enrollment{}, fmt.Errorf("insert enrollment: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return domain.Enrollment{}, fmt.Errorf("read inserted enrollment id: %w", err)
	}
	enrollment.ID = id
	enrollment.Version = 1
	return enrollment, nil
}

// UpdateEnrollment writes a lifecycle change guarded by the expected version.
func (d *dataset) UpdateEnrollment(ctx context.Context, enrollment domain.Enrollment, expectedVersion int64) (domain.Enrollment, error) {
	res, err := d.q.ExecContext(ctx, `
        UPDATE enrollments
        SET status = ?, waitlist_rank = ?, decided_at = ?, released_at = ?, release_reason = ?,
            version = version + 1, updated_at = ?
        WHERE id = ? AND version = ?`,
		string(enrollment.Status), enrollment.WaitlistRank, formatNullableTime(enrollment.DecidedAt),
		formatNullableTime(enrollment.ReleasedAt), enrollment.ReleaseReason,
		formatTime(enrollment.UpdatedAt), enrollment.ID, expectedVersion)
	if err != nil {
		return domain.Enrollment{}, fmt.Errorf("update enrollment %d: %w", enrollment.ID, err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return domain.Enrollment{}, fmt.Errorf("update enrollment rows: %w", err)
	}
	if affected == 0 {
		return domain.Enrollment{}, fmt.Errorf("enrollment %d changed concurrently: %w",
			enrollment.ID, domain.ErrVersionConflict)
	}
	enrollment.Version = expectedVersion + 1
	return enrollment, nil
}

// FindEnrollmentByID loads one seat claim.
func (d *dataset) FindEnrollmentByID(ctx context.Context, id int64) (domain.Enrollment, error) {
	row := d.q.QueryRowContext(ctx, `SELECT `+enrollmentColumns+` FROM enrollments WHERE id = ?`, id)
	enrollment, err := scanEnrollment(row)
	if err != nil {
		return domain.Enrollment{}, notFound(fmt.Sprintf("enrollment %d", id), err)
	}
	return enrollment, nil
}

// FindActiveEnrollment loads the live claim of a student on a section.
func (d *dataset) FindActiveEnrollment(ctx context.Context, studentID, sectionID int64) (domain.Enrollment, error) {
	row := d.q.QueryRowContext(ctx, `
        SELECT `+enrollmentColumns+` FROM enrollments
        WHERE student_id = ? AND section_id = ? AND status IN ('pending', 'enrolled', 'waitlisted', 'completed')`,
		studentID, sectionID)
	enrollment, err := scanEnrollment(row)
	if err != nil {
		return domain.Enrollment{}, notFound(
			fmt.Sprintf("active enrollment of student %d in section %d", studentID, sectionID), err)
	}
	return enrollment, nil
}

// ListEnrollments runs the paginated enrollment query.
func (d *dataset) ListEnrollments(ctx context.Context, filter domain.EnrollmentFilter) (domain.PageResult[domain.Enrollment], error) {
	page, err := filter.Page.Normalize(enrollmentSortColumns, "requested_at")
	if err != nil {
		return domain.PageResult[domain.Enrollment]{}, err
	}
	var (
		conditions []string
		args       []any
	)
	if filter.StudentID > 0 {
		conditions = append(conditions, "student_id = ?")
		args = append(args, filter.StudentID)
	}
	if filter.TermID > 0 {
		conditions = append(conditions, "term_id = ?")
		args = append(args, filter.TermID)
	}
	if filter.SectionID > 0 {
		conditions = append(conditions, "section_id = ?")
		args = append(args, filter.SectionID)
	}
	if len(filter.Statuses) > 0 {
		placeholders := make([]string, len(filter.Statuses))
		for i, status := range filter.Statuses {
			placeholders[i] = "?"
			args = append(args, string(status))
		}
		conditions = append(conditions, "status IN ("+strings.Join(placeholders, ",")+")")
	}
	where := ""
	if len(conditions) > 0 {
		where = " WHERE " + strings.Join(conditions, " AND ")
	}

	total, err := countRows(ctx, d.q, `SELECT COUNT(*) FROM enrollments`+where, args...)
	if err != nil {
		return domain.PageResult[domain.Enrollment]{}, err
	}

	query := `SELECT ` + enrollmentColumns + ` FROM enrollments` + where +
		fmt.Sprintf(" ORDER BY %s %s, id ASC LIMIT ? OFFSET ?",
			enrollmentSortColumns[page.SortBy], strings.ToUpper(string(page.Order)))
	args = append(args, page.Size, page.Offset())

	items, err := d.queryEnrollments(ctx, query, args...)
	if err != nil {
		return domain.PageResult[domain.Enrollment]{}, err
	}
	return domain.NewPageResult(items, total, page), nil
}

// ActiveEnrollmentsForStudent returns every claim that still occupies the term
// plan, which drives the credit limit and duplicate course checks.
func (d *dataset) ActiveEnrollmentsForStudent(ctx context.Context, studentID, termID int64) ([]domain.Enrollment, error) {
	return d.queryEnrollments(ctx, `
        SELECT `+enrollmentColumns+` FROM enrollments
        WHERE student_id = ? AND term_id = ? AND status IN ('pending', 'enrolled', 'waitlisted')
        ORDER BY requested_at, id`, studentID, termID)
}

// ActiveMeetingsForStudent returns the weekly blocks the student already holds,
// which drives the schedule conflict check.
func (d *dataset) ActiveMeetingsForStudent(ctx context.Context, studentID, termID int64) ([]domain.Meeting, error) {
	rows, err := d.q.QueryContext(ctx, `
        SELECT m.id, m.section_id, m.weekday, m.start_minute, m.end_minute, m.room
        FROM section_meetings m
        JOIN enrollments e ON e.section_id = m.section_id
        WHERE e.student_id = ? AND e.term_id = ? AND e.status IN ('pending', 'enrolled', 'waitlisted')
        ORDER BY m.weekday, m.start_minute, m.id`, studentID, termID)
	if err != nil {
		return nil, fmt.Errorf("list held meetings of student %d: %w", studentID, err)
	}
	defer func() { _ = rows.Close() }()

	meetings := make([]domain.Meeting, 0, 8)
	for rows.Next() {
		var (
			meeting domain.Meeting
			weekday int
		)
		if err := rows.Scan(&meeting.ID, &meeting.SectionID, &weekday,
			&meeting.StartMinute, &meeting.EndMinute, &meeting.Room); err != nil {
			return nil, fmt.Errorf("scan held meeting: %w", err)
		}
		meeting.Weekday = domain.Weekday(weekday)
		meetings = append(meetings, meeting)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate held meetings: %w", err)
	}
	return meetings, nil
}

// NextWaitlistRank returns the rank a new waitlist entry should take.
func (d *dataset) NextWaitlistRank(ctx context.Context, sectionID int64) (int, error) {
	var highest sql.NullInt64
	err := d.q.QueryRowContext(ctx,
		`SELECT MAX(waitlist_rank) FROM enrollments WHERE section_id = ? AND status = 'waitlisted'`,
		sectionID).Scan(&highest)
	if err != nil {
		return 0, fmt.Errorf("read waitlist rank of section %d: %w", sectionID, err)
	}
	if !highest.Valid {
		return 1, nil
	}
	return int(highest.Int64) + 1, nil
}

// HeadOfWaitlist returns the earliest waitlisted claim of a section.
func (d *dataset) HeadOfWaitlist(ctx context.Context, sectionID int64) (domain.Enrollment, error) {
	row := d.q.QueryRowContext(ctx, `
        SELECT `+enrollmentColumns+` FROM enrollments
        WHERE section_id = ? AND status = 'waitlisted'
        ORDER BY waitlist_rank ASC, requested_at ASC, id ASC
        LIMIT 1`, sectionID)
	enrollment, err := scanEnrollment(row)
	if err != nil {
		return domain.Enrollment{}, notFound(fmt.Sprintf("waitlist head of section %d", sectionID), err)
	}
	return enrollment, nil
}

// SectionRoster returns the paginated roster used by the registrar console.
func (d *dataset) SectionRoster(ctx context.Context, sectionID int64, page domain.Page) (domain.PageResult[domain.Enrollment], error) {
	normalized, err := page.Normalize(rosterSortColumns, "requested_at")
	if err != nil {
		return domain.PageResult[domain.Enrollment]{}, err
	}
	total, err := countRows(ctx, d.q,
		`SELECT COUNT(*) FROM enrollments WHERE section_id = ? AND status IN ('enrolled', 'waitlisted', 'completed')`,
		sectionID)
	if err != nil {
		return domain.PageResult[domain.Enrollment]{}, err
	}
	query := `SELECT ` + enrollmentColumns + ` FROM enrollments
              WHERE section_id = ? AND status IN ('enrolled', 'waitlisted', 'completed')` +
		fmt.Sprintf(" ORDER BY %s %s, id ASC LIMIT ? OFFSET ?",
			rosterSortColumns[normalized.SortBy], strings.ToUpper(string(normalized.Order)))

	items, err := d.queryEnrollments(ctx, query, sectionID, normalized.Size, normalized.Offset())
	if err != nil {
		return domain.PageResult[domain.Enrollment]{}, err
	}
	return domain.NewPageResult(items, total, normalized), nil
}

func (d *dataset) queryEnrollments(ctx context.Context, query string, args ...any) ([]domain.Enrollment, error) {
	rows, err := d.q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query enrollments: %w", err)
	}
	defer func() { _ = rows.Close() }()

	items := make([]domain.Enrollment, 0, 8)
	for rows.Next() {
		enrollment, err := scanEnrollment(rows)
		if err != nil {
			return nil, fmt.Errorf("scan enrollment: %w", err)
		}
		items = append(items, enrollment)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate enrollments: %w", err)
	}
	return items, nil
}

func scanEnrollment(row rowScanner) (domain.Enrollment, error) {
	var (
		enrollment  domain.Enrollment
		status      string
		requestedAt string
		decidedAt   sql.NullString
		releasedAt  sql.NullString
		createdAt   string
		updatedAt   string
	)
	if err := row.Scan(&enrollment.ID, &enrollment.StudentID, &enrollment.TermID, &enrollment.SectionID,
		&enrollment.CourseCode, &enrollment.Credits, &status, &enrollment.WaitlistRank, &requestedAt,
		&decidedAt, &releasedAt, &enrollment.ReleaseReason, &enrollment.Version,
		&createdAt, &updatedAt); err != nil {
		return domain.Enrollment{}, err
	}
	enrollment.Status = domain.EnrollmentStatus(status)

	var err error
	if enrollment.RequestedAt, err = parseTime(requestedAt); err != nil {
		return domain.Enrollment{}, err
	}
	if enrollment.DecidedAt, err = parseNullableTime(decidedAt); err != nil {
		return domain.Enrollment{}, err
	}
	if enrollment.ReleasedAt, err = parseNullableTime(releasedAt); err != nil {
		return domain.Enrollment{}, err
	}
	if enrollment.CreatedAt, err = parseTime(createdAt); err != nil {
		return domain.Enrollment{}, err
	}
	if enrollment.UpdatedAt, err = parseTime(updatedAt); err != nil {
		return domain.Enrollment{}, err
	}
	return enrollment, nil
}
