// Package domain holds the orientation and enrollment business model.
//
// The package is intentionally free of HTTP, SQL and framework concerns so the
// invariants below can be exercised in isolation.
package domain

import (
	"errors"
	"fmt"
)

// Sentinel errors describe every failure mode the platform exposes to callers.
// Transport layers map them to stable error codes; business layers wrap them
// with %w so the chain survives the trip from repository to handler.
var (
	ErrValidation             = errors.New("validation failed")
	ErrNotFound               = errors.New("resource not found")
	ErrConflict               = errors.New("resource conflict")
	ErrUnauthenticated        = errors.New("authentication required")
	ErrSessionExpired         = errors.New("session expired")
	ErrSessionRevoked         = errors.New("session revoked")
	ErrForbidden              = errors.New("operation not permitted for role")
	ErrInvalidTransition      = errors.New("invalid state transition")
	ErrEnrollmentWindowClosed = errors.New("enrollment window closed")
	ErrRegistrationIncomplete = errors.New("student registration is not verified")
	ErrPrerequisiteMissing    = errors.New("prerequisite requirement not satisfied")
	ErrCreditLimitExceeded    = errors.New("term credit limit exceeded")
	ErrScheduleConflict       = errors.New("section meeting times overlap")
	ErrCapacityExhausted      = errors.New("section capacity exhausted")
	ErrWaitlistFull           = errors.New("section waitlist is full")
	ErrDuplicateEnrollment    = errors.New("student already holds a seat in this course")
	ErrVersionConflict        = errors.New("optimistic version conflict")
	ErrIdempotencyMismatch    = errors.New("idempotency key reused with a different payload")
	ErrJobPermanentlyFailed   = errors.New("job exhausted its retry budget")
	ErrMigrationDrift         = errors.New("applied migration checksum does not match repository")
)

// FieldError reports a single invalid input field. It wraps ErrValidation so
// callers can branch on the class of failure without string matching.
type FieldError struct {
	Field  string
	Reason string
}

func (e *FieldError) Error() string {
	return fmt.Sprintf("field %q is invalid: %s", e.Field, e.Reason)
}

// Unwrap keeps ErrValidation reachable through errors.Is.
func (e *FieldError) Unwrap() error { return ErrValidation }

// NewFieldError builds a validation failure for a named input field.
func NewFieldError(field, reason string) error {
	return &FieldError{Field: field, Reason: reason}
}

// TransitionError reports a rejected state machine move and keeps both states
// available for auditing and for HTTP diagnostics.
type TransitionError struct {
	Entity string
	From   string
	To     string
}

func (e *TransitionError) Error() string {
	return fmt.Sprintf("%s cannot move from %s to %s", e.Entity, e.From, e.To)
}

// Unwrap keeps ErrInvalidTransition reachable through errors.Is.
func (e *TransitionError) Unwrap() error { return ErrInvalidTransition }

// NewTransitionError builds a rejected transition error.
func NewTransitionError(entity, from, to string) error {
	return &TransitionError{Entity: entity, From: from, To: to}
}

// ConflictError describes a business conflict that names the colliding object,
// for example the already scheduled section that blocks a new enrollment.
type ConflictError struct {
	Kind      error
	Subject   string
	Colliding string
}

func (e *ConflictError) Error() string {
	if e.Colliding == "" {
		return fmt.Sprintf("%s: %s", e.Subject, e.Kind)
	}
	return fmt.Sprintf("%s collides with %s: %s", e.Subject, e.Colliding, e.Kind)
}

// Unwrap exposes the underlying sentinel so errors.Is keeps working.
func (e *ConflictError) Unwrap() error { return e.Kind }

// NewConflictError builds a conflict that carries the colliding subject.
func NewConflictError(kind error, subject, colliding string) error {
	return &ConflictError{Kind: kind, Subject: subject, Colliding: colliding}
}

// Code maps an error chain onto the stable business error code carried by API
// responses, audit details and batch item results. The order of the checks is
// significant: the most specific sentinel wins.
func Code(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, ErrSessionExpired):
		return "session_expired"
	case errors.Is(err, ErrSessionRevoked):
		return "session_revoked"
	case errors.Is(err, ErrUnauthenticated):
		return "unauthenticated"
	case errors.Is(err, ErrForbidden):
		return "forbidden"
	case errors.Is(err, ErrNotFound):
		return "not_found"
	case errors.Is(err, ErrEnrollmentWindowClosed):
		return "enrollment_window_closed"
	case errors.Is(err, ErrRegistrationIncomplete):
		return "registration_incomplete"
	case errors.Is(err, ErrPrerequisiteMissing):
		return "prerequisite_missing"
	case errors.Is(err, ErrCreditLimitExceeded):
		return "credit_limit_exceeded"
	case errors.Is(err, ErrScheduleConflict):
		return "schedule_conflict"
	case errors.Is(err, ErrCapacityExhausted):
		return "capacity_exhausted"
	case errors.Is(err, ErrWaitlistFull):
		return "waitlist_full"
	case errors.Is(err, ErrDuplicateEnrollment):
		return "duplicate_enrollment"
	case errors.Is(err, ErrIdempotencyMismatch):
		return "idempotency_mismatch"
	case errors.Is(err, ErrVersionConflict):
		return "version_conflict"
	case errors.Is(err, ErrInvalidTransition):
		return "invalid_transition"
	case errors.Is(err, ErrMigrationDrift):
		return "migration_drift"
	case errors.Is(err, ErrJobPermanentlyFailed):
		return "job_permanently_failed"
	case errors.Is(err, ErrValidation):
		return "validation_failed"
	case errors.Is(err, ErrConflict):
		return "conflict"
	default:
		return "internal_error"
	}
}
