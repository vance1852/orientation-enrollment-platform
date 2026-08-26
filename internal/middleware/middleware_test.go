package middleware_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/vance1852/orientation-enrollment-platform/internal/middleware"
	"github.com/vance1852/orientation-enrollment-platform/internal/platform/logging"
)

func TestChainAppliesTheOutermostMiddlewareFirst(t *testing.T) {
	var order []string
	tag := func(name string) middleware.Middleware {
		return func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				order = append(order, name)
				next.ServeHTTP(w, r)
			})
		}
	}
	handler := middleware.Chain(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		order = append(order, "handler")
	}), tag("first"), tag("second"))

	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	if strings.Join(order, ",") != "first,second,handler" {
		t.Fatalf("order = %v", order)
	}
}

func TestRequestIDReachesTheHandlerContext(t *testing.T) {
	var seen string
	handler := middleware.RequestID()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = logging.RequestID(r.Context())
		w.WriteHeader(http.StatusNoContent)
	}))

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/", nil))
	if seen == "" {
		t.Fatal("the handler must observe a request id")
	}
	if recorder.Header().Get(middleware.RequestIDHeader) != seen {
		t.Fatal("the response header must carry the same request id")
	}
}

func TestRecoverTurnsAPanicIntoTheErrorEnvelope(t *testing.T) {
	var buffer bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buffer, nil))
	handler := middleware.Chain(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("orientation service exploded")
	}), middleware.RequestID(), middleware.Recover(logger))

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/sections", nil))

	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d", recorder.Code)
	}
	var envelope struct {
		Error struct {
			Code      string `json:"code"`
			Message   string `json:"message"`
			RequestID string `json:"request_id"`
		} `json:"error"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decoding the envelope failed: %v (body %s)", err, recorder.Body.String())
	}
	if envelope.Error.Code != "internal_error" || envelope.Error.RequestID == "" {
		t.Fatalf("envelope = %+v", envelope.Error)
	}
	if strings.Contains(envelope.Error.Message, "exploded") {
		t.Fatal("the panic value must not leak to the client")
	}
	if !strings.Contains(buffer.String(), "exploded") {
		t.Fatal("the panic value must be logged")
	}
}

func TestAccessLogRecordsTheOutcome(t *testing.T) {
	var buffer bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buffer, nil))
	handler := middleware.Chain(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
		_, _ = w.Write([]byte(`{"ok":false}`))
	}), middleware.RequestID(), middleware.AccessLog(logger))

	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/api/v1/enrollments", nil))

	record := buffer.String()
	for _, fragment := range []string{`"status":418`, `"method":"POST"`, `"path":"/api/v1/enrollments"`, `"request_id"`} {
		if !strings.Contains(record, fragment) {
			t.Fatalf("the access log is missing %s: %s", fragment, record)
		}
	}
}

func TestTimeoutCancelsTheHandlerContext(t *testing.T) {
	handler := middleware.Timeout(20 * time.Millisecond)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
			w.WriteHeader(http.StatusGatewayTimeout)
		case <-time.After(2 * time.Second):
			w.WriteHeader(http.StatusOK)
		}
	}))

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/", nil))
	if recorder.Code != http.StatusGatewayTimeout {
		t.Fatalf("status = %d, want the handler to observe the deadline", recorder.Code)
	}
}

func TestZeroTimeoutLeavesTheHandlerUntouched(t *testing.T) {
	var deadlineSet bool
	handler := middleware.Timeout(0)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, deadlineSet = r.Context().Deadline()
		w.WriteHeader(http.StatusOK)
	}))
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	if deadlineSet {
		t.Fatal("a zero budget must not install a deadline")
	}
}

func TestLoggingContextHelpers(t *testing.T) {
	if logging.RequestID(context.Background()) != "" {
		t.Fatal("an empty context must not carry a request id")
	}
	//nolint:staticcheck // deliberately passing a nil context to prove the guard
	if logging.RequestID(nil) != "" {
		t.Fatal("a nil context must be tolerated")
	}
	ctx := logging.WithRequestID(context.Background(), "")
	if logging.RequestID(ctx) != "" {
		t.Fatal("an empty request id must not be stored")
	}
	ctx = logging.WithRequestID(context.Background(), "req_abc")
	if logging.RequestID(ctx) != "req_abc" {
		t.Fatal("the request id must round trip")
	}

	var buffer bytes.Buffer
	logger := logging.New(&buffer, "debug")
	logging.FromContext(ctx, logger).Debug("annotated")
	if !strings.Contains(buffer.String(), `"request_id":"req_abc"`) {
		t.Fatalf("the logger must annotate records: %s", buffer.String())
	}

	buffer.Reset()
	logging.New(&buffer, "error").Info("suppressed")
	if buffer.Len() != 0 {
		t.Fatalf("the level filter is not applied: %s", buffer.String())
	}
}
