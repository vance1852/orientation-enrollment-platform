package service_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/vance1852/orientation-enrollment-platform/internal/audit"
	"github.com/vance1852/orientation-enrollment-platform/internal/domain"
	"github.com/vance1852/orientation-enrollment-platform/internal/platform/logging"
	"github.com/vance1852/orientation-enrollment-platform/internal/service"
)

func TestClaimSeatUpdatesSectionEnrollmentAndAuditTogether(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	result, err := h.enrollments.Claim(ctx, h.studentPrincipal(),
		service.ClaimInput{StudentID: h.student.ID, SectionID: h.tightID})
	if err != nil {
		t.Fatalf("claiming the seat failed: %v", err)
	}
	if result.Waitlisted {
		t.Fatal("a section with a free seat must not waitlist")
	}
	if result.Enrollment.Status != domain.EnrollmentEnrolled || result.Enrollment.Credits != 4 {
		t.Fatalf("enrollment = %+v", result.Enrollment)
	}
	if result.Section.SeatsTaken != 1 {
		t.Fatalf("seats taken = %d, want 1", result.Section.SeatsTaken)
	}
	if h.auditCount(domain.ActionEnrollmentClaim, domain.ResultSuccess) != 1 {
		t.Fatal("a successful claim must be audited")
	}

	stored, err := h.enrollments.Get(ctx, h.studentPrincipal(), result.Enrollment.ID)
	if err != nil {
		t.Fatalf("reading the enrollment failed: %v", err)
	}
	if stored.SectionID != h.tightID || stored.CourseCode != "CS210" {
		t.Fatalf("stored enrollment = %+v", stored)
	}
}

