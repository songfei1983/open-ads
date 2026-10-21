package observability

import (
	"context"
	"log/slog"
	"os"
	"strings"
)

type contextKey string

const (
	RequestIDKey contextKey = "request_id"
)

// WithRequestID returns a new context carrying the request ID.
func WithRequestID(ctx context.Context, requestID string) context.Context {
	return context.WithValue(ctx, RequestIDKey, requestID)
}

// RequestIDFromContext extracts request ID from context if present.
func RequestIDFromContext(ctx context.Context) string {
	if val, ok := ctx.Value(RequestIDKey).(string); ok {
		return val
	}
	return ""
}

// NewLogger initializes a JSON structured logger.
func NewLogger(serviceName, levelStr string) *slog.Logger {
	var level slog.Level
	switch strings.ToUpper(levelStr) {
	case "DEBUG":
		level = slog.LevelDebug
	case "WARN":
		level = slog.LevelWarn
	case "ERROR":
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}

	handler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: level,
	})

	logger := slog.New(handler).With(
		slog.String("service", serviceName),
	)

	return logger
}

// ContextLogger returns a logger annotated with request_id if present in ctx.
func ContextLogger(ctx context.Context, logger *slog.Logger) *slog.Logger {
	if reqID := RequestIDFromContext(ctx); reqID != "" {
		return logger.With(slog.String("request_id", reqID))
	}
	return logger
}
