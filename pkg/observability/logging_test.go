package observability_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/trace"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/observability"
)

func decode(t *testing.T, buf *bytes.Buffer) map[string]any {
	t.Helper()
	var line map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &line))
	return line
}

func TestLoggerStampsServiceCorrelationAndTrace(t *testing.T) {
	var buf bytes.Buffer
	logger := observability.NewLogger(&buf, "origination", slog.LevelInfo)

	traceID, _ := trace.TraceIDFromHex("0102030405060708090a0b0c0d0e0f10")
	spanID, _ := trace.SpanIDFromHex("0102030405060708")
	ctx := trace.ContextWithSpanContext(context.Background(), trace.NewSpanContext(trace.SpanContextConfig{
		TraceID: traceID, SpanID: spanID, TraceFlags: trace.FlagsSampled,
	}))
	ctx = observability.WithCorrelationID(ctx, "abc-123")

	logger.InfoContext(ctx, "quote created", "quote_id", "q1")

	line := decode(t, &buf)
	assert.Equal(t, "origination", line["service"])
	assert.Equal(t, "quote created", line["msg"])
	assert.Equal(t, "q1", line["quote_id"])
	assert.Equal(t, "abc-123", line["correlation_id"])
	assert.Equal(t, traceID.String(), line["trace_id"])
	assert.Equal(t, spanID.String(), line["span_id"])
}

func TestLoggerWithoutContextOmitsIdentifiers(t *testing.T) {
	var buf bytes.Buffer
	logger := observability.NewLogger(&buf, "origination", slog.LevelInfo).With("k", "v").WithGroup("g")

	logger.Info("plain")

	line := decode(t, &buf)
	assert.NotContains(t, line, "correlation_id")
	assert.NotContains(t, line, "trace_id")
	assert.Equal(t, "origination", line["service"])
}

func TestLoggerHonoursLevel(t *testing.T) {
	var buf bytes.Buffer
	logger := observability.NewLogger(&buf, "origination", slog.LevelWarn)

	logger.Info("dropped")
	assert.Empty(t, buf.String())
	logger.Warn("kept")
	assert.NotEmpty(t, buf.String())
}

func TestParseLevel(t *testing.T) {
	cases := map[string]slog.Level{
		"debug": slog.LevelDebug, "DEBUG": slog.LevelDebug, " warn ": slog.LevelWarn, "warning": slog.LevelWarn,
		"error": slog.LevelError, "info": slog.LevelInfo, "": slog.LevelInfo, "nonsense": slog.LevelInfo,
	}
	for in, want := range cases {
		assert.Equal(t, want, observability.ParseLevel(in), in)
	}
}

func TestCorrelationIDRoundTrip(t *testing.T) {
	assert.Empty(t, observability.CorrelationID(context.Background()))
	ctx := observability.WithCorrelationID(context.Background(), "id-1")
	assert.Equal(t, "id-1", observability.CorrelationID(ctx))
}
