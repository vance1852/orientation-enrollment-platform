package service_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/vance1852/orientation-enrollment-platform/internal/audit"
	"github.com/vance1852/orientation-enrollment-platform/internal/domain"
	"github.com/vance1852/orientation-enrollment-platform/internal/platform/clock"
	"github.com/vance1852/orientation-enrollment-platform/internal/platform/logging"
	"github.com/vance1852/orientation-enrollment-platform/internal/repository"
	"github.com/vance1852/orientation-enrollment-platform/internal/security"
	"github.com/vance1852/orientation-enrollment-platform/internal/service"
	"github.com/vance1852/orientation-enrollment-platform/internal/storage/sqlite"
)

const testPassword = "orientation-password-2026"

// harness assembles a real database, a deterministic clock and every service, so
// the tests exercise the same wiring the server uses.
type harness struct {
	t             *testing.T
	store         *sqlite.Store
	clock         *clock.Fixed
	deps          service.Deps
	auth          *service.AuthService
	catalog       *service.CatalogService
	registrations *service.RegistrationService
	enrollments   *service.EnrollmentService
	idempotency   *service.IdempotencyService

	term        domain.Term
	tightID     int64 // capacity 1, waitlist 2, Tuesday 10:00-11:40
	openID      int64 // capacity 5, no waitlist, Monday 08:00-09:40
	clashID     int64 // capacity 5, no waitlist, Tuesday 10:30-12:00
	heavyID     int64 // capacity 5, 10 credits, Friday 15:00-16:30
	registrar   domain.User
	student     domain.User
	otherStudnt domain.User
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	ctx := context.Background()
	dsn := "file:" + filepath.ToSlash(filepath.Join(t.TempDir(), "service-test.db"))
	store, err := sqlite.Open(ctx, dsn, sqlite.Options{MaxOpenConns: 8})
	if err != nil {
		t.Fatalf("opening the database failed: %v", err)
	}
	if _, err := sqlite.Migrate(ctx, store.DB()); err != nil {
		t.Fatalf("migrating failed: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	fixed := clock.NewFixed(time.Date(2026, time.August, 26, 10, 0, 0, 0, time.UTC))
	deps := service.Deps{
		Store:  store,
		Clock:  fixed,
		Audit:  audit.NewRecorder(func() time.Time { return fixed.Now() }),
		Logger: logging.Discard(),
	}
	h := &harness{t: t, store: store, clock: fixed, deps: deps}

	if h.auth, err = service.NewAuthService(deps, 2*time.Hour); err != nil {
		t.Fatalf("building the auth service failed: %v", err)
	}
	if h.catalog, err = service.NewCatalogService(deps); err != nil {
		t.Fatalf("building the catalog service failed: %v", err)
	}
	if h.registrations, err = service.NewRegistrationService(deps); err != nil {
		t.Fatalf("building the registration service failed: %v", err)
	}
	if h.enrollments, err = service.NewEnrollmentService(deps, 4); err != nil {
		t.Fatalf("building the enrollment service failed: %v", err)
	}
	if h.idempotency, err = service.NewIdempotencyService(deps); err != nil {
		t.Fatalf("building the idempotency service failed: %v", err)
	}

	h.seedCatalog()
	h.registrar = h.createUser("registrar@campus.example", domain.RoleRegistrar)
	h.student = h.createUser("student@campus.example", domain.RoleStudent)
	h.otherStudnt = h.createUser("other@campus.example", domain.RoleStudent)
	h.grantPrerequisite(h.student.ID)
	h.grantPrerequisite(h.otherStudnt.ID)
	h.verifyRegistration(h.student.ID)
	h.verifyRegistration(h.otherStudnt.ID)
	return h
}

func (h *harness) seedCatalog() {
	h.t.Helper()
	ctx := context.Background()
	now := h.clock.Now()

	termID, err := h.store.ProvisionTerm(ctx, domain.Term{
		Code: "2026-autumn", Name: "Autumn orientation term",
		EnrollmentOpensAt:  now.Add(-24 * time.Hour),
		EnrollmentClosesAt: now.Add(24 * time.Hour),
		AddDropClosesAt:    now.Add(48 * time.Hour),
		CreditLimit:        12,
	})
	if err != nil {
		h.t.Fatalf("provisioning the term failed: %v", err)
	}

	foundation, err := h.store.ProvisionCourse(ctx, domain.Course{
		Code: "CS110", Title: "Programming fundamentals", Credits: 4, Department: "computing"})
	if err != nil {
		h.t.Fatalf("provisioning CS110 failed: %v", err)
	}
	advanced, err := h.store.ProvisionCourse(ctx, domain.Course{
		Code: "CS210", Title: "Data structures", Credits: 4, Department: "computing",
		Prerequisites: []string{"CS110"}})
	if err != nil {
		h.t.Fatalf("provisioning CS210 failed: %v", err)
	}
	math, err := h.store.ProvisionCourse(ctx, domain.Course{
		Code: "MATH101", Title: "Calculus", Credits: 4, Department: "mathematics"})
	if err != nil {
		h.t.Fatalf("provisioning MATH101 failed: %v", err)
	}
	capstone, err := h.store.ProvisionCourse(ctx, domain.Course{
		Code: "ORI900", Title: "Orientation capstone", Credits: 10, Department: "student-life"})
	if err != nil {
		h.t.Fatalf("provisioning ORI900 failed: %v", err)
	}

	if h.tightID, err = h.store.ProvisionSection(ctx, domain.Section{
		TermID: termID, CourseID: advanced, Code: "CS210-A", Status: domain.SectionOpen,
		Capacity: 1, WaitlistLimit: 2, Instructor: "Prof. Qi",
		Meetings: []domain.Meeting{{Weekday: domain.Tuesday, StartMinute: 600, EndMinute: 700, Room: "E301"}},
	}, now); err != nil {
		h.t.Fatalf("provisioning CS210-A failed: %v", err)
	}
	if h.openID, err = h.store.ProvisionSection(ctx, domain.Section{
		TermID: termID, CourseID: foundation, Code: "CS110-A", Status: domain.SectionOpen,
		Capacity: 5, Instructor: "Prof. Zhao",
		Meetings: []domain.Meeting{{Weekday: domain.Monday, StartMinute: 480, EndMinute: 580, Room: "E105"}},
	}, now); err != nil {
		h.t.Fatalf("provisioning CS110-A failed: %v", err)
	}
	if h.clashID, err = h.store.ProvisionSection(ctx, domain.Section{
		TermID: termID, CourseID: math, Code: "MATH101-A", Status: domain.SectionOpen,
		Capacity: 5, Instructor: "Prof. Lin",
		Meetings: []domain.Meeting{{Weekday: domain.Tuesday, StartMinute: 630, EndMinute: 720, Room: "N201"}},
	}, now); err != nil {
		h.t.Fatalf("provisioning MATH101-A failed: %v", err)
	}
	if h.heavyID, err = h.store.ProvisionSection(ctx, domain.Section{
		TermID: termID, CourseID: capstone, Code: "ORI900-A", Status: domain.SectionOpen,
		Capacity: 5, Instructor: "Student Life Office",
		Meetings: []domain.Meeting{{Weekday: domain.Friday, StartMinute: 900, EndMinute: 990, Room: "Auditorium"}},
	}, now); err != nil {
		h.t.Fatalf("provisioning ORI900-A failed: %v", err)
	}

	if h.term, err = h.store.Catalog().FindTermByID(ctx, termID); err != nil {
		h.t.Fatalf("reading the term failed: %v", err)
	}
}

func (h *harness) createUser(email string, role domain.Role) domain.User {
	h.t.Helper()
	hash, err := security.HashNewPassword(testPassword)
	if err != nil {
		h.t.Fatalf("hashing failed: %v", err)
	}
	now := h.clock.Now()
	user, err := h.store.Users().CreateUser(context.Background(), domain.User{
		Email: email, DisplayName: email, Role: role, PasswordHash: hash,
		CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		h.t.Fatalf("creating %s failed: %v", email, err)
	}
	return user
}

func (h *harness) grantPrerequisite(studentID int64) {
	h.t.Helper()
	if err := h.store.ProvisionAcademicRecord(context.Background(), domain.AcademicRecord{
		StudentID: studentID, CourseCode: "CS110", Grade: "B", Credits: 4,
		CompletedAt: h.clock.Now().Add(-180 * 24 * time.Hour),
	}); err != nil {
		h.t.Fatalf("granting the prerequisite failed: %v", err)
	}
}

// verifyRegistration walks the real paperwork flow so the enrollment tests start
// from a state a student could actually reach.
func (h *harness) verifyRegistration(studentID int64) {
	h.t.Helper()
	ctx := context.Background()
	student := domain.Principal{UserID: studentID, Role: domain.RoleStudent}
	registration, err := h.registrations.Submit(ctx, student, service.SubmitInput{
		StudentID: studentID, TermID: h.term.ID,
		ProgramCode: "CS-BSC", AdvisorEmail: "advisor@campus.example", DormPreference: "on_campus",
	})
	if err != nil {
		h.t.Fatalf("submitting the registration failed: %v", err)
	}
	if _, err := h.registrations.Decide(ctx, h.principal(h.registrar), service.DecideInput{
		RegistrationID: registration.ID, Status: domain.RegistrationVerified,
	}); err != nil {
		h.t.Fatalf("verifying the registration failed: %v", err)
	}
}

func (h *harness) principal(user domain.User) domain.Principal {
	return domain.Principal{UserID: user.ID, SessionID: user.ID * 1000, Email: user.Email,
		DisplayName: user.DisplayName, Role: user.Role}
}

func (h *harness) studentPrincipal() domain.Principal   { return h.principal(h.student) }
func (h *harness) registrarPrincipal() domain.Principal { return h.principal(h.registrar) }

func (h *harness) section(id int64) domain.Section {
	h.t.Helper()
	section, err := h.store.Catalog().FindSectionByID(context.Background(), id)
	if err != nil {
		h.t.Fatalf("reading section %d failed: %v", id, err)
	}
	return section
}

// enrollAnotherStudent fills a seat with a freshly created verified student.
func (h *harness) enrollAnotherStudent(email string, sectionID int64) domain.Enrollment {
	h.t.Helper()
	user := h.createUser(email, domain.RoleStudent)
	h.grantPrerequisite(user.ID)
	h.verifyRegistration(user.ID)
	result, err := h.enrollments.Claim(context.Background(), h.principal(user),
		service.ClaimInput{StudentID: user.ID, SectionID: sectionID})
	if err != nil {
		h.t.Fatalf("filling a seat with %s failed: %v", email, err)
	}
	return result.Enrollment
}

// auditCount reports how many trail entries match an action, which the tests use
// to prove that a rejected request is recorded too.
func (h *harness) auditCount(action domain.AuditAction, result domain.AuditResult) int {
	h.t.Helper()
	page, err := h.store.Audit().ListAuditEvents(context.Background(), domain.AuditFilter{
		Action: string(action), Page: domain.Page{Size: domain.MaxPageSize},
	})
	if err != nil {
		h.t.Fatalf("reading the audit trail failed: %v", err)
	}
	count := 0
	for _, event := range page.Items {
		if event.Result == result {
			count++
		}
	}
	return count
}

// failingAuditStore makes the audit insert fail so the tests can prove the
// business write is rolled back with it.
type failingAuditStore struct {
	repository.Store
	err error
}

func (f *failingAuditStore) InTx(ctx context.Context, fn func(context.Context, repository.Repositories) error) error {
	return f.Store.InTx(ctx, func(ctx context.Context, tx repository.Repositories) error {
		return fn(ctx, &failingAuditRepos{Repositories: tx, err: f.err})
	})
}

type failingAuditRepos struct {
	repository.Repositories
	err error
}

func (f *failingAuditRepos) Audit() repository.AuditRepository {
	return failingAuditRepo{err: f.err}
}

type failingAuditRepo struct {
	err error
}

func (f failingAuditRepo) AppendAuditEvent(context.Context, domain.AuditEvent) (domain.AuditEvent, error) {
	return domain.AuditEvent{}, f.err
}

func (f failingAuditRepo) ListAuditEvents(context.Context, domain.AuditFilter) (domain.PageResult[domain.AuditEvent], error) {
	return domain.PageResult[domain.AuditEvent]{}, f.err
}
