// Package httpx renders the single JSON envelope used by every endpoint and
// maps domain errors onto stable HTTP status codes.
package httpx

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/vance1852/orientation-enrollment-platform/internal/domain"
	"github.com/vance1852/orientation-enrollment-platform/internal/platform/logging"
)

// ErrorBody is the payload of every failed request.
type ErrorBody struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	RequestID string `json:"request_id"`
}

// ErrorEnvelope wraps ErrorBody so success and failure payloads never collide.
type ErrorEnvelope struct {
	Error ErrorBody `json:"error"`
}

// statusByCode maps the stable business codes onto HTTP semantics.
var statusByCode = map[string]int{
	"validation_failed":        http.StatusBadRequest,
	"unauthenticated":          http.StatusUnauthorized,
	"session_expired":          http.StatusUnauthorized,
	"session_revoked":          http.StatusUnauthorized,
	"forbidden":                http.StatusForbidden,
	"not_found":                http.StatusNotFound,
	"conflict":                 http.StatusConflict,
	"duplicate_enrollment":     http.StatusConflict,
	"capacity_exhausted":       http.StatusConflict,
	"waitlist_full":            http.StatusConflict,
	"schedule_conflict":        http.StatusConflict,
	"version_conflict":         http.StatusConflict,
	"invalid_transition":       http.StatusConflict,
	"idempotency_mismatch":     http.StatusConflict,
	"enrollment_window_closed": http.StatusUnprocessableEntity,
	"registration_incomplete":  http.StatusUnprocessableEntity,
	"prerequisite_missing":     http.StatusUnprocessableEntity,
	"credit_limit_exceeded":    http.StatusUnprocessableEntity,
	"migration_drift":          http.StatusServiceUnavailable,
	"job_permanently_failed":   http.StatusInternalServerError,
	"internal_error":           http.StatusInternalServerError,
	"request_timeout":          http.StatusGatewayTimeout,
	"client_closed_request":    499,
}

// StatusForCode resolves the HTTP status of a business code.
func StatusForCode(code string) int {
	if status, ok := statusByCode[code]; ok {
		return status
	}
	return http.StatusInternalServerError
}

// PublicMessage renders the client visible message of an error. Internal faults
// are collapsed into a fixed sentence so a driver or file path never leaks.
func PublicMessage(err error) string {
	code := domain.Code(err)
	if code == "internal_error" {
		return "The request could not be completed because of an internal error."
	}
	return err.Error()
}

// WriteJSON renders a successful payload.
func WriteJSON(w http.ResponseWriter, status int, payload any) error {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	if payload == nil {
		return nil
	}
	encoder := json.NewEncoder(w)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(payload); err != nil {
		return fmt.Errorf("encode response: %w", err)
	}
	return nil
}

// WriteRawJSON writes an already encoded body, which the idempotency replay path
// needs so the second response is byte identical to the first one.
func WriteRawJSON(w http.ResponseWriter, status int, body string) error {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	if body == "" {
		return nil
	}
	if _, err := w.Write([]byte(body)); err != nil {
		return fmt.Errorf("write replayed response: %w", err)
	}
	return nil
}

// WriteError renders the unified error envelope for an error chain.
func WriteError(w http.ResponseWriter, r *http.Request, err error) {
	code := classify(err)
	body := ErrorEnvelope{Error: ErrorBody{
		Code:      code,
		Message:   PublicMessage(err),
		RequestID: logging.RequestID(r.Context()),
	}}
	if code == "internal_error" {
		body.Error.Message = "The request could not be completed because of an internal error."
	}
	_ = WriteJSON(w, StatusForCode(code), body)
}

// WriteCodedError renders an envelope for a transport level condition that has
// no domain sentinel, such as a malformed request body.
func WriteCodedError(w http.ResponseWriter, r *http.Request, code, message string) {
	_ = WriteJSON(w, StatusForCode(code), ErrorEnvelope{Error: ErrorBody{
		Code:      code,
		Message:   message,
		RequestID: logging.RequestID(r.Context()),
	}})
}

func classify(err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "request_timeout"
	case errors.Is(err, context.Canceled):
		return "client_closed_request"
	default:
		return domain.Code(err)
	}
}