func TestClaimSeatEnforcesEveryBusinessRule(t *testing.T) {
	t.Run("unverified paperwork", func(t *testing.T) {
		h := newHarness(t)
		newcomer := h.createUser("newcomer@campus.example", domain.RoleStudent)
		h.grantPrerequisite(newcomer.ID)
		actor := domain.Principal{UserID: newcomer.ID, Role: domain.RoleStudent}

		_, err := h.enrollments.Claim(context.Background(), actor,
			service.ClaimInput{StudentID: newcomer.ID, SectionID: h.openID})
		if !errors.Is(err, domain.ErrRegistrationIncomplete) {
			t.Fatalf("expected ErrRegistrationIncomplete, got %v", err)
		}
		if h.auditCount(domain.ActionEnrollmentClaim, domain.ResultRejected) != 1 {
			t.Fatal("a rejected claim must be audited")
		}
	})

	t.Run("missing prerequisite", func(t *testing.T) {
		h := newHarness(t)
		newcomer := h.createUser("newcomer@campus.example", domain.RoleStudent)
		h.verifyRegistration(newcomer.ID)
		actor := domain.Principal{UserID: newcomer.ID, Role: domain.RoleStudent}

		_, err := h.enrollments.Claim(context.Background(), actor,
			service.ClaimInput{StudentID: newcomer.ID, SectionID: h.tightID})
		if !errors.Is(err, domain.ErrPrerequisiteMissing) {
			t.Fatalf("expected ErrPrerequisiteMissing, got %v", err)
		}
	})

	t.Run("schedule conflict", func(t *testing.T) {
		h := newHarness(t)
		ctx := context.Background()
		if _, err := h.enrollments.Claim(ctx, h.studentPrincipal(),
			service.ClaimInput{StudentID: h.student.ID, SectionID: h.tightID}); err != nil {
			t.Fatalf("the first claim failed: %v", err)
		}
		_, err := h.enrollments.Claim(ctx, h.studentPrincipal(),
			service.ClaimInput{StudentID: h.student.ID, SectionID: h.clashID})
		if !errors.Is(err, domain.ErrScheduleConflict) {
			t.Fatalf("expected ErrScheduleConflict, got %v", err)
		}
		section := h.section(h.clashID)
		if section.SeatsTaken != 0 {
			t.Fatalf("a rejected claim must not consume a seat, got %d", section.SeatsTaken)
		}
	})

	t.Run("credit ceiling", func(t *testing.T) {
		h := newHarness(t)
		ctx := context.Background()
		if _, err := h.enrollments.Claim(ctx, h.studentPrincipal(),
			service.ClaimInput{StudentID: h.student.ID, SectionID: h.openID}); err != nil {
			t.Fatalf("the first claim failed: %v", err)
		}
		_, err := h.enrollments.Claim(ctx, h.studentPrincipal(),
			service.ClaimInput{StudentID: h.student.ID, SectionID: h.heavyID})
		if !errors.Is(err, domain.ErrCreditLimitExceeded) {
			t.Fatalf("expected ErrCreditLimitExceeded, got %v", err)
		}
	})

	t.Run("duplicate section", func(t *testing.T) {
		h := newHarness(t)
		ctx := context.Background()
		if _, err := h.enrollments.Claim(ctx, h.studentPrincipal(),
			service.ClaimInput{StudentID: h.student.ID, SectionID: h.openID}); err != nil {
			t.Fatalf("the first claim failed: %v", err)
		}
		_, err := h.enrollments.Claim(ctx, h.studentPrincipal(),
			service.ClaimInput{StudentID: h.student.ID, SectionID: h.openID})
		if !errors.Is(err, domain.ErrDuplicateEnrollment) {
			t.Fatalf("expected ErrDuplicateEnrollment, got %v", err)
		}
	})

	t.Run("closed enrollment window", func(t *testing.T) {
		h := newHarness(t)
		h.clock.Advance(48 * time.Hour)
		_, err := h.enrollments.Claim(context.Background(), h.studentPrincipal(),
			service.ClaimInput{StudentID: h.student.ID, SectionID: h.openID})
		if !errors.Is(err, domain.ErrEnrollmentWindowClosed) {
			t.Fatalf("expected ErrEnrollmentWindowClosed, got %v", err)
		}
	})

	t.Run("another student's seat", func(t *testing.T) {
		h := newHarness(t)
		_, err := h.enrollments.Claim(context.Background(), h.studentPrincipal(),
			service.ClaimInput{StudentID: h.otherStudnt.ID, SectionID: h.openID})
		if !errors.Is(err, domain.ErrForbidden) {
			t.Fatalf("expected ErrForbidden, got %v", err)
		}
	})

	t.Run("missing section", func(t *testing.T) {
		h := newHarness(t)
		_, err := h.enrollments.Claim(context.Background(), h.studentPrincipal(),
			service.ClaimInput{StudentID: h.student.ID, SectionID: 987654})
		if !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("expected ErrNotFound, got %v", err)
		}
	})

	t.Run("invalid identifiers", func(t *testing.T) {
		h := newHarness(t)
		_, err := h.enrollments.Claim(context.Background(), h.studentPrincipal(),
			service.ClaimInput{StudentID: h.student.ID, SectionID: -1})
		if !errors.Is(err, domain.ErrValidation) {
			t.Fatalf("expected a validation error, got %v", err)
		}
	})
}

