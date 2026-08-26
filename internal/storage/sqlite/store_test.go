package sqlite_test

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/vance1852/orientation-enrollment-platform/internal/domain"
	"github.com/vance1852/orientation-enrollment-platform/internal/migrations"
	"github.com/vance1852/orientation-enrollment-platform/internal/repository"
	"github.com/vance1852/orientation-enrollment-platform/internal/security"
	"github.com/vance1852/orientation-enrollment-platform/internal/storage/sqlite"
)

func dsnFor(t *testing.T) string {
	t.Helper()
	return "file:" + filepath.ToSlash(filepath.Join(t.TempDir(), "orientation-test.db"))
}

func openStore(t *testing.T, dsn string) *sqlite.Store {
	t.Helper()
	ctx := context.Background()
	store, err := sqlite.Open(ctx, dsn, sqlite.Options{MaxOpenConns: 4})
	if err != nil {
		t.Fatalf("opening the database failed: %v", err)
	}
	if _, err := sqlite.Migrate(ctx, store.DB()); err != nil {
		_ = store.Close()
		t.Fatalf("migrating the database failed: %v", err)
	}
	return store
}

func newStore(t *testing.T) *sqlite.Store {
	t.Helper()
	store := openStore(t, dsnFor(t))
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func mustUser(t *testing.T, store *sqlite.Store, email string, role domain.Role) domain.User {
	t.Helper()
	hash, err := security.HashNewPassword("orientation-password-2026")
	if err != nil {
		t.Fatalf("hashing failed: %v", err)
	}
	now := time.Now().UTC()
	user, err := store.Users().CreateUser(context.Background(), domain.User{
		Email: email, DisplayName: email, Role: role, PasswordHash: hash,
		CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		t.Fatalf("creating user %s failed: %v", email, err)
	}
	return user
}

type catalogFixture struct {
	term    domain.Term
	course  domain.Course
	section domain.Section
}

func mustCatalog(t *testing.T, store *sqlite.Store, capacity, waitlist int) catalogFixture {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()

	termID, err := store.ProvisionTerm(ctx, domain.Term{
		Code: "2026-autumn", Name: "Autumn",
		EnrollmentOpensAt:  now.Add(-time.Hour),
		EnrollmentClosesAt: now.Add(30 * 24 * time.Hour),
		AddDropClosesAt:    now.Add(45 * 24 * time.Hour),
		CreditLimit:        18,
	})
	if err != nil {
		t.Fatalf("provisioning the term failed: %v", err)
	}
	courseID, err := store.ProvisionCourse(ctx, domain.Course{
		Code: "CS210", Title: "Data structures", Credits: 4, Department: "computing",
	})
	if err != nil {
		t.Fatalf("provisioning the course failed: %v", err)
	}
	sectionID, err := store.ProvisionSection(ctx, domain.Section{
		TermID: termID, CourseID: courseID, Code: "CS210-A", Status: domain.SectionOpen,
		Capacity: capacity, WaitlistLimit: waitlist, Instructor: "Prof. Qi",
		Meetings: []domain.Meeting{
			{Weekday: domain.Tuesday, StartMinute: 600, EndMinute: 700, Room: "E301"},
			{Weekday: domain.Thursday, StartMinute: 600, EndMinute: 700, Room: "E301"},
		},
	}, now)
	if err != nil {
		t.Fatalf("provisioning the section failed: %v", err)
	}

	term, err := store.Catalog().FindTermByID(ctx, termID)
	if err != nil {
		t.Fatalf("reading the term failed: %v", err)
	}
	course, err := store.Catalog().FindCourseByID(ctx, courseID)
	if err != nil {
		t.Fatalf("reading the course failed: %v", err)
	}
	section, err := store.Catalog().FindSectionByID(ctx, sectionID)
	if err != nil {
		t.Fatalf("reading the section failed: %v", err)
	}
	return catalogFixture{term: term, course: course, section: section}
}

func TestMigrateIsIdempotentAcrossRestarts(t *testing.T) {
	ctx := context.Background()
	dsn := dsnFor(t)

	first := openStore(t, dsn)
	expected, err := migrations.LatestVersion()
	if err != nil {
		t.Fatalf("reading the latest version failed: %v", err)
	}
	version, err := first.SchemaVersion(ctx)
	if err != nil {
		t.Fatalf("reading the applied version failed: %v", err)
	}
	if version != expected {
		t.Fatalf("schema version = %d, want %d", version, expected)
	}
	again, err := sqlite.Migrate(ctx, first.DB())
	if err != nil {
		t.Fatalf("re-running the migration failed: %v", err)
	}
	if !again.AlreadyCurrent || len(again.Applied) != 0 {
		t.Fatalf("a second run must be a no-op, got %+v", again)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("closing failed: %v", err)
	}

	second, err := sqlite.Open(ctx, dsn, sqlite.Options{})
	if err != nil {
		t.Fatalf("reopening failed: %v", err)
	}
	defer func() { _ = second.Close() }()
	outcome, err := sqlite.Migrate(ctx, second.DB())
	if err != nil {
		t.Fatalf("migrating an existing database failed: %v", err)
	}
	if !outcome.AlreadyCurrent {
		t.Fatal("reopening an up to date database must not apply migrations")
	}
}

func TestMigrateReportsChecksumDrift(t *testing.T) {
	ctx := context.Background()
	store := newStore(t)

	if _, err := store.DB().ExecContext(ctx,
		`UPDATE schema_migrations SET checksum = 'tampered' WHERE version = 2`); err != nil {
		t.Fatalf("tampering with the ledger failed: %v", err)
	}
	_, err := sqlite.Migrate(ctx, store.DB())
	if !errors.Is(err, domain.ErrMigrationDrift) {
		t.Fatalf("expected ErrMigrationDrift, got %v", err)
	}
}

func TestMigrateReportsUnknownFutureVersion(t *testing.T) {
	ctx := context.Background()
	store := newStore(t)

	if _, err := store.DB().ExecContext(ctx,
		`INSERT INTO schema_migrations (version, name, checksum, applied_at) VALUES (999, 'future', 'x', ?)`,
		time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatalf("inserting a future version failed: %v", err)
	}
	_, err := sqlite.Migrate(ctx, store.DB())
	if !errors.Is(err, domain.ErrMigrationDrift) {
		t.Fatalf("expected ErrMigrationDrift, got %v", err)
	}
}

func TestSeedIsIdempotent(t *testing.T) {
	ctx := context.Background()
	store := newStore(t)
	spec := sqlite.SeedSpec{
		Now:            time.Now().UTC(),
		RegistrarEmail: "registrar@campus.example",
		RegistrarPass:  "orientation-registrar-2026",
		StudentEmail:   "student@campus.example",
		StudentPass:    "orientation-student-2026",
		TermCode:       "2026-autumn",
	}

	first, err := sqlite.Seed(ctx, store, spec)
	if err != nil {
		t.Fatalf("seeding failed: %v", err)
	}
	if first.Skipped || first.TermID == 0 || len(first.SectionIDs) == 0 {
		t.Fatalf("first seed = %+v", first)
	}
	second, err := sqlite.Seed(ctx, store, spec)
	if err != nil {
		t.Fatalf("re-seeding failed: %v", err)
	}
	if !second.Skipped {
		t.Fatal("a second seed must be skipped")
	}
	if _, err := store.Users().FindUserByEmail(ctx, "student@campus.example"); err != nil {
		t.Fatalf("the seeded student is missing: %v", err)
	}
}

func TestCreateUserRejectsDuplicateEmail(t *testing.T) {
	store := newStore(t)
	mustUser(t, store, "student@campus.example", domain.RoleStudent)

	hash, err := security.HashNewPassword("orientation-password-2026")
	if err != nil {
		t.Fatalf("hashing failed: %v", err)
	}
	now := time.Now().UTC()
	_, err = store.Users().CreateUser(context.Background(), domain.User{
		Email: "STUDENT@campus.example", DisplayName: "dup", Role: domain.RoleStudent,
		PasswordHash: hash, CreatedAt: now, UpdatedAt: now,
	})
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("expected a conflict on a duplicate address, got %v", err)
	}
	if _, err := store.Users().FindUserByEmail(context.Background(), "nobody@campus.example"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestClaimSeatEnforcesVersionAndCapacity(t *testing.T) {
	ctx := context.Background()
	store := newStore(t)
	fixture := mustCatalog(t, store, 1, 2)

	claimed, err := store.Catalog().ClaimSeat(ctx, fixture.section.ID, fixture.section.Version, time.Now().UTC())
	if err != nil {
		t.Fatalf("claiming the only seat failed: %v", err)
	}
	if claimed.SeatsTaken != 1 || claimed.Version != fixture.section.Version+1 {
		t.Fatalf("section after the claim = %+v", claimed)
	}

	if _, err := store.Catalog().ClaimSeat(ctx, fixture.section.ID, fixture.section.Version, time.Now().UTC()); !errors.Is(err, domain.ErrVersionConflict) {
		t.Fatalf("a stale version must be rejected, got %v", err)
	}
	if _, err := store.Catalog().ClaimSeat(ctx, fixture.section.ID, claimed.Version, time.Now().UTC()); !errors.Is(err, domain.ErrCapacityExhausted) {
		t.Fatalf("a full section must report capacity_exhausted, got %v", err)
	}

	if err := store.SetSectionStatus(ctx, fixture.section.ID, domain.SectionClosed, time.Now().UTC()); err != nil {
		t.Fatalf("closing the section failed: %v", err)
	}
	current, err := store.Catalog().FindSectionByID(ctx, fixture.section.ID)
	if err != nil {
		t.Fatalf("reading the section failed: %v", err)
	}
	_, err = store.Catalog().ClaimSeat(ctx, current.ID, current.Version, time.Now().UTC())
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("a closed section must reject claims, got %v", err)
	}
}

func TestReleaseSeatAndWaitlistCounters(t *testing.T) {
	ctx := context.Background()
	store := newStore(t)
	fixture := mustCatalog(t, store, 2, 1)
	now := time.Now().UTC()

	claimed, err := store.Catalog().ClaimSeat(ctx, fixture.section.ID, fixture.section.Version, now)
	if err != nil {
		t.Fatalf("claiming failed: %v", err)
	}
	released, err := store.Catalog().ReleaseSeat(ctx, claimed.ID, claimed.Version, now)
	if err != nil {
		t.Fatalf("releasing failed: %v", err)
	}
	if released.SeatsTaken != 0 {
		t.Fatalf("seats taken after release = %d", released.SeatsTaken)
	}
	if _, err := store.Catalog().ReleaseSeat(ctx, released.ID, released.Version, now); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("releasing an empty section must fail, got %v", err)
	}

	withWaitlist, err := store.Catalog().AdjustWaitlistLength(ctx, released.ID, 1, now)
	if err != nil {
		t.Fatalf("growing the waitlist failed: %v", err)
	}
	if withWaitlist.WaitlistLength != 1 {
		t.Fatalf("waitlist length = %d", withWaitlist.WaitlistLength)
	}
	if _, err := store.Catalog().AdjustWaitlistLength(ctx, released.ID, 1, now); !errors.Is(err, domain.ErrWaitlistFull) {
		t.Fatalf("exceeding the waitlist limit must fail, got %v", err)
	}
	if _, err := store.Catalog().AdjustWaitlistLength(ctx, released.ID, -2, now); !errors.Is(err, domain.ErrWaitlistFull) {
		t.Fatalf("driving the waitlist negative must fail, got %v", err)
	}
	unchanged, err := store.Catalog().AdjustWaitlistLength(ctx, released.ID, 0, now)
	if err != nil {
		t.Fatalf("a zero adjustment must be a no-op, got %v", err)
	}
	if unchanged.Version != withWaitlist.Version {
		t.Fatalf("a zero adjustment must not bump the version")
	}
}

func TestActiveSeatIndexAllowsReclaimAfterRelease(t *testing.T) {
	ctx := context.Background()
	store := newStore(t)
	fixture := mustCatalog(t, store, 5, 0)
	student := mustUser(t, store, "student@campus.example", domain.RoleStudent)
	now := time.Now().UTC()

	base := domain.Enrollment{
		StudentID: student.ID, TermID: fixture.term.ID, SectionID: fixture.section.ID,
		CourseCode: fixture.course.Code, Credits: fixture.course.Credits,
		Status: domain.EnrollmentEnrolled, RequestedAt: now, CreatedAt: now, UpdatedAt: now,
	}
	created, err := store.Enrollments().CreateEnrollment(ctx, base)
	if err != nil {
		t.Fatalf("creating the first claim failed: %v", err)
	}
	if _, err := store.Enrollments().CreateEnrollment(ctx, base); !errors.Is(err, domain.ErrDuplicateEnrollment) {
		t.Fatalf("a second active claim must be rejected, got %v", err)
	}

	dropped := created
	if err := dropped.Transition(domain.EnrollmentDropped, now, "test"); err != nil {
		t.Fatalf("transition failed: %v", err)
	}
	if _, err := store.Enrollments().UpdateEnrollment(ctx, dropped, created.Version); err != nil {
		t.Fatalf("updating the claim failed: %v", err)
	}
	if _, err := store.Enrollments().UpdateEnrollment(ctx, dropped, created.Version); !errors.Is(err, domain.ErrVersionConflict) {
		t.Fatalf("a stale version must be rejected, got %v", err)
	}
	if _, err := store.Enrollments().CreateEnrollment(ctx, base); err != nil {
		t.Fatalf("re-claiming after a release must be allowed, got %v", err)
	}
}

func TestTransactionRollbackLeavesNoPartialState(t *testing.T) {
	ctx := context.Background()
	store := newStore(t)
	fixture := mustCatalog(t, store, 3, 0)
	student := mustUser(t, store, "student@campus.example", domain.RoleStudent)
	now := time.Now().UTC()
	sentinel := errors.New("audit sink unavailable")

	err := store.InTx(ctx, func(ctx context.Context, tx repository.Repositories) error {
		if _, err := tx.Catalog().ClaimSeat(ctx, fixture.section.ID, fixture.section.Version, now); err != nil {
			return err
		}
		if _, err := tx.Enrollments().CreateEnrollment(ctx, domain.Enrollment{
			StudentID: student.ID, TermID: fixture.term.ID, SectionID: fixture.section.ID,
			CourseCode: fixture.course.Code, Credits: fixture.course.Credits,
			Status: domain.EnrollmentEnrolled, RequestedAt: now, CreatedAt: now, UpdatedAt: now,
		}); err != nil {
			return err
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("expected the sentinel error, got %v", err)
	}

	section, err := store.Catalog().FindSectionByID(ctx, fixture.section.ID)
	if err != nil {
		t.Fatalf("reading the section failed: %v", err)
	}
	if section.SeatsTaken != 0 || section.Version != fixture.section.Version {
		t.Fatalf("the seat update survived the rollback: %+v", section)
	}
	enrollments, err := store.Enrollments().ActiveEnrollmentsForStudent(ctx, student.ID, fixture.term.ID)
	if err != nil {
		t.Fatalf("reading enrollments failed: %v", err)
	}
	if len(enrollments) != 0 {
		t.Fatalf("the enrollment survived the rollback: %+v", enrollments)
	}
}

func TestListSectionsFiltersPaginatesAndSorts(t *testing.T) {
	ctx := context.Background()
	store := newStore(t)
	fixture := mustCatalog(t, store, 1, 0)
	now := time.Now().UTC()

	otherCourse, err := store.ProvisionCourse(ctx, domain.Course{
		Code: "MATH101", Title: "Calculus", Credits: 4, Department: "mathematics"})
	if err != nil {
		t.Fatalf("provisioning the second course failed: %v", err)
	}
	for i, code := range []string{"MATH101-A", "MATH101-B", "MATH101-C"} {
		if _, err := store.ProvisionSection(ctx, domain.Section{
			TermID: fixture.term.ID, CourseID: otherCourse, Code: code,
			Status: domain.SectionOpen, Capacity: 10 + i, Instructor: "Prof. Lin",
		}, now); err != nil {
			t.Fatalf("provisioning %s failed: %v", code, err)
		}
	}

	all, err := store.Catalog().ListSections(ctx, domain.SectionFilter{TermID: fixture.term.ID,
		Page: domain.Page{Number: 1, Size: 2, SortBy: "code", Order: domain.SortAscending}})
	if err != nil {
		t.Fatalf("listing sections failed: %v", err)
	}
	if all.Total != 4 || all.TotalPages != 2 || len(all.Items) != 2 {
		t.Fatalf("page meta = %+v with %d items", all, len(all.Items))
	}
	if all.Items[0].Code != "CS210-A" {
		t.Fatalf("ascending order returned %s first", all.Items[0].Code)
	}

	descending, err := store.Catalog().ListSections(ctx, domain.SectionFilter{TermID: fixture.term.ID,
		Page: domain.Page{Number: 1, Size: 2, SortBy: "code", Order: domain.SortDescending}})
	if err != nil {
		t.Fatalf("listing sections failed: %v", err)
	}
	if descending.Items[0].Code != "MATH101-C" {
		t.Fatalf("descending order returned %s first", descending.Items[0].Code)
	}

	byCourse, err := store.Catalog().ListSections(ctx, domain.SectionFilter{
		TermID: fixture.term.ID, CourseCode: "math101", Page: domain.Page{Size: 10}})
	if err != nil {
		t.Fatalf("filtering by course failed: %v", err)
	}
	if byCourse.Total != 3 {
		t.Fatalf("course filter total = %d, want 3", byCourse.Total)
	}

	// A full section must disappear from the only_open view, and the reported
	// total must shrink with it.
	if _, err := store.Catalog().ClaimSeat(ctx, fixture.section.ID, fixture.section.Version, now); err != nil {
		t.Fatalf("filling the section failed: %v", err)
	}
	open, err := store.Catalog().ListSections(ctx, domain.SectionFilter{
		TermID: fixture.term.ID, OnlyOpen: true, Page: domain.Page{Size: 10}})
	if err != nil {
		t.Fatalf("filtering open sections failed: %v", err)
	}
	if open.Total != 3 || len(open.Items) != 3 {
		t.Fatalf("only_open total = %d with %d items, want 3", open.Total, len(open.Items))
	}

	byDepartment, err := store.Catalog().ListSections(ctx, domain.SectionFilter{
		Department: "computing", Page: domain.Page{Size: 10}})
	if err != nil {
		t.Fatalf("filtering by department failed: %v", err)
	}
	if byDepartment.Total != 1 {
		t.Fatalf("department filter total = %d, want 1", byDepartment.Total)
	}

	if _, err := store.Catalog().ListSections(ctx, domain.SectionFilter{
		Page: domain.Page{SortBy: "capacity; DROP TABLE users"}}); !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("an unknown sort key must be rejected, got %v", err)
	}
}

func TestSectionMeetingsAreIsolatedPerSection(t *testing.T) {
	ctx := context.Background()
	store := newStore(t)
	fixture := mustCatalog(t, store, 1, 0)

	first, err := store.Catalog().FindSectionByID(ctx, fixture.section.ID)
	if err != nil {
		t.Fatalf("reading the section failed: %v", err)
	}
	first.Meetings[0].Room = "mutated"

	second, err := store.Catalog().FindSectionByID(ctx, fixture.section.ID)
	if err != nil {
		t.Fatalf("re-reading the section failed: %v", err)
	}
	if second.Meetings[0].Room != "E301" {
		t.Fatal("mutating one result must not affect the next read")
	}

	empty, err := store.Catalog().SectionMeetings(ctx, nil)
	if err != nil {
		t.Fatalf("reading meetings without ids failed: %v", err)
	}
	if len(empty) != 0 {
		t.Fatalf("expected no meetings, got %d", len(empty))
	}
}

func TestSessionSweepOnlyRevokesExpiredSessions(t *testing.T) {
	ctx := context.Background()
	store := newStore(t)
	user := mustUser(t, store, "student@campus.example", domain.RoleStudent)
	now := time.Now().UTC()

	live, err := store.Sessions().CreateSession(ctx, domain.Session{
		UserID: user.ID, TokenDigest: security.TokenDigest("live"),
		IssuedAt: now.Add(-time.Hour), ExpiresAt: now.Add(time.Hour), LastSeenAt: now,
	})
	if err != nil {
		t.Fatalf("creating the live session failed: %v", err)
	}
	if _, err := store.Sessions().CreateSession(ctx, domain.Session{
		UserID: user.ID, TokenDigest: security.TokenDigest("stale"),
		IssuedAt: now.Add(-2 * time.Hour), ExpiresAt: now.Add(-time.Hour), LastSeenAt: now,
	}); err != nil {
		t.Fatalf("creating the stale session failed: %v", err)
	}

	active, err := store.Sessions().CountActiveSessions(ctx, user.ID, now)
	if err != nil {
		t.Fatalf("counting sessions failed: %v", err)
	}
	if active != 1 {
		t.Fatalf("active sessions = %d, want 1", active)
	}

	revoked, err := store.Sessions().RevokeExpiredSessions(ctx, now, 0)
	if err != nil {
		t.Fatalf("sweeping failed: %v", err)
	}
	if revoked != 1 {
		t.Fatalf("revoked = %d, want 1", revoked)
	}
	if err := store.Sessions().TouchSession(ctx, live.ID, now); err != nil {
		t.Fatalf("the live session must remain usable: %v", err)
	}
	if err := store.Sessions().RevokeSession(ctx, live.ID, now); err != nil {
		t.Fatalf("revoking the live session failed: %v", err)
	}
	if err := store.Sessions().RevokeSession(ctx, live.ID, now); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("revoking twice must report not found, got %v", err)
	}
	if err := store.Sessions().TouchSession(ctx, live.ID, now); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("touching a revoked session must fail, got %v", err)
	}
}

