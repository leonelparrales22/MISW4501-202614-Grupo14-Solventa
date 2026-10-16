package observability_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/observability"
)

func TestConfigFromEnv(t *testing.T) {
	env := map[string]string{
		observability.EnvLogLevel: "debug", observability.EnvEnvironment: "staging", observability.EnvOTLPEndpoint: "http://otel:4318",
	}
	cfg := observability.ConfigFromEnv(func(k string) string { return env[k] }, "origination", "1.2.3")
	assert.Equal(t, observability.Config{
		Service: "origination", Version: "1.2.3", Environment: "staging",
		LogLevel: slog.LevelDebug, OTLPEndpoint: "http://otel:4318",
	}, cfg)

	defaults := observability.ConfigFromEnv(func(string) string { return "" }, "origination", "dev")
	assert.Equal(t, "local", defaults.Environment)
	assert.Equal(t, slog.LevelInfo, defaults.LogLevel)
	assert.Empty(t, defaults.OTLPEndpoint)
}

func TestSetupRequiresAServiceName(t *testing.T) {
	_, err := observability.Setup(context.Background(), observability.Config{})
	assert.Error(t, err)
}

func TestSetupInProcessWritesJSONLogsAndShutsDown(t *testing.T) {
	previous := slog.Default()
	t.Cleanup(func() { slog.SetDefault(previous) })
	var buf bytes.Buffer

	shutdown, err := observability.Setup(context.Background(), observability.Config{
		Service: "origination", Version: "dev", Environment: "test", LogLevel: slog.LevelInfo, LogWriter: &buf,
	})
	require.NoError(t, err)

	_, span := observability.Tracer("test").Start(context.Background(), "op")
	assert.True(t, span.SpanContext().IsValid(), "spans exist even when nothing is exported")
	span.End()
	_, err = observability.Meter("test").Int64Counter("c")
	require.NoError(t, err)

	slog.Info("hello")
	lines := bytes.Split(bytes.TrimSpace(buf.Bytes()), []byte("\n"))
	var last map[string]any
	require.NoError(t, json.Unmarshal(lines[len(lines)-1], &last))
	assert.Equal(t, "hello", last["msg"])
	assert.Equal(t, "origination", last["service"])

	assert.NotNil(t, otel.GetTracerProvider())
	require.NoError(t, shutdown(context.Background()))
}

func TestSetupExportsOverOTLPWhenAnEndpointIsSet(t *testing.T) {
	previous := slog.Default()
	t.Cleanup(func() { slog.SetDefault(previous) })
	var hits atomic.Int32
	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer collector.Close()

	shutdown, err := observability.Setup(context.Background(), observability.Config{
		Service: "origination", OTLPEndpoint: collector.URL, LogWriter: &bytes.Buffer{},
	})
	require.NoError(t, err)

	_, span := observability.Tracer("test").Start(context.Background(), "op")
	span.End()
	require.NoError(t, shutdown(context.Background())) // flushes the batch

	assert.Positive(t, hits.Load(), "the span batch reached the collector")
}
