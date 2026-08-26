package httpx_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vance1852/orientation-enrollment-platform/internal/domain"
	"github.com/vance1852/orientation-enrollment-platform/internal/httpx"
	"github.com/vance1852/orientation-enrollment-platform/internal/platform/logging"
)

func TestStatusForCodeCoversTheBusinessVocabulary(t *testing.T) {
	cases := map[string]int{
		"validation_failed":        http.StatusBadRequest,
		"unauthenticated":          http.StatusUnauthorized,
		"session_expired":          http.StatusUnauthorized,
		"forbidden":                http.StatusForbidden,
		"not_found":                http.StatusNotFound,
		"capacity_exhausted":       http.StatusConflict,
		"schedule_conflict":        http.StatusConflict,
		"duplicate_enrollment":     http.StatusConflict,
		"idempotency_mismatch":     http.StatusConflict,
		"credit_limit_exceeded":    http.StatusUnprocessableEntity,
		"registration_incomplete":  http.StatusUnprocessableEntity,
		"enrollment_window_closed": http.StatusUnprocessableEntity,
		"migration_drift":          http.StatusServiceUnavailable,
		"internal_error":           http.StatusInternalServerError,
		"request_timeout":          http.StatusGatewayTimeout,
		"totally_unknown":          http.StatusInternalServerError,
	}
	for code, want := range cases {
		if got := httpx.StatusForCode(code); got != want {
			t.Fatalf("StatusForCode(%q) = %d, want %d", code, got, want)
		}
	}
}

func TestWriteErrorRendersTheEnvelopeWithTheRequestID(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/api/v1/enrollments", nil)
	request = request.WithContext(logging.WithRequestID(request.Context(), "req_test"))
	recorder := httptest.NewRecorder()

	err := fmt.Errorf("section CS210-A is full: %w", domain.ErrCapacityExhausted)
	httpx.WriteError(recorder, request, err)

	if recorder.Code != http.StatusConflict {
		t.Fatalf("status = %d", recorder.Code)
	}
	if got := recorder.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
		t.Fatalf("content type = %q", got)
	}
	if recorder.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal("the sniffing guard header is missing")
	}
	var envelope httpx.ErrorEnvelope
	if decodeErr := json.Unmarshal(recorder.Body.Bytes(), &envelope); decodeErr != nil {
		t.Fatalf("decoding failed: %v", decodeErr)
	}
	if envelope.Error.Code != "capacity_exhausted" || envelope.Error.RequestID != "req_test" {
		t.Fatalf("envelope = %+v", envelope.Error)
	}
	if !strings.Contains(envelope.Error.Message, "CS210-A") {
		t.Fatalf("a business message must reach the client, got %q", envelope.Error.Message)
	}
}

func TestWriteErrorHidesInternalDetail(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/api/v1/sections", nil)
	recorder := httptest.NewRecorder()

	httpx.WriteError(recorder, request, fmt.Errorf("dial tcp 10.0.0.5:5432: connection refused"))

	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d", recorder.Code)
	}
	body := recorder.Body.String()
	if strings.Contains(body, "10.0.0.5") || strings.Contains(body, "connection refused") {
		t.Fatalf("internal detail leaked: %s", body)
	}
	if !strings.Contains(body, "internal_error") {
		t.Fatalf("body = %s", body)
	}
}

func TestWriteErrorClassifiesContextFailures(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/api/v1/sections", nil)

	timeout := httptest.NewRecorder()
	httpx.WriteError(timeout, request, fmt.Errorf("query budget: %w", context.DeadlineExceeded))
	if timeout.Code != http.StatusGatewayTimeout {
		t.Fatalf("timeout status = %d", timeout.Code)
	}

	cancelled := httptest.NewRecorder()
	httpx.WriteError(cancelled, request, fmt.Errorf("client gone: %w", context.Canceled))
	if cancelled.Code != 499 {
		t.Fatalf("cancellation status = %d", cancelled.Code)
	}
}

func TestWriteJSONAndRawJSON(t *testing.T) {
	recorder := httptest.NewRecorder()
	if err := httpx.WriteJSON(recorder, http.StatusOK, map[string]string{"code": "CS210-A"}); err != nil {
		t.Fatalf("writing failed: %v", err)
	}
	if !strings.Contains(recorder.Body.String(), `"CS210-A"`) {
		t.Fatalf("body = %s", recorder.Body.String())
	}

	empty := httptest.NewRecorder()
	if err := httpx.WriteJSON(empty, http.StatusNoContent, nil); err != nil {
		t.Fatalf("writing an empty payload failed: %v", err)
	}
	if empty.Body.Len() != 0 || empty.Code != http.StatusNoContent {
		t.Fatalf("empty response = %d %q", empty.Code, empty.Body.String())
	}

	raw := httptest.NewRecorder()
	if err := httpx.WriteRawJSON(raw, http.StatusCreated, `{"replayed":true}`); err != nil {
		t.Fatalf("writing raw json failed: %v", err)
	}
	if raw.Body.String() != `{"replayed":true}` {
		t.Fatalf("raw body = %q", raw.Body.String())
	}
}

func TestPublicMessageKeepsBusinessErrorsReadable(t *testing.T) {
	business := fmt.Errorf("term 2026-autumn is closed: %w", domain.ErrEnrollmentWindowClosed)
	if got := httpx.PublicMessage(business); !strings.Contains(got, "2026-autumn") {
		t.Fatalf("message = %q", got)
	}
	if got := httpx.PublicMessage(fmt.Errorf("driver panic")); strings.Contains(got, "driver") {
		t.Fatalf("internal message leaked: %q", got)
	}
}

func TestWriteCodedErrorForTransportFailures(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	request = request.WithContext(logging.WithRequestID(request.Context(), "req_ready"))
	recorder := httptest.NewRecorder()

	httpx.WriteCodedError(recorder, request, "migration_drift", "The database is not reachable.")

	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d", recorder.Code)
	}
	var envelope httpx.ErrorEnvelope
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decoding failed: %v", err)
	}
	if envelope.Error.RequestID != "req_ready" || envelope.Error.Message == "" {
		t.Fatalf("envelope = %+v", envelope.Error)
	}
}
