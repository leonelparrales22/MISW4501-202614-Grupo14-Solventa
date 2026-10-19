package observability_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/observability"
)

// telemetry installs in-memory providers and a JSON logger as the globals.
type telemetry struct {
	spans   *tracetest.SpanRecorder
	metrics *sdkmetric.ManualReader
	logs    *bytes.Buffer
}

func install(t *testing.T) telemetry {
	t.Helper()
	spans := tracetest.NewSpanRecorder()
	reader := sdkmetric.NewManualReader()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spans)))
	otel.SetMeterProvider(sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)))
	otel.SetTextMapPropagator(propagation.TraceContext{})
	logs := &bytes.Buffer{}
	previous := slog.Default()
	slog.SetDefault(observability.NewLogger(logs, "svc", slog.LevelInfo))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return telemetry{spans: spans, metrics: reader, logs: logs}
}

func serve(h http.Handler, r *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec
}

func TestMiddlewareKeepsValidCorrelationIDAndLogsTheRequest(t *testing.T) {
	tel := install(t)
	var seen string
	h := observability.Middleware("svc", func(*http.Request) string { return "/v1/things/{id}" })(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			seen = observability.CorrelationID(r.Context())
			w.WriteHeader(http.StatusCreated)
		}))

	req := httptest.NewRequest(http.MethodPost, "/v1/things/42", nil)
	req.Header.Set(observability.CorrelationHeader, "corr-1")
	rec := serve(h, req)

	assert.Equal(t, "corr-1", seen)
	assert.Equal(t, "corr-1", rec.Header().Get(observability.CorrelationHeader))

	var line map[string]any
	require.NoError(t, json.Unmarshal(tel.logs.Bytes(), &line))
	assert.Equal(t, "http request", line["msg"])
	assert.Equal(t, "/v1/things/{id}", line["route"])
	assert.EqualValues(t, http.StatusCreated, line["status"])
	assert.Equal(t, "corr-1", line["correlation_id"])
	assert.NotEmpty(t, line["trace_id"])
}

func TestMiddlewareReplacesMissingOrHostileCorrelationID(t *testing.T) {
	install(t)
	h := observability.Middleware("svc", nil)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))

	for _, in := range []string{"", "has spaces and\ttabs", string(bytes.Repeat([]byte("a"), 65))} {
		req := httptest.NewRequest(http.MethodGet, "/x", nil)
		if in != "" {
			req.Header.Set(observability.CorrelationHeader, in)
		}
		got := serve(h, req).Header().Get(observability.CorrelationHeader)
		assert.Regexp(t, `^[0-9a-f]{32}$`, got)
	}
}

func TestMiddlewareOpensServerSpanThatContinuesTheCallerTrace(t *testing.T) {
	tel := install(t)
	h := observability.Middleware("svc", func(*http.Request) string { return "/v1/things" })(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusInternalServerError) }))

	req := httptest.NewRequest(http.MethodGet, "/v1/things", nil)
	req.Header.Set("traceparent", "00-0102030405060708090a0b0c0d0e0f10-0102030405060708-01")
	serve(h, req)

	ended := tel.spans.Ended()
	require.Len(t, ended, 1)
	span := ended[0]
	assert.Equal(t, "GET /v1/things", span.Name())
	assert.Equal(t, "0102030405060708090a0b0c0d0e0f10", span.SpanContext().TraceID().String())
	assert.Equal(t, "Error", span.Status().Code.String())
}

func TestMiddlewareRecordsRequestCountAndDuration(t *testing.T) {
	tel := install(t)
	h := observability.Middleware("svc", nil)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	serve(h, httptest.NewRequest(http.MethodGet, "/x", nil))
	serve(h, httptest.NewRequest(http.MethodGet, "/x", nil))

	var rm metricdata.ResourceMetrics
	require.NoError(t, tel.metrics.Collect(context.Background(), &rm))
	names := map[string]metricdata.Aggregation{}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			names[m.Name] = m.Data
		}
	}
	sum, ok := names["http.server.requests"].(metricdata.Sum[int64])
	require.True(t, ok)
	require.Len(t, sum.DataPoints, 1)
	assert.EqualValues(t, 2, sum.DataPoints[0].Value)
	assert.Contains(t, names, "http.server.duration")
}

func TestMiddlewareFallsBackToGoPatternThenUnmatched(t *testing.T) {
	tel := install(t)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/items/{id}", func(http.ResponseWriter, *http.Request) {})
	h := observability.Middleware("svc", nil)(mux)

	serve(h, httptest.NewRequest(http.MethodGet, "/v1/items/9", nil))
	serve(h, httptest.NewRequest(http.MethodGet, "/nothing", nil))

	ended := tel.spans.Ended()
	require.Len(t, ended, 2)
	assert.Equal(t, "GET GET /v1/items/{id}", ended[0].Name())
	assert.Equal(t, "GET unmatched", ended[1].Name())
}

func TestStatusRecorderFlushPassesThrough(t *testing.T) {
	install(t)
	h := observability.Middleware("svc", nil)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		f, ok := w.(http.Flusher)
		require.True(t, ok)
		f.Flush()
	}))
	rec := serve(h, httptest.NewRequest(http.MethodGet, "/x", nil))
	assert.True(t, rec.Flushed)
}