func TestJobQueueLifecycleAndStaleRecovery(t *testing.T) {
	ctx := context.Background()
	store := newStore(t)
	now := time.Now().UTC()

	queued, err := store.Jobs().EnqueueJob(ctx, domain.Job{
		Kind: domain.JobPromoteWaitlist, Payload: `{"section_id":1}`,
		RunAfter: now, CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		t.Fatalf("enqueueing failed: %v", err)
	}
	if queued.MaxAttempts != domain.MaxJobAttempts || queued.State != domain.JobQueued {
		t.Fatalf("queued job = %+v", queued)
	}

	claimed, err := store.Jobs().ClaimNextJob(ctx, []domain.JobKind{domain.JobPromoteWaitlist}, "worker-a", now)
	if err != nil {
		t.Fatalf("claiming failed: %v", err)
	}
	if claimed.State != domain.JobRunning || claimed.Attempts != 1 || claimed.LockedBy != "worker-a" {
		t.Fatalf("claimed job = %+v", claimed)
	}
	if _, err := store.Jobs().ClaimNextJob(ctx, []domain.JobKind{domain.JobPromoteWaitlist}, "worker-b", now); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("a leased job must not be claimed twice, got %v", err)
	}

	if err := store.Jobs().MarkJobRetry(ctx, claimed.ID, now.Add(time.Minute), "boom", now); err != nil {
		t.Fatalf("scheduling a retry failed: %v", err)
	}
	if _, err := store.Jobs().ClaimNextJob(ctx, []domain.JobKind{domain.JobPromoteWaitlist}, "worker-b", now); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("a backing off job must not be claimable, got %v", err)
	}
	later, err := store.Jobs().ClaimNextJob(ctx, []domain.JobKind{domain.JobPromoteWaitlist}, "worker-b", now.Add(2*time.Minute))
	if err != nil {
		t.Fatalf("claiming after the backoff failed: %v", err)
	}
	if later.Attempts != 2 || later.LastError != "boom" {
		t.Fatalf("job after the retry = %+v", later)
	}

	// A crashed worker leaves the row in the running state; recovery must put it
	// back on the queue without losing the attempt history.
	requeued, err := store.Jobs().RequeueStaleJobs(ctx, now.Add(time.Hour), now.Add(time.Hour))
	if err != nil {
		t.Fatalf("requeueing failed: %v", err)
	}
	if requeued != 1 {
		t.Fatalf("requeued = %d, want 1", requeued)
	}
	recovered, err := store.Jobs().FindJobByID(ctx, later.ID)
	if err != nil {
		t.Fatalf("reading the job failed: %v", err)
	}
	if recovered.State != domain.JobQueued || recovered.Attempts != 2 || recovered.LockedBy != "" {
		t.Fatalf("recovered job = %+v", recovered)
	}

	final, err := store.Jobs().ClaimNextJob(ctx, []domain.JobKind{domain.JobPromoteWaitlist}, "worker-c", now.Add(2*time.Hour))
	if err != nil {
		t.Fatalf("claiming the recovered job failed: %v", err)
	}
	if err := store.Jobs().MarkJobPermanentlyFailed(ctx, final.ID, "gave up", now.Add(2*time.Hour)); err != nil {
		t.Fatalf("retiring the job failed: %v", err)
	}
	if err := store.Jobs().MarkJobSucceeded(ctx, final.ID, now.Add(2*time.Hour)); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("a retired job must not be closed again, got %v", err)
	}
	failedCount, err := store.Jobs().CountJobsByState(ctx, domain.JobPermanentlyFailed)
	if err != nil {
		t.Fatalf("counting failed jobs failed: %v", err)
	}
	if failedCount != 1 {
		t.Fatalf("permanently failed jobs = %d, want 1", failedCount)
	}
}

