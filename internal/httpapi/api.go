// Package httpapi exposes the orientation platform over HTTP. Handlers parse
// input, delegate to the service layer and render the shared JSON envelope; no
// business rule and no SQL lives here.
package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"strings"

	"github.com/vance1852/orientation-enrollment-platform/internal/domain"
	"github.com/vance1852/orientation-enrollment-platform/internal/httpx"
	"github.com/vance1852/orientation-enrollment-platform/internal/platform/logging"
	"github.com/vance1852/orientation-enrollment-platform/internal/service"
)

// Readiness reports whether the dependencies of the process are usable.
type Readiness interface {
	Ping(ctx context.Context) error
	SchemaVersion(ctx context.Context) (int, error)
}

// Services groups the use cases the HTTP layer delegates to.
type Services struct {
	Auth          *service.AuthService
	Catalog       *service.CatalogService
	Registrations *service.RegistrationService
	Enrollments   *service.EnrollmentService
	Idempotency   *service.IdempotencyService
}

// API holds the handler dependencies.
type API struct {
	services       Services
	readiness      Readiness
	logger         *slog.Logger
	expectedSchema int
}

// New builds the HTTP API.
func New(services Services, readiness Readiness, logger *slog.Logger, expectedSchema int) (*API, error) {
	switch {
	case services.Auth == nil:
		return nil, domain.NewFieldError("services.auth", "must not be nil")
	case services.Catalog == nil:
		return nil, domain.NewFieldError("services.catalog", "must not be nil")
	case services.Registrations == nil:
		return nil, domain.NewFieldError("services.registrations", "must not be nil")
	case services.Enrollments == nil:
		return nil, domain.NewFieldError("services.enrollments", "must not be nil")
	case services.Idempotency == nil:
		return nil, domain.NewFieldError("services.idempotency", "must not be nil")
	case readiness == nil:
		return nil, domain.NewFieldError("readiness", "must not be nil")
	}
	if logger == nil {
		logger = slog.Default()
	}
	if expectedSchema <= 0 {
		return nil, domain.NewFieldError("expected_schema", "must be positive")
	}
	return &API{services: services, readiness: readiness, logger: logger, expectedSchema: expectedSchema}, nil
}

// Routes registers every endpoint on a fresh mux.
func (a *API) Routes() *http.ServeMux {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", a.handleLiveness)
	mux.HandleFunc("GET /readyz", a.handleReadiness)

	mux.HandleFunc("POST /api/v1/auth/login", a.handleLogin)
	mux.HandleFunc("POST /api/v1/auth/logout", a.authenticated(a.handleLogout))
	mux.HandleFunc("GET /api/v1/auth/me", a.authenticated(a.handleProfile))

	mux.HandleFunc("GET /api/v1/terms", a.authenticated(a.handleListTerms))
	mux.HandleFunc("GET /api/v1/sections", a.authenticated(a.handleListSections))
	mux.HandleFunc("GET /api/v1/sections/{sectionID}", a.authenticated(a.handleGetSection))
	mux.HandleFunc("GET /api/v1/sections/{sectionID}/roster", a.authenticated(a.handleSectionRoster))

	mux.HandleFunc("POST /api/v1/registrations", a.authenticated(a.handleSubmitRegistration))
	mux.HandleFunc("GET /api/v1/registrations", a.authenticated(a.handleListRegistrations))
	mux.HandleFunc("GET /api/v1/registrations/mine", a.authenticated(a.handleMyRegistration))
	mux.HandleFunc("POST /api/v1/registrations/{registrationID}/decision", a.authenticated(a.handleDecideRegistration))

	mux.HandleFunc("POST /api/v1/enrollments", a.authenticated(a.handleClaimEnrollment))
	mux.HandleFunc("POST /api/v1/enrollments/batch", a.authenticated(a.handleBatchClaim))
	mux.HandleFunc("GET /api/v1/enrollments", a.authenticated(a.handleListEnrollments))
	mux.HandleFunc("GET /api/v1/enrollments/{enrollmentID}", a.authenticated(a.handleGetEnrollment))
	mux.HandleFunc("DELETE /api/v1/enrollments/{enrollmentID}", a.authenticated(a.handleDropEnrollment))

	mux.HandleFunc("GET /api/v1/audit-events", a.authenticated(a.handleListAuditEvents))

	return mux
}

// authenticatedHandler is a handler that already received a verified principal.
type authenticatedHandler func(http.ResponseWriter, *http.Request, domain.Principal)

// authenticated resolves the bearer token before the handler runs and maps every
// authentication failure onto the unified error envelope.
func (a *API) authenticated(next authenticatedHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token, err := bearerToken(r)
		if err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		principal, err := a.services.Auth.Authenticate(r.Context(), token)
		if err != nil {
			logging.FromContext(r.Context(), a.logger).Debug("authentication rejected",
				slog.String("code", domain.Code(err)))
			httpx.WriteError(w, r, err)
			return
		}
		ctx := service.WithPrincipal(r.Context(), principal)
		next(w, r.WithContext(ctx), principal)
	}
}

func bearerToken(r *http.Request) (string, error) {
	header := strings.TrimSpace(r.Header.Get("Authorization"))
	if header == "" {
		return "", domain.ErrUnauthenticated
	}
	parts := strings.SplitN(header, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return "", domain.ErrUnauthenticated
	}
	token := strings.TrimSpace(parts[1])
	if token == "" {
		return "", domain.ErrUnauthenticated
	}
	return token, nil
}
