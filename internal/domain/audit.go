package domain

import (
	"strings"
	"time"
)

// AuditAction names an operation worth keeping in the permanent trail.
type AuditAction string

// Audited operations.
const (
	ActionLogin              AuditAction = "auth.login"
	ActionLogout             AuditAction = "auth.logout"
	ActionSessionSwept       AuditAction = "auth.session_swept"
	ActionRegistrationSubmit AuditAction = "registration.submit"
	ActionRegistrationDecide AuditAction = "registration.decide"
	ActionEnrollmentClaim    AuditAction = "enrollment.claim"
	ActionEnrollmentWaitlist AuditAction = "enrollment.waitlist"
	ActionEnrollmentDrop     AuditAction = "enrollment.drop"
	ActionEnrollmentPromote  AuditAction = "enrollment.promote"
	ActionJobFailed          AuditAction = "job.permanently_failed"
)

// AuditResult reports the outcome recorded next to an action.
type AuditResult string

// Audit outcomes.
const (
	ResultSuccess  AuditResult = "success"
	ResultRejected AuditResult = "rejected"
	ResultFailure  AuditResult = "failure"
)

// AuditEvent binds an actor, an object, an action, a result and the request that
// triggered it. Every mutating business path writes one.
type AuditEvent struct {
	ID          int64
	ActorUserID *int64
	ActorRole   string
	Action      AuditAction
	ObjectType  string
	ObjectID    string
	Result      AuditResult
	RequestID   string
	Detail      string
	OccurredAt  time.Time
}

// Validate keeps malformed audit rows out of the trail.
func (e AuditEvent) Validate() error {
	if strings.TrimSpace(string(e.Action)) == "" {
		return NewFieldError("audit.action", "must not be empty")
	}
	if strings.TrimSpace(e.ObjectType) == "" {
		return NewFieldError("audit.object_type", "must not be empty")
	}
	switch e.Result {
	case ResultSuccess, ResultRejected, ResultFailure:
	default:
		return NewFieldError("audit.result", "must be success, rejected or failure")
	}
	return nil
}

// AuditFilter narrows an audit query. Empty fields are ignored.
type AuditFilter struct {
	ActorUserID *int64
	Action      string
	ObjectType  string
	ObjectID    string
	Since       *time.Time
	Page        Page
}