func TestFullSectionWaitlistsAndPromotesInOrder(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	holder := h.enrollAnotherStudent("holder@campus.example", h.tightID)
	if holder.Status != domain.EnrollmentEnrolled {
		t.Fatalf("the first student must take the only seat, got %s", holder.Status)
	}

	first, err := h.enrollments.Claim(ctx, h.studentPrincipal(),
		service.ClaimInput{StudentID: h.student.ID, SectionID: h.tightID})
	if err != nil {
		t.Fatalf("waitlisting the second student failed: %v", err)
	}
	if !first.Waitlisted || first.Enrollment.WaitlistRank != 1 {
		t.Fatalf("first waitlist entry = %+v", first)
	}
	second, err := h.enrollments.Claim(ctx, h.principal(h.otherStudnt),
		service.ClaimInput{StudentID: h.otherStudnt.ID, SectionID: h.tightID})
	if err != nil {
		t.Fatalf("waitlisting the third student failed: %v", err)
	}
	if second.Enrollment.WaitlistRank != 2 {
		t.Fatalf("second waitlist rank = %d, want 2", second.Enrollment.WaitlistRank)
	}
	if section := h.section(h.tightID); section.WaitlistLength != 2 {
		t.Fatalf("waitlist length = %d, want 2", section.WaitlistLength)
	}

	// The waitlist limit is two, so a fourth student is turned away.
	fourth := h.createUser("fourth@campus.example", domain.RoleStudent)
	h.grantPrerequisite(fourth.ID)
	h.verifyRegistration(fourth.ID)
	if _, err := h.enrollments.Claim(ctx, h.principal(fourth),
		service.ClaimInput{StudentID: fourth.ID, SectionID: h.tightID}); !errors.Is(err, domain.ErrWaitlistFull) {
		t.Fatalf("expected ErrWaitlistFull, got %v", err)
	}

	// Releasing the seat must promote the head of the waitlist, not the newest
	// candidate, and must keep the seat counter at one.
	if _, err := h.enrollments.Drop(ctx, h.registrarPrincipal(), holder.ID, "left the programme"); err != nil {
		t.Fatalf("dropping the seat holder failed: %v", err)
	}
	promoted, err := h.enrollments.PromoteWaitlist(ctx, h.tightID)
	if err != nil {
		t.Fatalf("promotion failed: %v", err)
	}
	if !promoted {
		t.Fatal("a freed seat must promote the waitlist head")
	}

	head, err := h.enrollments.Get(ctx, h.studentPrincipal(), first.Enrollment.ID)
	if err != nil {
		t.Fatalf("reading the promoted record failed: %v", err)
	}
	if head.Status != domain.EnrollmentEnrolled || head.WaitlistRank != 0 {
		t.Fatalf("promoted record = %+v", head)
	}
	stillWaiting, err := h.enrollments.Get(ctx, h.principal(h.otherStudnt), second.Enrollment.ID)
	if err != nil {
		t.Fatalf("reading the remaining record failed: %v", err)
	}
	if stillWaiting.Status != domain.EnrollmentWaitlisted {
		t.Fatalf("the second candidate must keep waiting, got %s", stillWaiting.Status)
	}
	section := h.section(h.tightID)
	if section.SeatsTaken != 1 || section.WaitlistLength != 1 {
		t.Fatalf("section after the promotion = %+v", section)
	}
	if h.auditCount(domain.ActionEnrollmentPromote, domain.ResultSuccess) != 1 {
		t.Fatal("a promotion must be audited")
	}

	// A second promotion with no free seat is a no-op rather than an error.
	again, err := h.enrollments.PromoteWaitlist(ctx, h.tightID)
	if err != nil {
		t.Fatalf("the second promotion errored: %v", err)
	}
	if again {
		t.Fatal("a full section must not promote anybody")
	}
}

