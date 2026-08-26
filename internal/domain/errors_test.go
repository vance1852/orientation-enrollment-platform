package domain_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/vance1852/orientation-enrollment-platform/internal/domain"
)

func TestCodeMapsEveryPublicFailureMode(t *testing.T) {
	cases := map[error]string{
		nil:                                "",
		domain.ErrSessionExpired:           "session_expired",
		domain.ErrSessionRevoked:           "session_revoked",
		domain.ErrUnauthenticated:          "unauthenticated",
		domain.ErrForbidden:                "forbidden",
		domain.ErrNotFound:                 "not_found",
		domain.ErrEnrollmentWindowClosed:   "enrollment_window_closed",
		domain.ErrRegistrationIncomplete:   "registration_incomplete",
		domain.ErrPrerequisiteMissing:      "prerequisite_missing",
		domain.ErrCreditLimitExceeded:      "credit_limit_exceeded",
		domain.ErrScheduleConflict:         "schedule_conflict",
		domain.ErrCapacityExhausted:        "capacity_exhausted",
		domain.ErrWaitlistFull:             "waitlist_full",
		domain.ErrDuplicateEnrollment:      "duplicate_enrollment",
		domain.ErrIdempotencyMismatch:      "idempotency_mismatch",
		domain.ErrVersionConflict:          "version_conflict",
		domain.ErrInvalidTransition:        "invalid_transition",
		domain.ErrMigrationDrift:           "migration_drift",
		domain.ErrJobPermanentlyFailed:     "job_permanently_failed",
		domain.ErrValidation:               "validation_failed",
		domain.ErrConflict:                 "conflict",
		errors.New("something unexpected"): "internal_error",
	}
	for err, want := range cases {
		if got := domain.Code(err); got != want {
			t.Fatalf("Code(%v) = %q, want %q", err, got, want)
		}
	}
}

func TestCodeSurvivesWrapping(t *testing.T) {
	wrapped := fmt.Errorf("claim seat in section 42: %w",
		fmt.Errorf("section CS210-A is full: %w", domain.ErrCapacityExhausted))
	if got := domain.Code(wrapped); got != "capacity_exhausted" {
		t.Fatalf("Code() = %q through two wraps", got)
	}
	if !errors.Is(wrapped, domain.ErrCapacityExhausted) {
		t.Fatal("the sentinel must stay reachable through the chain")
	}
}

func TestFieldErrorCarriesTheFieldName(t *testing.T) {
	err := domain.NewFieldError("page_size", "must not exceed 100")
	var fieldErr *domain.FieldError
	if !errors.As(err, &fieldErr) {
		t.Fatalf("expected a FieldError, got %v", err)
	}
	if fieldErr.Field != "page_size" {
		t.Fatalf("field = %q", fieldErr.Field)
	}
	if !errors.Is(err, domain.ErrValidation) {
		t.Fatal("a field error must satisfy errors.Is(ErrValidation)")
	}
	if got := err.Error(); got == "" {
		t.Fatal("a field error must render a message")
	}
}

func TestConflictErrorRendersWithAndWithoutACollider(t *testing.T) {
	withCollider := domain.NewConflictError(domain.ErrScheduleConflict, "monday 08:00-09:00", "monday 08:30-09:30")
	if got := withCollider.Error(); got != "monday 08:00-09:00 collides with monday 08:30-09:30: section meeting times overlap" {
		t.Fatalf("message = %q", got)
	}
	withoutCollider := domain.NewConflictError(domain.ErrConflict, "section CS210-A", "")
	if got := withoutCollider.Error(); got != "section CS210-A: resource conflict" {
		t.Fatalf("message = %q", got)
	}
	if !errors.Is(withoutCollider, domain.ErrConflict) {
		t.Fatal("the conflict sentinel must stay reachable")
	}
}
