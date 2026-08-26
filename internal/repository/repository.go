// Package repository declares the persistence contracts consumed by the
// service layer. The domain and service packages depend on these interfaces
// only, never on a concrete database driver.
package repository

import (
	"context"
	"time"

	"github.com/vance1852/orientation-enrollment-platform/internal/domain"
)

// UserRepository stores principals and their password digests.
type UserRepository interface {
	CreateUser(ctx context.Context, user domain.User) (domain.User, error)
	FindUserByEmail(ctx context.Context, email string) (domain.User, error)
	FindUserByID(ctx context.Context, id int64) (domain.User, error)
}

// SessionRepository stores revocable server side sessions.
type SessionRepository interface {
	CreateSession(ctx context.Context, session domain.Session) (domain.Session, error)
	FindSessionByDigest(ctx context.Context, digest string) (domain.Session, error)
	TouchSession(ctx context.Context, id int64, seenAt time.Time) error
	RevokeSession(ctx context.Context, id int64, at time.Time) error
	RevokeExpiredSessions(ctx context.Context, before time.Time, limit int) (int, error)
	CountActiveSessions(ctx context.Context, userID int64, now time.Time) (int, error)
}

// CatalogRepository serves terms, courses and sections.
type CatalogRepository interface {
	FindTermByID(ctx context.Context, id int64) (domain.Term, error)
	FindTermByCode(ctx context.Context, code string) (domain.Term, error)
	ListTerms(ctx context.Context, includeArchived bool) ([]domain.Term, error)
	FindCourseByID(ctx context.Context, id int64) (domain.Course, error)
	FindCourseByCode(ctx context.Context, code string) (domain.Course, error)
	CoursePrerequisites(ctx context.Context, courseID int64) ([]string, error)
	FindSectionByID(ctx context.Context, id int64) (domain.Section, error)
	FindSectionForUpdate(ctx context.Context, id int64) (domain.Section, error)
	ListSections(ctx context.Context, filter domain.SectionFilter) (domain.PageResult[domain.Section], error)
	SectionMeetings(ctx context.Context, sectionIDs []int64) (map[int64][]domain.Meeting, error)
	// ClaimSeat performs the conditional seat update that guards capacity.
	// It returns domain.ErrVersionConflict when another request advanced the
	// row, and domain.ErrCapacityExhausted when no seat is left.
	ClaimSeat(ctx context.Context, sectionID int64, expectedVersion int64, at time.Time) (domain.Section, error)
	ReleaseSeat(ctx context.Context, sectionID int64, expectedVersion int64, at time.Time) (domain.Section, error)
	AdjustWaitlistLength(ctx context.Context, sectionID int64, delta int, at time.Time) (domain.Section, error)
}

// RegistrationRepository stores orientation paperwork.
type RegistrationRepository interface {
	UpsertRegistration(ctx context.Context, registration domain.Registration) (domain.Registration, error)
	FindRegistration(ctx context.Context, studentID, termID int64) (domain.Registration, error)
	FindRegistrationByID(ctx context.Context, id int64) (domain.Registration, error)
	ListRegistrations(ctx context.Context, termID int64, status domain.RegistrationStatus, page domain.Page) (domain.PageResult[domain.Registration], error)
	StudentAcademicRecords(ctx context.Context, studentID int64) ([]domain.AcademicRecord, error)
}

// EnrollmentRepository stores seat claims and waitlist positions.
type EnrollmentRepository interface {
	CreateEnrollment(ctx context.Context, enrollment domain.Enrollment) (domain.Enrollment, error)
	UpdateEnrollment(ctx context.Context, enrollment domain.Enrollment, expectedVersion int64) (domain.Enrollment, error)
	FindEnrollmentByID(ctx context.Context, id int64) (domain.Enrollment, error)
	FindActiveEnrollment(ctx context.Context, studentID, sectionID int64) (domain.Enrollment, error)
	ListEnrollments(ctx context.Context, filter domain.EnrollmentFilter) (domain.PageResult[domain.Enrollment], error)
	ActiveEnrollmentsForStudent(ctx context.Context, studentID, termID int64) ([]domain.Enrollment, error)
	ActiveMeetingsForStudent(ctx context.Context, studentID, termID int64) ([]domain.Meeting, error)
	NextWaitlistRank(ctx context.Context, sectionID int64) (int, error)
	HeadOfWaitlist(ctx context.Context, sectionID int64) (domain.Enrollment, error)
	SectionRoster(ctx context.Context, sectionID int64, page domain.Page) (domain.PageResult[domain.Enrollment], error)
}

// IdempotencyRepository stores the replay protection records of mutating
// endpoints.
type IdempotencyRepository interface {
	FindIdempotencyRecord(ctx context.Context, actorID int64, method, path, key string) (IdempotencyRecord, error)
	SaveIdempotencyRecord(ctx context.Context, record IdempotencyRecord) error
	PurgeIdempotencyRecords(ctx context.Context, before time.Time) (int, error)
}

// IdempotencyRecord is the persisted snapshot of a completed mutation.
type IdempotencyRecord struct {
	ActorUserID        int64
	Method             string
	Path               string
	Key                string
	RequestFingerprint string
	ResponseStatus     int
	ResponseBody       string
	CreatedAt          time.Time
	ExpiresAt          time.Time
}

// AuditRepository appends and queries the audit trail.
type AuditRepository interface {
	AppendAuditEvent(ctx context.Context, event domain.AuditEvent) (domain.AuditEvent, error)
	ListAuditEvents(ctx context.Context, filter domain.AuditFilter) (domain.PageResult[domain.AuditEvent], error)
}

// JobRepository backs the durable background queue.
type JobRepository interface {
	EnqueueJob(ctx context.Context, job domain.Job) (domain.Job, error)
	ClaimNextJob(ctx context.Context, kinds []domain.JobKind, workerID string, now time.Time) (domain.Job, error)
	MarkJobSucceeded(ctx context.Context, id int64, at time.Time) error
	MarkJobRetry(ctx context.Context, id int64, runAfter time.Time, lastErr string, at time.Time) error
	MarkJobPermanentlyFailed(ctx context.Context, id int64, lastErr string, at time.Time) error
	RequeueStaleJobs(ctx context.Context, lockedBefore time.Time, at time.Time) (int, error)
	FindJobByID(ctx context.Context, id int64) (domain.Job, error)
	CountJobsByState(ctx context.Context, state domain.JobState) (int, error)
}

// Repositories groups every accessor available both on the root store and
// inside a transaction, so services can share one code path.
type Repositories interface {
	Users() UserRepository
	Sessions() SessionRepository
	Catalog() CatalogRepository
	Registrations() RegistrationRepository
	Enrollments() EnrollmentRepository
	Idempotency() IdempotencyRepository
	Audit() AuditRepository
	Jobs() JobRepository
}

// Store is the root persistence handle. InTx runs fn inside a single database
// transaction and rolls back when fn returns an error or panics.
type Store interface {
	Repositories
	InTx(ctx context.Context, fn func(ctx context.Context, tx Repositories) error) error
	Ping(ctx context.Context) error
	SchemaVersion(ctx context.Context) (int, error)
	Close() error
}