func TestDropReleasesSeatAndEnqueuesPromotion(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	seat, err := h.enrollments.Claim(ctx, h.studentPrincipal(),
		service.ClaimInput{StudentID: h.student.ID, SectionID: h.tightID})
	if err != nil {
		t.Fatalf("claiming failed: %v", err)
	}
	waiting, err := h.enrollments.Claim(ctx, h.principal(h.otherStudnt),
		service.ClaimInput{StudentID: h.otherStudnt.ID, SectionID: h.tightID})
	if err != nil {
		t.Fatalf("waitlisting failed: %v", err)
	}

	queuedBefore, err := h.store.Jobs().CountJobsByState(ctx, domain.JobQueued)
	if err != nil {
		t.Fatalf("counting jobs failed: %v", err)
	}
	dropped, err := h.enrollments.Drop(ctx, h.studentPrincipal(), seat.Enrollment.ID, "schedule changed")
	if err != nil {
		t.Fatalf("dropping failed: %v", err)
	}
	if dropped.Status != domain.EnrollmentDropped || dropped.ReleaseReason != "schedule changed" {
		t.Fatalf("dropped record = %+v", dropped)
	}
	if section := h.section(h.tightID); section.SeatsTaken != 0 {
		t.Fatalf("the seat must be released, got %d", section.SeatsTaken)
	}
	queuedAfter, err := h.store.Jobs().CountJobsByState(ctx, domain.JobQueued)
	if err != nil {
		t.Fatalf("counting jobs failed: %v", err)
	}
	if queuedAfter != queuedBefore+1 {
		t.Fatalf("queued jobs = %d, want %d", queuedAfter, queuedBefore+1)
	}

	// Withdrawing from a waitlist frees a waitlist slot but no seat, so it must
	// not enqueue a promotion job.
	withdrawn, err := h.enrollments.Drop(ctx, h.principal(h.otherStudnt), waiting.Enrollment.ID, "no longer needed")
	if err != nil {
		t.Fatalf("withdrawing failed: %v", err)
	}
	if withdrawn.Status != domain.EnrollmentWithdrawn {
		t.Fatalf("withdrawn record = %+v", withdrawn)
	}
	if section := h.section(h.tightID); section.WaitlistLength != 0 {
		t.Fatalf("waitlist length = %d, want 0", section.WaitlistLength)
	}
	finalQueued, err := h.store.Jobs().CountJobsByState(ctx, domain.JobQueued)
	if err != nil {
		t.Fatalf("counting jobs failed: %v", err)
	}
	if finalQueued != queuedAfter {
		t.Fatalf("a waitlist withdrawal must not enqueue a promotion, got %d", finalQueued)
	}
}

func TestDropRespectsWindowAndOwnership(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	seat, err := h.enrollments.Claim(ctx, h.studentPrincipal(),
		service.ClaimInput{StudentID: h.student.ID, SectionID: h.openID})
	if err != nil {
		t.Fatalf("claiming failed: %v", err)
	}
	if _, err := h.enrollments.Drop(ctx, h.principal(h.otherStudnt), seat.Enrollment.ID, ""); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("another student must not drop the seat, got %v", err)
	}
	if _, err := h.enrollments.Drop(ctx, h.studentPrincipal(), 0, ""); !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("a zero identifier must be rejected, got %v", err)
	}
	if _, err := h.enrollments.Drop(ctx, h.studentPrincipal(), 999999, ""); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("a missing record must report not found, got %v", err)
	}

	// After the add/drop window the student is locked out, while the registrar
	// can still correct the record.
	h.clock.Advance(72 * time.Hour)
	if _, err := h.enrollments.Drop(ctx, h.studentPrincipal(), seat.Enrollment.ID, "too late"); !errors.Is(err, domain.ErrEnrollmentWindowClosed) {
		t.Fatalf("expected ErrEnrollmentWindowClosed, got %v", err)
	}
	if _, err := h.enrollments.Drop(ctx, h.registrarPrincipal(), seat.Enrollment.ID, "administrative correction"); err != nil {
		t.Fatalf("a registrar drop after the window failed: %v", err)
	}
	if _, err := h.enrollments.Drop(ctx, h.registrarPrincipal(), seat.Enrollment.ID, "again"); !errors.Is(err, domain.ErrInvalidTransition) {
		t.Fatalf("dropping twice must be rejected, got %v", err)
	}
}

