package observability

import (
	"context"
	"io"
	"log/slog"
	"strings"

	"go.opentelemetry.io/otel/trace"
)

// NewLogger returns a JSON logger that stamps every record with the service
// name and, when the context passed to the *Context methods has them, the
// correlation id and the trace and span ids.
func NewLogger(w io.Writer, service string, level slog.Level) *slog.Logger {
	h := &contextHandler{Handler: slog.NewJSONHandler(w, &slog.HandlerOptions{Level: level})}
	return slog.New(h).With("service", service)
}

// ParseLevel maps LOG_LEVEL values to slog levels; anything unknown is info.
func ParseLevel(s string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// contextHandler adds the request-scoped identifiers to each record.
type contextHandler struct {
	slog.Handler
}

func (h *contextHandler) Handle(ctx context.Context, r slog.Record) error {
	if id := CorrelationID(ctx); id != "" {
		r.AddAttrs(slog.String("correlation_id", id))
	}
	if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
		r.AddAttrs(slog.String("trace_id", sc.TraceID().String()), slog.String("span_id", sc.SpanID().String()))
	}
	return h.Handler.Handle(ctx, r)
}

func (h *contextHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &contextHandler{Handler: h.Handler.WithAttrs(attrs)}
}

func (h *contextHandler) WithGroup(name string) slog.Handler {
	return &contextHandler{Handler: h.Handler.WithGroup(name)}
}
