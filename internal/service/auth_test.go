package service_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/vance1852/orientation-enrollment-platform/internal/domain"
	"github.com/vance1852/orientation-enrollment-platform/internal/service"
)

func TestLoginIssuesARevocableSessionAndAudit(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	result, err := h.auth.Login(ctx, " Student@Campus.Example ", testPassword, "go-test")
	if err != nil {
		t.Fatalf("login failed: %v", err)
	}
	if result.Token == "" {
		t.Fatal("login must return an opaque token")
	}
	if !result.ExpiresAt.Equal(h.clock.Now().Add(2 * time.Hour)) {
		t.Fatalf("expires_at = %s", result.ExpiresAt)
	}
	if result.Principal.Role != domain.RoleStudent || result.Principal.SessionID == 0 {
		t.Fatalf("principal = %+v", result.Principal)
	}
	if h.auditCount(domain.ActionLogin, domain.ResultSuccess) != 1 {
		t.Fatal("a successful login must be audited")
	}

	principal, err := h.auth.Authenticate(ctx, result.Token)
	if err != nil {
		t.Fatalf("authenticating the fresh token failed: %v", err)
	}
	if principal.UserID != h.student.ID || principal.SessionID != result.Principal.SessionID {
		t.Fatalf("principal = %+v", principal)
	}

	count, err := h.auth.ActiveSessionCount(ctx, principal)
	if err != nil {
		t.Fatalf("counting sessions failed: %v", err)
	}
	if count != 1 {
		t.Fatalf("active sessions = %d, want 1", count)
	}

	if err := h.auth.Logout(ctx, principal); err != nil {
		t.Fatalf("logout failed: %v", err)
	}
	if _, err := h.auth.Authenticate(ctx, result.Token); !errors.Is(err, domain.ErrSessionRevoked) {
		t.Fatalf("a logged out token must report ErrSessionRevoked, got %v", err)
	}
	if h.auditCount(domain.ActionLogout, domain.ResultSuccess) != 1 {
		t.Fatal("logout must be audited")
	}
}

func TestLoginRejectionsDoNotRevealAccountExistence(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	_, unknownErr := h.auth.Login(ctx, "nobody@campus.example", testPassword, "go-test")
	_, wrongErr := h.auth.Login(ctx, "student@campus.example", "wrong-password", "go-test")
	if !errors.Is(unknownErr, domain.ErrUnauthenticated) {
		t.Fatalf("an unknown address must report ErrUnauthenticated, got %v", unknownErr)
	}
	if !errors.Is(wrongErr, domain.ErrUnauthenticated) {
		t.Fatalf("a wrong password must report ErrUnauthenticated, got %v", wrongErr)
	}
	if domain.Code(unknownErr) != domain.Code(wrongErr) {
		t.Fatal("both rejections must map onto the same public code")
	}

	if _, err := h.auth.Login(ctx, "not-an-email", testPassword, ""); !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("a malformed address must be a validation error, got %v", err)
	}
	if _, err := h.auth.Login(ctx, "student@campus.example", "", ""); !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("an empty password must be a validation error, got %v", err)
	}
}

func TestAuthenticateRejectsExpiredSessions(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	result, err := h.auth.Login(ctx, "student@campus.example", testPassword, "go-test")
	if err != nil {
		t.Fatalf("login failed: %v", err)
	}
	h.clock.Advance(2 * time.Hour)

	if _, err := h.auth.Authenticate(ctx, result.Token); !errors.Is(err, domain.ErrSessionExpired) {
		t.Fatalf("expected ErrSessionExpired, got %v", err)
	}
	if _, err := h.auth.Authenticate(ctx, ""); !errors.Is(err, domain.ErrUnauthenticated) {
		t.Fatalf("an empty token must be unauthenticated, got %v", err)
	}
	if _, err := h.auth.Authenticate(ctx, "not-a-real-token"); !errors.Is(err, domain.ErrUnauthenticated) {
		t.Fatalf("an unknown token must be unauthenticated, got %v", err)
	}
}

func TestSweepExpiredSessionsRevokesAndAudits(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	first, err := h.auth.Login(ctx, "student@campus.example", testPassword, "go-test")
	if err != nil {
		t.Fatalf("login failed: %v", err)
	}
	h.clock.Advance(3 * time.Hour)
	second, err := h.auth.Login(ctx, "other@campus.example", testPassword, "go-test")
	if err != nil {
		t.Fatalf("second login failed: %v", err)
	}

	revoked, err := h.auth.SweepExpiredSessions(ctx, 10)
	if err != nil {
		t.Fatalf("sweeping failed: %v", err)
	}
	if revoked != 1 {
		t.Fatalf("revoked = %d, want 1", revoked)
	}
	if _, err := h.auth.Authenticate(ctx, first.Token); !errors.Is(err, domain.ErrSessionRevoked) {
		t.Fatalf("the swept session must be revoked, got %v", err)
	}
	if _, err := h.auth.Authenticate(ctx, second.Token); err != nil {
		t.Fatalf("the live session must survive the sweep, got %v", err)
	}
	if h.auditCount(domain.ActionSessionSwept, domain.ResultSuccess) != 1 {
		t.Fatal("a sweep that revoked rows must be audited")
	}

	again, err := h.auth.SweepExpiredSessions(ctx, 10)
	if err != nil {
		t.Fatalf("the second sweep failed: %v", err)
	}
	if again != 0 {
		t.Fatalf("the second sweep revoked %d rows, want 0", again)
	}
	if h.auditCount(domain.ActionSessionSwept, domain.ResultSuccess) != 1 {
		t.Fatal("an empty sweep must not add an audit entry")
	}
}

func TestLogoutWithoutASessionIsRejected(t *testing.T) {
	h := newHarness(t)
	err := h.auth.Logout(context.Background(), domain.Principal{UserID: h.student.ID, Role: domain.RoleStudent})
	if !errors.Is(err, domain.ErrUnauthenticated) {
		t.Fatalf("expected ErrUnauthenticated, got %v", err)
	}
}

func TestNewAuthServiceValidatesItsInputs(t *testing.T) {
	h := newHarness(t)
	if _, err := service.NewAuthService(h.deps, time.Second); !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("a tiny session lifetime must be rejected, got %v", err)
	}
	if _, err := service.NewAuthService(service.Deps{Clock: h.clock, Audit: h.deps.Audit}, time.Hour); !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("a missing store must be rejected, got %v", err)
	}
	if _, err := service.NewEnrollmentService(h.deps, -1); !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("a negative retry budget must be rejected, got %v", err)
	}
}

func TestPrincipalTravelsThroughTheContext(t *testing.T) {
	h := newHarness(t)
	if _, ok := service.PrincipalFrom(context.Background()); ok {
		t.Fatal("an empty context must not carry a principal")
	}
	ctx := service.WithPrincipal(context.Background(), h.studentPrincipal())
	principal, ok := service.PrincipalFrom(ctx)
	if !ok || principal.UserID != h.student.ID {
		t.Fatalf("principal = %+v, ok = %v", principal, ok)
	}
}
