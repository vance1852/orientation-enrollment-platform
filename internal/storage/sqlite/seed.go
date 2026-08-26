package sqlite

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/vance1852/orientation-enrollment-platform/internal/domain"
	"github.com/vance1852/orientation-enrollment-platform/internal/security"
)

// SeedSpec describes the reference data inserted into an empty database so the
// service is operable right after the first start-up.
type SeedSpec struct {
	Now              time.Time
	RegistrarEmail   string
	RegistrarPass    string
	StudentEmail     string
	StudentPass      string
	TermCode         string
	BusinessLocation *time.Location
}

// SeedResult reports what Seed created.
type SeedResult struct {
	Skipped     bool
	TermID      int64
	RegistrarID int64
	StudentID   int64
	SectionIDs  []int64
}

// Seed inserts the orientation reference data inside a single transaction.
//
// The call is idempotent: when the requested term already exists it reports
// Skipped and leaves the database untouched, which is what a restart needs.
func Seed(ctx context.Context, store *Store, spec SeedSpec) (SeedResult, error) {
	if store == nil {
		return SeedResult{}, fmt.Errorf("seed: store handle is nil")
	}
	if spec.BusinessLocation == nil {
		spec.BusinessLocation = time.UTC
	}
	if spec.Now.IsZero() {
		spec.Now = time.Now().UTC()
	}
	if spec.TermCode == "" {
		spec.TermCode = "2026-autumn"
	}
	if _, err := store.Catalog().FindTermByCode(ctx, spec.TermCode); err == nil {
		return SeedResult{Skipped: true}, nil
	} else if !errors.Is(err, domain.ErrNotFound) {
		return SeedResult{}, err
	}

	registrarHash, err := security.HashNewPassword(spec.RegistrarPass)
	if err != nil {
		return SeedResult{}, fmt.Errorf("seed registrar password: %w", err)
	}
	studentHash, err := security.HashNewPassword(spec.StudentPass)
	if err != nil {
		return SeedResult{}, fmt.Errorf("seed student password: %w", err)
	}

	var result SeedResult
	err = store.inTxDataset(ctx, func(ctx context.Context, d *dataset) error {
		registrar, err := d.CreateUser(ctx, domain.User{
			Email:        spec.RegistrarEmail,
			DisplayName:  "Orientation Registrar",
			Role:         domain.RoleRegistrar,
			PasswordHash: registrarHash,
			CreatedAt:    spec.Now,
			UpdatedAt:    spec.Now,
		})
		if err != nil {
			return err
		}
		student, err := d.CreateUser(ctx, domain.User{
			Email:        spec.StudentEmail,
			DisplayName:  "Incoming Student",
			Role:         domain.RoleStudent,
			PasswordHash: studentHash,
			CreatedAt:    spec.Now,
			UpdatedAt:    spec.Now,
		})
		if err != nil {
			return err
		}
		result.RegistrarID = registrar.ID
		result.StudentID = student.ID

		// The window is anchored on the seeding instant instead of a fixed
		// calendar date, so a freshly provisioned environment is always usable
		// no matter when it is started.
		opens := spec.Now.Add(-24 * time.Hour)
		termID, err := d.insertTerm(ctx, domain.Term{
			Code:               spec.TermCode,
			Name:               "Autumn orientation term",
			EnrollmentOpensAt:  opens,
			EnrollmentClosesAt: opens.Add(30 * 24 * time.Hour),
			AddDropClosesAt:    opens.Add(45 * 24 * time.Hour),
			CreditLimit:        18,
		})
		if err != nil {
			return err
		}
		result.TermID = termID

		courses := []struct {
			course  domain.Course
			prereqs []string
		}{
			{course: domain.Course{Code: "MATH101", Title: "Calculus foundations", Credits: 4, Department: "mathematics"}},
			{course: domain.Course{Code: "CS110", Title: "Programming fundamentals", Credits: 4, Department: "computing"}},
			{course: domain.Course{Code: "CS210", Title: "Data structures", Credits: 4, Department: "computing"}, prereqs: []string{"CS110"}},
			{course: domain.Course{Code: "ORI100", Title: "Campus orientation seminar", Credits: 2, Department: "student-life"}},
		}
		courseIDs := make(map[string]int64, len(courses))
		for _, entry := range courses {
			id, err := d.insertCourse(ctx, entry.course)
			if err != nil {
				return err
			}
			courseIDs[entry.course.Code] = id
			for ordinal, required := range entry.prereqs {
				if err := d.insertPrerequisite(ctx, id, required, ordinal); err != nil {
					return err
				}
			}
		}

		sections := []struct {
			section  domain.Section
			meetings []domain.Meeting
		}{
			{
				section: domain.Section{TermID: termID, CourseID: courseIDs["MATH101"], Code: "MATH101-A",
					Status: domain.SectionOpen, Capacity: 40, WaitlistLimit: 10, Instructor: "Prof. Lin"},
				meetings: []domain.Meeting{
					{Weekday: domain.Monday, StartMinute: 8 * 60, EndMinute: 9*60 + 40, Room: "N201"},
					{Weekday: domain.Wednesday, StartMinute: 8 * 60, EndMinute: 9*60 + 40, Room: "N201"},
				},
			},
			{
				section: domain.Section{TermID: termID, CourseID: courseIDs["CS110"], Code: "CS110-A",
					Status: domain.SectionOpen, Capacity: 30, WaitlistLimit: 8, Instructor: "Prof. Zhao"},
				meetings: []domain.Meeting{
					{Weekday: domain.Monday, StartMinute: 10 * 60, EndMinute: 11*60 + 40, Room: "E105"},
					{Weekday: domain.Thursday, StartMinute: 14 * 60, EndMinute: 15*60 + 40, Room: "E105"},
				},
			},
			{
				section: domain.Section{TermID: termID, CourseID: courseIDs["CS210"], Code: "CS210-A",
					Status: domain.SectionOpen, Capacity: 2, WaitlistLimit: 4, Instructor: "Prof. Qi"},
				meetings: []domain.Meeting{
					{Weekday: domain.Tuesday, StartMinute: 10 * 60, EndMinute: 11*60 + 40, Room: "E301"},
				},
			},
			{
				section: domain.Section{TermID: termID, CourseID: courseIDs["ORI100"], Code: "ORI100-A",
					Status: domain.SectionOpen, Capacity: 120, WaitlistLimit: 0, Instructor: "Student Life Office"},
				meetings: []domain.Meeting{
					{Weekday: domain.Friday, StartMinute: 15 * 60, EndMinute: 16*60 + 30, Room: "Auditorium"},
				},
			},
		}
		for _, entry := range sections {
			sectionID, err := d.insertSection(ctx, entry.section, spec.Now)
			if err != nil {
				return err
			}
			for _, meeting := range entry.meetings {
				meeting.SectionID = sectionID
				if err := d.insertMeeting(ctx, meeting); err != nil {
					return err
				}
			}
			result.SectionIDs = append(result.SectionIDs, sectionID)
		}

		return d.insertAcademicRecord(ctx, domain.AcademicRecord{
			StudentID:   student.ID,
			CourseCode:  "CS110",
			Grade:       "B",
			Credits:     4,
			CompletedAt: spec.Now.Add(-180 * 24 * time.Hour),
		})
	})
	if err != nil {
		return SeedResult{}, err
	}
	return result, nil
}