func TestAuditTrailFiltersAndPaginates(t *testing.T) {
	ctx := context.Background()
	store := newStore(t)
	user := mustUser(t, store, "registrar@campus.example", domain.RoleRegistrar)
	now := time.Now().UTC()

	for i := 0; i < 5; i++ {
		action := domain.ActionEnrollmentClaim
		if i%2 == 0 {
			action = domain.ActionEnrollmentDrop
		}
		if _, err := store.Audit().AppendAuditEvent(ctx, domain.AuditEvent{
			ActorUserID: &user.ID, ActorRole: string(user.Role), Action: action,
			ObjectType: "enrollment", ObjectID: fmt.Sprintf("%d", i+1),
			Result: domain.ResultSuccess, RequestID: "req_test", Detail: "seeded",
			OccurredAt: now.Add(time.Duration(i) * time.Second),
		}); err != nil {
			t.Fatalf("appending audit event %d failed: %v", i, err)
		}
	}

	page, err := store.Audit().ListAuditEvents(ctx, domain.AuditFilter{
		Page: domain.Page{Number: 1, Size: 2, SortBy: "occurred_at", Order: domain.SortDescending}})
	if err != nil {
		t.Fatalf("listing audit events failed: %v", err)
	}
	if page.Total != 5 || page.TotalPages != 3 || len(page.Items) != 2 {
		t.Fatalf("audit page = %+v", page)
	}
	if page.Items[0].ObjectID != "5" {
		t.Fatalf("descending order returned object %s first", page.Items[0].ObjectID)
	}

	filtered, err := store.Audit().ListAuditEvents(ctx, domain.AuditFilter{
		Action: string(domain.ActionEnrollmentDrop), Page: domain.Page{Size: 10}})
	if err != nil {
		t.Fatalf("filtering by action failed: %v", err)
	}
	if filtered.Total != 3 {
		t.Fatalf("drop events = %d, want 3", filtered.Total)
	}

	since := now.Add(3 * time.Second)
	recent, err := store.Audit().ListAuditEvents(ctx, domain.AuditFilter{
		Since: &since, ActorUserID: &user.ID, Page: domain.Page{Size: 10}})
	if err != nil {
		t.Fatalf("filtering by time failed: %v", err)
	}
	if recent.Total != 2 {
		t.Fatalf("recent events = %d, want 2", recent.Total)
	}

	if _, err := store.Audit().AppendAuditEvent(ctx, domain.AuditEvent{
		Action: domain.ActionLogin, ObjectType: "session", Result: "bogus", OccurredAt: now}); !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("an invalid result must be rejected, got %v", err)
	}
}

