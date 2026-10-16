package observability

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

// Environment variables read by ConfigFromEnv.
const (
	EnvLogLevel     = "LOG_LEVEL"
	EnvEnvironment  = "ENVIRONMENT"
	EnvOTLPEndpoint = "OTEL_EXPORTER_OTLP_ENDPOINT"

	metricInterval = 15 * time.Second
	shutdownBudget = 5 * time.Second
)

// Config describes the service that is being observed.
type Config struct {
	Service     string
	Version     string
	Environment string
	LogLevel    slog.Level
	// OTLPEndpoint is the collector base URL (http://host:4318). Empty keeps
	// the telemetry in process: nothing is exported.
	OTLPEndpoint string
	// LogWriter receives the JSON logs; os.Stdout when nil.
	LogWriter io.Writer
}

// ConfigFromEnv builds the Config of a service from LOG_LEVEL, ENVIRONMENT
// and OTEL_EXPORTER_OTLP_ENDPOINT.
func ConfigFromEnv(getenv func(string) string, service, version string) Config {
	env := getenv(EnvEnvironment)
	if env == "" {
		env = "local"
	}
	return Config{
		Service:      service,
		Version:      version,
		Environment:  env,
		LogLevel:     ParseLevel(getenv(EnvLogLevel)),
		OTLPEndpoint: getenv(EnvOTLPEndpoint),
	}
}

// Setup installs the JSON logger as the slog default and the OpenTelemetry
// trace and meter providers (with W3C trace-context propagation) as the
// globals. The returned function flushes and stops the exporters; call it on
// shutdown.
func Setup(ctx context.Context, cfg Config) (shutdown func(context.Context) error, err error) {
	if cfg.Service == "" {
		return nil, errors.New("observability: the service name is required")
	}
	w := cfg.LogWriter
	if w == nil {
		w = os.Stdout
	}
	slog.SetDefault(NewLogger(w, cfg.Service, cfg.LogLevel))

	res, err := resource.Merge(resource.Default(), resource.NewSchemaless(
		attribute.String("service.name", cfg.Service),
		attribute.String("service.version", cfg.Version),
		attribute.String("deployment.environment", cfg.Environment),
	))
	if err != nil {
		return nil, err
	}

	traceOpts := []sdktrace.TracerProviderOption{sdktrace.WithResource(res)}
	metricOpts := []sdkmetric.Option{sdkmetric.WithResource(res)}
	if cfg.OTLPEndpoint != "" {
		spanExporter, err := otlptracehttp.New(ctx, otlptracehttp.WithEndpointURL(cfg.OTLPEndpoint))
		if err != nil {
			return nil, err
		}
		metricExporter, err := otlpmetrichttp.New(ctx, otlpmetrichttp.WithEndpointURL(cfg.OTLPEndpoint))
		if err != nil {
			return nil, err
		}
		traceOpts = append(traceOpts, sdktrace.WithBatcher(spanExporter))
		metricOpts = append(metricOpts, sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExporter, sdkmetric.WithInterval(metricInterval))))
		slog.Info("telemetry exported over OTLP", "endpoint", cfg.OTLPEndpoint)
	} else {
		slog.Info("telemetry kept in process: " + EnvOTLPEndpoint + " is not set")
	}

	tp := sdktrace.NewTracerProvider(traceOpts...)
	mp := sdkmetric.NewMeterProvider(metricOpts...)
	otel.SetTracerProvider(tp)
	otel.SetMeterProvider(mp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{}))

	return func(ctx context.Context) error {
		ctx, cancel := context.WithTimeout(ctx, shutdownBudget)
		defer cancel()
		return errors.Join(tp.Shutdown(ctx), mp.Shutdown(ctx))
	}, nil
}

// Tracer returns the tracer a package uses to open its own spans.
func Tracer(name string) trace.Tracer { return otel.Tracer(name) }

// Meter returns the meter a package uses to record its own metrics.
func Meter(name string) metric.Meter { return otel.Meter(name) }
