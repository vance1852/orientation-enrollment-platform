// Package logging builds the structured logger and carries the request id
// through the context so every layer can annotate its records.
package logging

import (
	"context"
	"io"
	"log/slog"
	"strings"
)

type contextKey struct{}

var requestIDKey contextKey

// New builds a JSON structured logger at the requested level.
func New(w io.Writer, level string) *slog.Logger {
	var lvl slog.Level
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "debug":
		lvl = slog.LevelDebug
	case "warn", "warning":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	handler := slog.NewJSONHandler(w, &slog.HandlerOptions{Level: lvl})
	return slog.New(handler)
}

// WithRequestID stores the request identifier in the context.
func WithRequestID(ctx context.Context, requestID string) context.Context {
	if requestID == "" {
		return ctx
	}
	return context.WithValue(ctx, requestIDKey, requestID)
}

// RequestID reads the request identifier from the context, returning an empty
// string for background work that has no inbound request.
func RequestID(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if value, ok := ctx.Value(requestIDKey).(string); ok {
		return value
	}
	return ""
}

// FromContext returns a logger already annotated with the request identifier.
func FromContext(ctx context.Context, base *slog.Logger) *slog.Logger {
	if base == nil {
		base = slog.Default()
	}
	if id := RequestID(ctx); id != "" {
		return base.With(slog.String("request_id", id))
	}
	return base
}

// Discard returns a logger that drops every record, used by tests that assert
// behaviour rather than log output.
func Discard() *slog.Logger {
	return slog.New(slog.NewJSONHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError + 1}))
}
