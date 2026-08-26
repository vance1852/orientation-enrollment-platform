package domain_test

import (
	"errors"
	"testing"
	"time"

	"github.com/vance1852/orientation-enrollment-platform/internal/domain"
)

func TestPageNormalizeAppliesDefaultsAndAllowList(t *testing.T) {
	allowed := map[string]string{"code": "s.code", "updated_at": "s.updated_at"}

	normalized, err := (domain.Page{}).Normalize(allowed, "code")
	if err != nil {
		t.Fatalf("normalising an empty page failed: %v", err)
	}
	if normalized.Number != 1 || normalized.Size != domain.DefaultPageSize {
		t.Fatalf("page = %+v, want page 1 with the default size", normalized)
	}
	if normalized.SortBy != "code" || normalized.Order != domain.SortAscending {
		t.Fatalf("sort = %s %s, want code asc", normalized.SortBy, normalized.Order)
	}

	descending, err := (domain.Page{Number: 3, Size: 10, SortBy: " UPDATED_AT ", Order: "DESC"}).Normalize(allowed, "code")
	if err != nil {
		t.Fatalf("normalising an explicit page failed: %v", err)
	}
	if descending.SortBy != "updated_at" || descending.Order != domain.SortDescending {
		t.Fatalf("sort = %s %s, want updated_at desc", descending.SortBy, descending.Order)
	}
	if descending.Offset() != 20 {
		t.Fatalf("Offset() = %d, want 20", descending.Offset())
	}

	if _, err := (domain.Page{Size: domain.MaxPageSize + 1}).Normalize(allowed, "code"); !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("an oversized page must be rejected, got %v", err)
	}
	if _, err := (domain.Page{SortBy: "password_hash"}).Normalize(allowed, "code"); !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("a sort key outside the allow list must be rejected, got %v", err)
	}
	if _, err := (domain.Page{Order: "sideways"}).Normalize(allowed, "code"); !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("an unknown order must be rejected, got %v", err)
	}
	if got := (domain.Page{Number: 1, Size: 10}).Offset(); got != 0 {
		t.Fatalf("the first page must start at offset 0, got %d", got)
	}
}

func TestNewPageResultComputesTotalPages(t *testing.T) {
	page := domain.Page{Number: 2, Size: 3}
	result := domain.NewPageResult([]int{4, 5, 6}, 7, page)
	if result.TotalPages != 3 {
		t.Fatalf("TotalPages = %d, want 3", result.TotalPages)
	}
	if result.Page != 2 || result.PageSize != 3 || result.Total != 7 {
		t.Fatalf("meta = %+v", result)
	}

	empty := domain.NewPageResult[int](nil, 0, page)
	if empty.Items == nil {
		t.Fatal("an empty result must still carry an allocated slice")
	}
	if empty.TotalPages != 0 {
		t.Fatalf("TotalPages = %d, want 0", empty.TotalPages)
	}
}

func TestAuditEventValidation(t *testing.T) {
	event := domain.AuditEvent{
		Action:     domain.ActionEnrollmentClaim,
		ObjectType: "enrollment",
		Result:     domain.ResultSuccess,
		OccurredAt: time.Now(),
	}
	if err := event.Validate(); err != nil {
		t.Fatalf("expected a valid audit event, got %v", err)
	}

	missingAction := event
	missingAction.Action = ""
	if err := missingAction.Validate(); !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("an event without an action must be rejected, got %v", err)
	}

	missingObject := event
	missingObject.ObjectType = " "
	if err := missingObject.Validate(); !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("an event without an object type must be rejected, got %v", err)
	}

	badResult := event
	badResult.Result = "maybe"
	if err := badResult.Validate(); !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("an unknown result must be rejected, got %v", err)
	}
}

func TestJobRetryScheduleIsBoundedAndExponential(t *testing.T) {
	base := time.Second
	max := 10 * time.Second
	want := []time.Duration{base, 2 * base, 4 * base, 8 * base, max, max}
	for attempt, expected := range want {
		if got := domain.Backoff(attempt+1, base, max); got != expected {
			t.Fatalf("Backoff(%d) = %s, want %s", attempt+1, got, expected)
		}
	}
	if got := domain.Backoff(0, base, max); got != base {
		t.Fatalf("Backoff(0) = %s, want the base delay", got)
	}
	if got := domain.Backoff(3, 0, 0); got <= 0 {
		t.Fatalf("Backoff must stay positive with zero inputs, got %s", got)
	}
	if got := domain.Backoff(40, base, max); got != max {
		t.Fatalf("Backoff must saturate at the ceiling, got %s", got)
	}
}

func TestJobAttemptabilityAndBudget(t *testing.T) {
	now := time.Date(2026, time.August, 26, 12, 0, 0, 0, time.UTC)
	job := domain.Job{State: domain.JobQueued, RunAfter: now, MaxAttempts: 3}
	if !job.Attemptable(now) {
		t.Fatal("a queued job due now must be attemptable")
	}
	job.RunAfter = now.Add(time.Second)
	if job.Attemptable(now) {
		t.Fatal("a job scheduled in the future must not be attemptable")
	}
	job.RunAfter = now
	job.State = domain.JobRunning
	if job.Attemptable(now) {
		t.Fatal("a running job must not be claimed again")
	}

	job = domain.Job{Attempts: 2, MaxAttempts: 3}
	if job.BudgetExhausted() {
		t.Fatal("two of three attempts must leave budget")
	}
	job.Attempts = 3
	if !job.BudgetExhausted() {
		t.Fatal("three of three attempts must exhaust the budget")
	}
	unlimited := domain.Job{Attempts: domain.MaxJobAttempts}
	if !unlimited.BudgetExhausted() {
		t.Fatal("a job without an explicit ceiling must fall back to the default")
	}

	if got := domain.StaleLockCutoff(now, time.Minute); !got.Equal(now.Add(-time.Minute)) {
		t.Fatalf("StaleLockCutoff = %s", got)
	}
	if got := domain.StaleLockCutoff(now, 0); !got.Equal(now.Add(-time.Minute)) {
		t.Fatalf("StaleLockCutoff with a zero lease = %s", got)
	}
}