func TestConcurrentClaimsNeverOversellASection(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	const contenders = 8
	principals := make([]domain.Principal, 0, contenders)
	for i := 0; i < contenders; i++ {
		user := h.createUser(fmt.Sprintf("racer%d@campus.example", i), domain.RoleStudent)
		h.grantPrerequisite(user.ID)
		h.verifyRegistration(user.ID)
		principals = append(principals, h.principal(user))
	}

	var (
		start    sync.WaitGroup
		finished sync.WaitGroup
		mu       sync.Mutex
		enrolled int
		waitlist int
		rejected int
	)
	start.Add(1)
	for _, actor := range principals {
		finished.Add(1)
		go func(actor domain.Principal) {
			defer finished.Done()
			start.Wait()
			result, err := h.enrollments.Claim(ctx, actor,
				service.ClaimInput{StudentID: actor.UserID, SectionID: h.tightID})
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil && result.Waitlisted:
				waitlist++
			case err == nil:
				enrolled++
			default:
				rejected++
			}
		}(actor)
	}
	start.Done()
	finished.Wait()

	if enrolled != 1 {
		t.Fatalf("exactly one contender may take the single seat, got %d", enrolled)
	}
	if waitlist != 2 {
		t.Fatalf("the waitlist limit of two must be filled exactly, got %d", waitlist)
	}
	if rejected != contenders-3 {
		t.Fatalf("rejected = %d, want %d", rejected, contenders-3)
	}

	section := h.section(h.tightID)
	if section.SeatsTaken != 1 || section.SeatsTaken > section.Capacity {
		t.Fatalf("section oversold: %+v", section)
	}
	if section.WaitlistLength != 2 {
		t.Fatalf("waitlist length = %d, want 2", section.WaitlistLength)
	}

	roster, err := h.store.Enrollments().SectionRoster(ctx, h.tightID, domain.Page{Size: 50})
	if err != nil {
		t.Fatalf("reading the roster failed: %v", err)
	}
	if roster.Total != 3 {
		t.Fatalf("roster total = %d, want 3", roster.Total)
	}
	ranks := map[int]bool{}
	for _, entry := range roster.Items {
		if entry.Status == domain.EnrollmentWaitlisted {
			if ranks[entry.WaitlistRank] {
				t.Fatalf("waitlist rank %d was handed out twice", entry.WaitlistRank)
			}
			ranks[entry.WaitlistRank] = true
		}
	}
	if !ranks[1] || !ranks[2] {
		t.Fatalf("waitlist ranks = %v, want 1 and 2", ranks)
	}
}

func TestFailingAuditRollsBackTheSeatAllocation(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	sentinel := errors.New("audit storage unavailable")

	deps := service.Deps{
		Store:  &failingAuditStore{Store: h.store, err: sentinel},
		Clock:  h.clock,
		Audit:  audit.NewRecorder(func() time.Time { return h.clock.Now() }),
		Logger: logging.Discard(),
	}
	brittle, err := service.NewEnrollmentService(deps, 0)
	if err != nil {
		t.Fatalf("building the service failed: %v", err)
	}

	_, err = brittle.Claim(ctx, h.studentPrincipal(),
		service.ClaimInput{StudentID: h.student.ID, SectionID: h.tightID})
	if !errors.Is(err, sentinel) {
		t.Fatalf("expected the audit failure to surface, got %v", err)
	}

	section := h.section(h.tightID)
	if section.SeatsTaken != 0 {
		t.Fatalf("the seat must be rolled back with the audit entry, got %d", section.SeatsTaken)
	}
	held, err := h.store.Enrollments().ActiveEnrollmentsForStudent(ctx, h.student.ID, h.term.ID)
	if err != nil {
		t.Fatalf("reading enrollments failed: %v", err)
	}
	if len(held) != 0 {
		t.Fatalf("the enrollment must be rolled back, got %+v", held)
	}
}

