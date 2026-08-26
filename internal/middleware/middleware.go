// Package middleware carries transport concerns that apply to every route:
// request identity, structured access logs, panic recovery and request budgets.
package middleware

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/vance1852/orientation-enrollment-platform/internal/httpx"
	"github.com/vance1852/orientation-enrollment-platform/internal/platform/ids"
	"github.com/vance1852/orientation-enrollment-platform/internal/platform/logging"
)

// RequestIDHeader is the header used to accept and echo a request identifier.
const RequestIDHeader = "X-Request-Id"

// Middleware decorates an http.Handler.
type Middleware func(http.Handler) http.Handler

// Chain applies middlewares so the first argument is the outermost layer.
func Chain(handler http.Handler, middlewares ...Middleware) http.Handler {
	for i := len(middlewares) - 1; i >= 0; i-- {
		handler = middlewares[i](handler)
	}
	return handler
}

// RequestID attaches an identifier to the context and echoes it back. A client
// supplied value is reused so a trace can span several services.
func RequestID() Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			requestID := r.Header.Get(RequestIDHeader)
			if !validRequestID(requestID) {
				generated, err := ids.NewRequestID()
				if err != nil {
					httpx.WriteCodedError(w, r, "internal_error", "Could not allocate a request identifier.")
					return
				}
				requestID = generated
			}
			ctx := logging.WithRequestID(r.Context(), requestID)
			w.Header().Set(RequestIDHeader, requestID)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// statusRecorder captures the status code for the access log.
type statusRecorder struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (s *statusRecorder) WriteHeader(status int) {
	if s.status == 0 {
		s.status = status
	}
	s.ResponseWriter.WriteHeader(status)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	n, err := s.ResponseWriter.Write(b)
	s.bytes += n
	return n, err
}

// AccessLog emits one structured record per request. It never logs the body, so
// credentials submitted to the login endpoint stay out of the log stream.
func AccessLog(logger *slog.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			started := time.Now()
			recorder := &statusRecorder{ResponseWriter: w}
			next.ServeHTTP(recorder, r)
			if recorder.status == 0 {
				recorder.status = http.StatusOK
			}
			logging.FromContext(r.Context(), logger).Info("http request",
				slog.String("method", r.Method),
				slog.String("path", r.URL.Path),
				slog.Int("status", recorder.status),
				slog.Int("bytes", recorder.bytes),
				slog.String("duration", time.Since(started).String()),
			)
		})
	}
}

// Recover converts a panic into the unified error envelope and keeps the process
// alive. The stack stays in the log and never reaches the client.
func Recover(logger *slog.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if recovered := recover(); recovered != nil {
					logging.FromContext(r.Context(), logger).Error("panic recovered",
						slog.Any("panic", recovered),
						slog.String("path", r.URL.Path),
					)
					httpx.WriteCodedError(w, r, "internal_error",
						"The request could not be completed because of an internal error.")
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// Timeout bounds how long a handler may run and cancels the request context, so
// every downstream service and repository call observes the deadline.
func Timeout(budget time.Duration) Middleware {
	return func(next http.Handler) http.Handler {
		if budget <= 0 {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx, cancel := context.WithTimeout(r.Context(), budget)
			defer cancel()
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func validRequestID(value string) bool {
	if len(value) < 4 || len(value) > 64 {
		return false
	}
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '-', r == '_':
		default:
			return false
		}
	}
	return true
}