// inTxDataset runs fn inside a transaction with direct access to the package
// level dataset, which the seeding path needs for tables that have no public
// repository writer.
func (s *Store) inTxDataset(ctx context.Context, fn func(ctx context.Context, d *dataset) error) (err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin seed transaction: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	if err = fn(ctx, &dataset{q: tx}); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("commit seed transaction: %w", err)
	}
	committed = true
	return nil
}

func (d *dataset) insertTerm(ctx context.Context, term domain.Term) (int64, error) {
	if err := term.Validate(); err != nil {
		return 0, err
	}
	res, err := d.q.ExecContext(ctx, `
        INSERT INTO terms (code, name, enrollment_opens_at, enrollment_closes_at, add_drop_closes_at,
                           credit_limit, archived)
        VALUES (?, ?, ?, ?, ?, ?, ?)`,
		term.Code, term.Name, formatTime(term.EnrollmentOpensAt), formatTime(term.EnrollmentClosesAt),
		formatTime(term.AddDropClosesAt), term.CreditLimit, boolToInt(term.Archived))
	if err != nil {
		return 0, fmt.Errorf("insert term %s: %w", term.Code, err)
	}
	return res.LastInsertId()
}

func (d *dataset) insertCourse(ctx context.Context, course domain.Course) (int64, error) {
	if err := course.Validate(); err != nil {
		return 0, err
	}
	res, err := d.q.ExecContext(ctx, `
        INSERT INTO courses (code, title, credits, department, retired) VALUES (?, ?, ?, ?, ?)`,
		course.Code, course.Title, course.Credits, course.Department, boolToInt(course.Retired))
	if err != nil {
		return 0, fmt.Errorf("insert course %s: %w", course.Code, err)
	}
	return res.LastInsertId()
}

func (d *dataset) insertPrerequisite(ctx context.Context, courseID int64, requiredCode string, ordinal int) error {
	if _, err := d.q.ExecContext(ctx, `
        INSERT INTO course_prerequisites (course_id, required_course_code, ordinal) VALUES (?, ?, ?)`,
		courseID, requiredCode, ordinal); err != nil {
		return fmt.Errorf("insert prerequisite %s: %w", requiredCode, err)
	}
	return nil
}