func TestClaimHonoursContextCancellation(t *testing.T) {
	h := newHarness(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := h.enrollments.Claim(ctx, h.studentPrincipal(),
		service.ClaimInput{StudentID: h.student.ID, SectionID: h.openID})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	if section := h.section(h.openID); section.SeatsTaken != 0 {
		t.Fatalf("a cancelled claim must not consume a seat, got %d", section.SeatsTaken)
	}
}

func TestBatchClaimReportsPerItemOutcomes(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	results, err := h.enrollments.BatchClaim(ctx, h.studentPrincipal(), h.student.ID,
		[]int64{h.openID, h.tightID, h.clashID, 999999})
	if err != nil {
		t.Fatalf("the batch call itself must succeed, got %v", err)
	}
	if len(results) != 4 {
		t.Fatalf("results = %d, want 4", len(results))
	}
	bySection := make(map[int64]domain.BatchItemResult, len(results))
	for _, item := range results {
		bySection[item.SectionID] = item
	}
	if !bySection[h.openID].Succeeded || bySection[h.openID].Status != domain.EnrollmentEnrolled {
		t.Fatalf("CS110-A item = %+v", bySection[h.openID])
	}
	if !bySection[h.tightID].Succeeded {
		t.Fatalf("CS210-A item = %+v", bySection[h.tightID])
	}
	if bySection[h.clashID].Succeeded || bySection[h.clashID].Code != "schedule_conflict" {
		t.Fatalf("MATH101-A item = %+v", bySection[h.clashID])
	}
	if bySection[999999].Succeeded || bySection[999999].Code != "not_found" {
		t.Fatalf("missing section item = %+v", bySection[999999])
	}

	if _, err := h.enrollments.BatchClaim(ctx, h.studentPrincipal(), h.student.ID, nil); !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("an empty batch must be rejected, got %v", err)
	}
	oversized := make([]int64, 21)
	for i := range oversized {
		oversized[i] = h.openID
	}
	if _, err := h.enrollments.BatchClaim(ctx, h.studentPrincipal(), h.student.ID, oversized); !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("an oversized batch must be rejected, got %v", err)
	}
	if _, err := h.enrollments.BatchClaim(ctx, h.studentPrincipal(), h.otherStudnt.ID, []int64{h.openID}); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("a batch for another student must be rejected, got %v", err)
	}
}

func TestListEnrollmentsIsScopedForStudentsAndFiltered(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	if _, err := h.enrollments.Claim(ctx, h.studentPrincipal(),
		service.ClaimInput{StudentID: h.student.ID, SectionID: h.openID}); err != nil {
		t.Fatalf("claiming failed: %v", err)
	}
	if _, err := h.enrollments.Claim(ctx, h.principal(h.otherStudnt),
		service.ClaimInput{StudentID: h.otherStudnt.ID, SectionID: h.openID}); err != nil {
		t.Fatalf("claiming failed: %v", err)
	}

	// A student query is narrowed to the caller even when it asks for someone
	// else, so the list endpoint cannot leak another student's plan.
	mine, err := h.enrollments.List(ctx, h.studentPrincipal(), domain.EnrollmentFilter{
		StudentID: h.otherStudnt.ID, Page: domain.Page{Size: 10}})
	if err != nil {
		t.Fatalf("listing failed: %v", err)
	}
	if mine.Total != 1 {
		t.Fatalf("a student must only see their own record, got %d", mine.Total)
	}
	if mine.Items[0].StudentID != h.student.ID {
		t.Fatalf("returned record belongs to %d", mine.Items[0].StudentID)
	}

	all, err := h.enrollments.List(ctx, h.registrarPrincipal(), domain.EnrollmentFilter{
		SectionID: h.openID, Page: domain.Page{Size: 10}})
	if err != nil {
		t.Fatalf("listing failed: %v", err)
	}
	if all.Total != 2 {
		t.Fatalf("a registrar must see both records, got %d", all.Total)
	}

	enrolled, err := h.enrollments.List(ctx, h.registrarPrincipal(), domain.EnrollmentFilter{
		TermID: h.term.ID, Statuses: []domain.EnrollmentStatus{domain.EnrollmentDropped},
		Page: domain.Page{Size: 10}})
	if err != nil {
		t.Fatalf("listing failed: %v", err)
	}
	if enrolled.Total != 0 {
		t.Fatalf("no record was dropped yet, got %d", enrolled.Total)
	}

	if _, err := h.enrollments.Get(ctx, h.principal(h.otherStudnt), mine.Items[0].ID); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("reading another student's record must be forbidden, got %v", err)
	}
}

