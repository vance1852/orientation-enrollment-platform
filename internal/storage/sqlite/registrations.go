package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/vance1852/orientation-enrollment-platform/internal/domain"
)

const registrationColumns = `id, student_id, term_id, status, program_code, advisor_email, dorm_preference,
       submitted_at, decided_at, decided_by_user_id, decision_note, version, created_at, updated_at`

var registrationSortColumns = map[string]string{
	"submitted_at": "submitted_at",
	"updated_at":   "updated_at",
	"status":       "status",
	"id":           "id",
}

// UpsertRegistration inserts a new orientation record or updates the existing
// one for the same student and term. The version column guards concurrent
// registrar decisions.
func (d *dataset) UpsertRegistration(ctx context.Context, registration domain.Registration) (domain.Registration, error) {
	if registration.StudentID <= 0 || registration.TermID <= 0 {
		return domain.Registration{}, domain.NewFieldError("registration", "student and term are required")
	}
	if registration.ID == 0 {
		res, err := d.q.ExecContext(ctx, `
            INSERT INTO student_registrations
                (student_id, term_id, status, program_code, advisor_email, dorm_preference,
                 submitted_at, decided_at, decided_by_user_id, decision_note, version, created_at, updated_at)
            VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			registration.StudentID, registration.TermID, string(registration.Status),
			registration.ProgramCode, registration.AdvisorEmail, registration.DormPreference,
			formatNullableTime(registration.SubmittedAt), formatNullableTime(registration.DecidedAt),
			nullableInt64(registration.DecidedByUserID), registration.DecisionNote,
			1, formatTime(registration.CreatedAt), formatTime(registration.UpdatedAt))
		if err != nil {
			if isUniqueViolation(err) {
				return domain.Registration{}, fmt.Errorf(
					"registration for student %d in term %d already exists: %w",
					registration.StudentID, registration.TermID, domain.ErrConflict)
			}
			return domain.Registration{}, fmt.Errorf("insert registration: %w", err)
		}
		id, err := res.LastInsertId()
		if err != nil {
			return domain.Registration{}, fmt.Errorf("read inserted registration id: %w", err)
		}
		registration.ID = id
		registration.Version = 1
		return registration, nil
	}

	res, err := d.q.ExecContext(ctx, `
        UPDATE student_registrations
        SET status = ?, program_code = ?, advisor_email = ?, dorm_preference = ?,
            submitted_at = ?, decided_at = ?, decided_by_user_id = ?, decision_note = ?,
            version = version + 1, updated_at = ?
        WHERE id = ? AND version = ?`,
		string(registration.Status), registration.ProgramCode, registration.AdvisorEmail,
		registration.DormPreference, formatNullableTime(registration.SubmittedAt),
		formatNullableTime(registration.DecidedAt), nullableInt64(registration.DecidedByUserID),
		registration.DecisionNote, formatTime(registration.UpdatedAt),
		registration.ID, registration.Version)
	if err != nil {
		return domain.Registration{}, fmt.Errorf("update registration %d: %w", registration.ID, err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return domain.Registration{}, fmt.Errorf("update registration rows: %w", err)
	}
	if affected == 0 {
		return domain.Registration{}, fmt.Errorf("registration %d changed concurrently: %w",
			registration.ID, domain.ErrVersionConflict)
	}
	registration.Version++
	return registration, nil
}

// FindRegistration loads the orientation record of one student in one term.
func (d *dataset) FindRegistration(ctx context.Context, studentID, termID int64) (domain.Registration, error) {
	row := d.q.QueryRowContext(ctx,
		`SELECT `+registrationColumns+` FROM student_registrations WHERE student_id = ? AND term_id = ?`,
		studentID, termID)
	registration, err := scanRegistration(row)
	if err != nil {
		return domain.Registration{}, notFound(
			fmt.Sprintf("registration for student %d in term %d", studentID, termID), err)
	}
	return registration, nil
}

// FindRegistrationByID loads a registration by identifier.
func (d *dataset) FindRegistrationByID(ctx context.Context, id int64) (domain.Registration, error) {
	row := d.q.QueryRowContext(ctx,
		`SELECT `+registrationColumns+` FROM student_registrations WHERE id = ?`, id)
	registration, err := scanRegistration(row)
	if err != nil {
		return domain.Registration{}, notFound(fmt.Sprintf("registration %d", id), err)
	}
	return registration, nil
}

// ListRegistrations returns the registrar work queue for a term.
func (d *dataset) ListRegistrations(ctx context.Context, termID int64, status domain.RegistrationStatus, page domain.Page) (domain.PageResult[domain.Registration], error) {
	normalized, err := page.Normalize(registrationSortColumns, "submitted_at")
	if err != nil {
		return domain.PageResult[domain.Registration]{}, err
	}
	var (
		conditions []string
		args       []any
	)
	if termID > 0 {
		conditions = append(conditions, "term_id = ?")
		args = append(args, termID)
	}
	if status != "" {
		conditions = append(conditions, "status = ?")
		args = append(args, string(status))
	}
	where := ""
	if len(conditions) > 0 {
		where = " WHERE " + strings.Join(conditions, " AND ")
	}

	total, err := countRows(ctx, d.q, `SELECT COUNT(*) FROM student_registrations`+where, args...)
	if err != nil {
		return domain.PageResult[domain.Registration]{}, err
	}

	query := `SELECT ` + registrationColumns + ` FROM student_registrations` + where +
		fmt.Sprintf(" ORDER BY %s %s, id ASC LIMIT ? OFFSET ?",
			registrationSortColumns[normalized.SortBy], strings.ToUpper(string(normalized.Order)))
	args = append(args, normalized.Size, normalized.Offset())

	rows, err := d.q.QueryContext(ctx, query, args...)
	if err != nil {
		return domain.PageResult[domain.Registration]{}, fmt.Errorf("list registrations: %w", err)
	}
	defer func() { _ = rows.Close() }()

	items := make([]domain.Registration, 0, normalized.Size)
	for rows.Next() {
		registration, err := scanRegistration(rows)
		if err != nil {
			return domain.PageResult[domain.Registration]{}, fmt.Errorf("scan registration: %w", err)
		}
		items = append(items, registration)
	}
	if err := rows.Err(); err != nil {
		return domain.PageResult[domain.Registration]{}, fmt.Errorf("iterate registrations: %w", err)
	}
	return domain.NewPageResult(items, total, normalized), nil
}

// StudentAcademicRecords returns the completed courses used for prerequisites.
func (d *dataset) StudentAcademicRecords(ctx context.Context, studentID int64) ([]domain.AcademicRecord, error) {
	rows, err := d.q.QueryContext(ctx, `
        SELECT id, student_id, course_code, grade, credits, completed_at
        FROM academic_records WHERE student_id = ? ORDER BY completed_at, course_code`, studentID)
	if err != nil {
		return nil, fmt.Errorf("list academic records of student %d: %w", studentID, err)
	}
	defer func() { _ = rows.Close() }()

	records := make([]domain.AcademicRecord, 0, 8)
	for rows.Next() {
		var (
			record      domain.AcademicRecord
			completedAt string
		)
		if err := rows.Scan(&record.ID, &record.StudentID, &record.CourseCode, &record.Grade,
			&record.Credits, &completedAt); err != nil {
			return nil, fmt.Errorf("scan academic record: %w", err)
		}
		parsed, err := parseTime(completedAt)
		if err != nil {
			return nil, err
		}
		record.CompletedAt = parsed
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate academic records: %w", err)
	}
	return records, nil
}

func scanRegistration(row rowScanner) (domain.Registration, error) {
	var (
		registration domain.Registration
		status       string
		submittedAt  sql.NullString
		decidedAt    sql.NullString
		decidedBy    sql.NullInt64
		createdAt    string
		updatedAt    string
	)
	if err := row.Scan(&registration.ID, &registration.StudentID, &registration.TermID, &status,
		&registration.ProgramCode, &registration.AdvisorEmail, &registration.DormPreference,
		&submittedAt, &decidedAt, &decidedBy, &registration.DecisionNote, &registration.Version,
		&createdAt, &updatedAt); err != nil {
		return domain.Registration{}, err
	}
	registration.Status = domain.RegistrationStatus(status)
	registration.DecidedByUserID = readNullableInt64(decidedBy)

	var err error
	if registration.SubmittedAt, err = parseNullableTime(submittedAt); err != nil {
		return domain.Registration{}, err
	}
	if registration.DecidedAt, err = parseNullableTime(decidedAt); err != nil {
		return domain.Registration{}, err
	}
	if registration.CreatedAt, err = parseTime(createdAt); err != nil {
		return domain.Registration{}, err
	}
	if registration.UpdatedAt, err = parseTime(updatedAt); err != nil {
		return domain.Registration{}, err
	}
	return registration, nil
}