func (d *dataset) insertSection(ctx context.Context, section domain.Section, now time.Time) (int64, error) {
	if section.Status == "" {
		section.Status = domain.SectionDraft
	}
	res, err := d.q.ExecContext(ctx, `
        INSERT INTO course_sections (term_id, course_id, code, status, capacity, seats_taken,
                                     waitlist_limit, waitlist_length, instructor, version, updated_at)
        VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 1, ?)`,
		section.TermID, section.CourseID, section.Code, string(section.Status), section.Capacity,
		section.SeatsTaken, section.WaitlistLimit, section.WaitlistLength, section.Instructor,
		formatTime(now))
	if err != nil {
		return 0, fmt.Errorf("insert section %s: %w", section.Code, err)
	}
	return res.LastInsertId()
}

func (d *dataset) insertMeeting(ctx context.Context, meeting domain.Meeting) error {
	if err := meeting.Validate(); err != nil {
		return err
	}
	if _, err := d.q.ExecContext(ctx, `
        INSERT INTO section_meetings (section_id, weekday, start_minute, end_minute, room)
        VALUES (?, ?, ?, ?, ?)`,
		meeting.SectionID, int(meeting.Weekday), meeting.StartMinute, meeting.EndMinute, meeting.Room); err != nil {
		return fmt.Errorf("insert meeting for section %d: %w", meeting.SectionID, err)
	}
	return nil
}

func (d *dataset) insertAcademicRecord(ctx context.Context, record domain.AcademicRecord) error {
	if _, err := d.q.ExecContext(ctx, `
        INSERT INTO academic_records (student_id, course_code, grade, credits, completed_at)
        VALUES (?, ?, ?, ?, ?)`,
		record.StudentID, record.CourseCode, record.Grade, record.Credits,
		formatTime(record.CompletedAt)); err != nil {
		return fmt.Errorf("insert academic record %s: %w", record.CourseCode, err)
	}
	return nil
}

// ProvisionTerm inserts a term definition. Catalogue bootstrap runs outside the
// student facing API, so it lives on the store rather than on a service.
func (s *Store) ProvisionTerm(ctx context.Context, term domain.Term) (int64, error) {
	var id int64
	err := s.inTxDataset(ctx, func(ctx context.Context, d *dataset) error {
		created, err := d.insertTerm(ctx, term)
		if err != nil {
			return err
		}
		id = created
		return nil
	})
	return id, err
}

// ProvisionCourse inserts a catalogue entry together with its prerequisites.
func (s *Store) ProvisionCourse(ctx context.Context, course domain.Course) (int64, error) {
	var id int64
	err := s.inTxDataset(ctx, func(ctx context.Context, d *dataset) error {
		created, err := d.insertCourse(ctx, course)
		if err != nil {
			return err
		}
		for ordinal, required := range course.Prerequisites {
			if err := d.insertPrerequisite(ctx, created, required, ordinal); err != nil {
				return err
			}
		}
		id = created
		return nil
	})
	return id, err
}

// ProvisionSection inserts a teaching section together with its weekly blocks.
func (s *Store) ProvisionSection(ctx context.Context, section domain.Section, now time.Time) (int64, error) {
	var id int64
	err := s.inTxDataset(ctx, func(ctx context.Context, d *dataset) error {
		created, err := d.insertSection(ctx, section, now)
		if err != nil {
			return err
		}
		for _, meeting := range section.Meetings {
			meeting.SectionID = created
			if err := d.insertMeeting(ctx, meeting); err != nil {
				return err
			}
		}
		id = created
		return nil
	})
	return id, err
}

// ProvisionAcademicRecord inserts a completed course record.
func (s *Store) ProvisionAcademicRecord(ctx context.Context, record domain.AcademicRecord) error {
	return s.inTxDataset(ctx, func(ctx context.Context, d *dataset) error {
		return d.insertAcademicRecord(ctx, record)
	})
}

// SetSectionStatus changes the lifecycle state of a section, used by the
// registrar tooling that closes or cancels an offering.
func (s *Store) SetSectionStatus(ctx context.Context, sectionID int64, status domain.SectionStatus, now time.Time) error {
	return s.inTxDataset(ctx, func(ctx context.Context, d *dataset) error {
		res, err := d.q.ExecContext(ctx, `
            UPDATE course_sections SET status = ?, version = version + 1, updated_at = ?
            WHERE id = ?`, string(status), formatTime(now), sectionID)
		if err != nil {
			return fmt.Errorf("set status of section %d: %w", sectionID, err)
		}
		affected, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("set status rows: %w", err)
		}
		if affected == 0 {
			return fmt.Errorf("section %d: %w", sectionID, domain.ErrNotFound)
		}
		return nil
	})
}