func TestIdempotencyRecordsAreScopedAndExpirable(t *testing.T) {
	ctx := context.Background()
	store := newStore(t)
	user := mustUser(t, store, "student@campus.example", domain.RoleStudent)
	now := time.Now().UTC()

	record := repository.IdempotencyRecord{
		ActorUserID: user.ID, Method: "POST", Path: "/api/v1/enrollments", Key: "key-1",
		RequestFingerprint: "fp", ResponseStatus: 201, ResponseBody: `{"ok":true}`,
		CreatedAt: now, ExpiresAt: now.Add(time.Hour),
	}
	if err := store.Idempotency().SaveIdempotencyRecord(ctx, record); err != nil {
		t.Fatalf("saving failed: %v", err)
	}
	if err := store.Idempotency().SaveIdempotencyRecord(ctx, record); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("re-saving the same key must conflict, got %v", err)
	}

	stored, err := store.Idempotency().FindIdempotencyRecord(ctx, user.ID, "POST", "/api/v1/enrollments", "key-1")
	if err != nil {
		t.Fatalf("reading failed: %v", err)
	}
	if stored.ResponseStatus != 201 || stored.ResponseBody != `{"ok":true}` {
		t.Fatalf("stored record = %+v", stored)
	}

	// The same client value on another path is a different record.
	if _, err := store.Idempotency().FindIdempotencyRecord(ctx, user.ID, "POST", "/api/v1/enrollments/batch", "key-1"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("path scoping is broken, got %v", err)
	}
	if _, err := store.Idempotency().FindIdempotencyRecord(ctx, user.ID+1, "POST", "/api/v1/enrollments", "key-1"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("actor scoping is broken, got %v", err)
	}

	purged, err := store.Idempotency().PurgeIdempotencyRecords(ctx, now.Add(2*time.Hour))
	if err != nil {
		t.Fatalf("purging failed: %v", err)
	}
	if purged != 1 {
		t.Fatalf("purged = %d, want 1", purged)
	}
}