func TestIdempotentClaimReplaysTheStoredOutcome(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	calls := 0

	scope := service.Scope{
		ActorUserID: h.student.ID,
		Method:      "POST",
		Path:        "/api/v1/enrollments",
		Key:         "orientation-key-1",
		Payload:     `{"section_id":1}`,
	}
	run := func() (service.Outcome, error) {
		return h.idempotency.Execute(ctx, scope, func(ctx context.Context) (int, string, error) {
			calls++
			return 201, `{"enrollment":{"id":7}}`, nil
		})
	}

	first, err := run()
	if err != nil {
		t.Fatalf("the first call failed: %v", err)
	}
	if first.Replayed || first.Status != 201 {
		t.Fatalf("first outcome = %+v", first)
	}
	second, err := run()
	if err != nil {
		t.Fatalf("the replay failed: %v", err)
	}
	if !second.Replayed || second.Body != first.Body {
		t.Fatalf("second outcome = %+v", second)
	}
	if calls != 1 {
		t.Fatalf("the operation ran %d times, want 1", calls)
	}

	// The same key with another payload must not return the stored answer.
	mismatched := scope
	mismatched.Payload = `{"section_id":2}`
	if _, err := h.idempotency.Execute(ctx, mismatched, func(context.Context) (int, string, error) {
		return 201, "{}", nil
	}); !errors.Is(err, domain.ErrIdempotencyMismatch) {
		t.Fatalf("expected ErrIdempotencyMismatch, got %v", err)
	}

	// Another endpoint with the same key is a separate record.
	otherPath := scope
	otherPath.Path = "/api/v1/enrollments/batch"
	if outcome, err := h.idempotency.Execute(ctx, otherPath, func(context.Context) (int, string, error) {
		return 200, `{"items":[]}`, nil
	}); err != nil || outcome.Replayed {
		t.Fatalf("path scoping is broken: %+v %v", outcome, err)
	}

	// Without a key the operation always runs.
	keyless := scope
	keyless.Key = ""
	before := calls
	if _, err := h.idempotency.Execute(ctx, keyless, func(context.Context) (int, string, error) {
		calls++
		return 201, "{}", nil
	}); err != nil {
		t.Fatalf("a keyless call failed: %v", err)
	}
	if calls != before+1 {
		t.Fatal("a keyless call must execute the operation")
	}

	h.clock.Advance(service.IdempotencyTTL + time.Hour)
	if _, err := run(); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("an expired key must be rejected, got %v", err)
	}
	purged, err := h.idempotency.PurgeExpired(ctx)
	if err != nil {
		t.Fatalf("purging failed: %v", err)
	}
	if purged < 1 {
		t.Fatalf("purged = %d, want at least one", purged)
	}
}

func TestIdempotentCallRequiresAnActor(t *testing.T) {
	h := newHarness(t)
	_, err := h.idempotency.Execute(context.Background(), service.Scope{
		Method: "POST", Path: "/api/v1/enrollments", Key: "k",
	}, func(context.Context) (int, string, error) { return 201, "{}", nil })
	if !errors.Is(err, domain.ErrUnauthenticated) {
		t.Fatalf("expected ErrUnauthenticated, got %v", err)
	}
}

func TestFailedOperationIsNotStoredForReplay(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	scope := service.Scope{ActorUserID: h.student.ID, Method: "POST",
		Path: "/api/v1/enrollments", Key: "failing-key", Payload: "{}"}
	sentinel := errors.New("downstream failure")

	if _, err := h.idempotency.Execute(ctx, scope, func(context.Context) (int, string, error) {
		return 0, "", sentinel
	}); !errors.Is(err, sentinel) {
		t.Fatalf("expected the sentinel, got %v", err)
	}
	outcome, err := h.idempotency.Execute(ctx, scope, func(context.Context) (int, string, error) {
		return 201, `{"retried":true}`, nil
	})
	if err != nil {
		t.Fatalf("the retry failed: %v", err)
	}
	if outcome.Replayed {
		t.Fatal("a failed attempt must not be replayable")
	}
}
