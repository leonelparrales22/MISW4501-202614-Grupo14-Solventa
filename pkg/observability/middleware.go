package observability

import (
	"log/slog"
	"net/http"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

// RouteResolver returns the route pattern that served the request
// ("/v1/assisted-quotes/{id}"), never the concrete path, so spans and metrics
// keep a bounded cardinality. It is called after the handler ran.
type RouteResolver func(*http.Request) string

// Middleware wraps a handler so every request keeps its correlation id (taken
// from X-Correlation-Id or generated, and echoed in the response), runs inside
// a server span that continues the caller's trace, is counted and timed, and
// leaves one access-log line. route names the matched route; nil falls back to
// the Go 1.22 pattern, then to "unmatched".
func Middleware(service string, route RouteResolver) func(http.Handler) http.Handler {
	tracer := otel.Tracer(service)
	meter := otel.Meter(service)
	requests, _ := meter.Int64Counter("http.server.requests",
		metric.WithDescription("HTTP requests served"), metric.WithUnit("{request}"))
	duration, _ := meter.Float64Histogram("http.server.duration",
		metric.WithDescription("HTTP request duration"), metric.WithUnit("ms"))

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			id := r.Header.Get(CorrelationHeader)
			if !validCorrelationID.MatchString(id) {
				id = newCorrelationID()
			}
			w.Header().Set(CorrelationHeader, id)

			ctx := otel.GetTextMapPropagator().Extract(r.Context(), propagation.HeaderCarrier(r.Header))
			ctx = WithCorrelationID(ctx, id)
			ctx, span := tracer.Start(ctx, r.Method, trace.WithSpanKind(trace.SpanKindServer))
			defer span.End()

			rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
			req := r.WithContext(ctx)
			next.ServeHTTP(rec, req)

			pattern := routeOf(req, route)
			elapsed := time.Since(start)
			attrs := []attribute.KeyValue{
				attribute.String("http.request.method", r.Method),
				attribute.String("http.route", pattern),
				attribute.Int("http.response.status_code", rec.status),
			}
			span.SetName(r.Method + " " + pattern)
			span.SetAttributes(attrs...)
			span.SetAttributes(attribute.String("correlation_id", id))
			if rec.status >= http.StatusInternalServerError {
				span.SetStatus(codes.Error, http.StatusText(rec.status))
			}
			requests.Add(ctx, 1, metric.WithAttributes(attrs...))
			duration.Record(ctx, float64(elapsed.Microseconds())/1000, metric.WithAttributes(attrs...))

			level := slog.LevelInfo
			if rec.status >= http.StatusInternalServerError {
				level = slog.LevelError
			}
			slog.Log(ctx, level, "http request",
				"method", r.Method, "route", pattern, "status", rec.status, "duration_ms", elapsed.Milliseconds())
		})
	}
}

func routeOf(r *http.Request, route RouteResolver) string {
	if route != nil {
		if p := route(r); p != "" {
			return p
		}
	}
	if r.Pattern != "" {
		return r.Pattern
	}
	return "unmatched"
}

// statusRecorder remembers the status code the handler wrote.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

// Flush keeps streaming handlers working through the wrapper.
func (s *statusRecorder) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