func TestEnrollmentStatePersistsAcrossRestart(t *testing.T) {
	ctx := context.Background()
	dsn := dsnFor(t)
	first := openStore(t, dsn)

	fixture := mustCatalog(t, first, 1, 2)
	holder := mustUser(t, first, "holder@campus.example", domain.RoleStudent)
	waiting := mustUser(t, first, "waiting@campus.example", domain.RoleStudent)
	now := time.Now().UTC()

	if _, err := first.Catalog().ClaimSeat(ctx, fixture.section.ID, fixture.section.Version, now); err != nil {
		t.Fatalf("claiming the seat failed: %v", err)
	}
	if _, err := first.Enrollments().CreateEnrollment(ctx, domain.Enrollment{
		StudentID: holder.ID, TermID: fixture.term.ID, SectionID: fixture.section.ID,
		CourseCode: fixture.course.Code, Credits: fixture.course.Credits,
		Status: domain.EnrollmentEnrolled, RequestedAt: now, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("creating the seat holder failed: %v", err)
	}
	rank, err := first.Enrollments().NextWaitlistRank(ctx, fixture.section.ID)
	if err != nil {
		t.Fatalf("reading the next rank failed: %v", err)
	}
	if rank != 1 {
		t.Fatalf("first waitlist rank = %d, want 1", rank)
	}
	if _, err := first.Enrollments().CreateEnrollment(ctx, domain.Enrollment{
		StudentID: waiting.ID, TermID: fixture.term.ID, SectionID: fixture.section.ID,
		CourseCode: fixture.course.Code, Credits: fixture.course.Credits,
		Status: domain.EnrollmentWaitlisted, WaitlistRank: rank,
		RequestedAt: now, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("creating the waitlist entry failed: %v", err)
	}
	if _, err := first.Catalog().AdjustWaitlistLength(ctx, fixture.section.ID, 1, now); err != nil {
		t.Fatalf("growing the waitlist failed: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("closing failed: %v", err)
	}

	second := openStore(t, dsn)
	defer func() { _ = second.Close() }()

	section, err := second.Catalog().FindSectionByID(ctx, fixture.section.ID)
	if err != nil {
		t.Fatalf("reading the section after the restart failed: %v", err)
	}
	if section.SeatsTaken != 1 || section.WaitlistLength != 1 {
		t.Fatalf("section after the restart = %+v", section)
	}
	if len(section.Meetings) != 2 {
		t.Fatalf("meetings after the restart = %d, want 2", len(section.Meetings))
	}
	head, err := second.Enrollments().HeadOfWaitlist(ctx, fixture.section.ID)
	if err != nil {
		t.Fatalf("reading the waitlist head after the restart failed: %v", err)
	}
	if head.StudentID != waiting.ID || head.WaitlistRank != 1 {
		t.Fatalf("waitlist head after the restart = %+v", head)
	}
	held, err := second.Enrollments().ActiveMeetingsForStudent(ctx, holder.ID, fixture.term.ID)
	if err != nil {
		t.Fatalf("reading held meetings failed: %v", err)
	}
	if len(held) != 2 {
		t.Fatalf("held meetings = %d, want 2", len(held))
	}
	roster, err := second.Enrollments().SectionRoster(ctx, fixture.section.ID, domain.Page{Size: 10})
	if err != nil {
		t.Fatalf("reading the roster failed: %v", err)
	}
	if roster.Total != 2 {
		t.Fatalf("roster total = %d, want 2", roster.Total)
	}
}

func TestCoursePrerequisitesAndLookups(t *testing.T) {
	ctx := context.Background()
	store := newStore(t)
	fixture := mustCatalog(t, store, 1, 0)

	advancedID, err := store.ProvisionCourse(ctx, domain.Course{
		Code: "CS310", Title: "Algorithms", Credits: 4, Department: "computing",
		Prerequisites: []string{"CS210", "MATH101"},
	})
	if err != nil {
		t.Fatalf("provisioning the advanced course failed: %v", err)
	}
	prereqs, err := store.Catalog().CoursePrerequisites(ctx, advancedID)
	if err != nil {
		t.Fatalf("reading prerequisites failed: %v", err)
	}
	if len(prereqs) != 2 || prereqs[0] != "CS210" || prereqs[1] != "MATH101" {
		t.Fatalf("prerequisites = %v, want catalogue order", prereqs)
	}

	byCode, err := store.Catalog().FindCourseByCode(ctx, " cs310 ")
	if err != nil {
		t.Fatalf("looking up by code failed: %v", err)
	}
	if byCode.ID != advancedID || len(byCode.Prerequisites) != 2 {
		t.Fatalf("course by code = %+v", byCode)
	}
	if _, err := store.Catalog().FindCourseByCode(ctx, "NOPE"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}

	byTermCode, err := store.Catalog().FindTermByCode(ctx, " 2026-AUTUMN ")
	if err != nil {
		t.Fatalf("looking up the term failed: %v", err)
	}
	if byTermCode.ID != fixture.term.ID {
		t.Fatalf("term by code = %+v", byTermCode)
	}
	terms, err := store.Catalog().ListTerms(ctx, true)
	if err != nil {
		t.Fatalf("listing terms failed: %v", err)
	}
	if len(terms) != 1 {
		t.Fatalf("terms = %d, want 1", len(terms))
	}
	if _, err := store.Catalog().FindSectionByID(ctx, 9999); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expected ErrNotFound for a missing section, got %v", err)
	}
}

func TestStudentAcademicRecordsRoundTrip(t *testing.T) {
	ctx := context.Background()
	store := newStore(t)
	student := mustUser(t, store, "student@campus.example", domain.RoleStudent)
	now := time.Now().UTC()

	if err := store.ProvisionAcademicRecord(ctx, domain.AcademicRecord{
		StudentID: student.ID, CourseCode: "CS110", Grade: "B", Credits: 4,
		CompletedAt: now.Add(-180 * 24 * time.Hour),
	}); err != nil {
		t.Fatalf("provisioning the record failed: %v", err)
	}
	records, err := store.Registrations().StudentAcademicRecords(ctx, student.ID)
	if err != nil {
		t.Fatalf("reading records failed: %v", err)
	}
	if len(records) != 1 || records[0].CourseCode != "CS110" || !records[0].Passing() {
		t.Fatalf("records = %+v", records)
	}
	if records[0].CompletedAt.IsZero() {
		t.Fatal("the completion timestamp must survive the round trip")
	}
}

func TestPingAndSchemaVersionServeReadiness(t *testing.T) {
	ctx := context.Background()
	store := newStore(t)
	if err := store.Ping(ctx); err != nil {
		t.Fatalf("ping failed: %v", err)
	}
	expected, err := migrations.LatestVersion()
	if err != nil {
		t.Fatalf("reading the expected version failed: %v", err)
	}
	version, err := store.SchemaVersion(ctx)
	if err != nil {
		t.Fatalf("reading the version failed: %v", err)
	}
	if version != expected {
		t.Fatalf("schema version = %d, want %d", version, expected)
	}
}

func TestOpenRejectsAnEmptyDSN(t *testing.T) {
	if _, err := sqlite.Open(context.Background(), "   ", sqlite.Options{}); err == nil {
		t.Fatal("an empty DSN must be rejected")
	}
}

func TestContextCancellationStopsQueries(t *testing.T) {
	store := newStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := store.Catalog().ListTerms(ctx, false); !errors.Is(err, context.Canceled) {
		t.Fatalf("a cancelled context must abort the query, got %v", err)
	}
}
