// Package observability gives every service the same logs, traces and
// metrics: structured JSON logs that carry the correlation and trace ids, an
// HTTP middleware that opens a server span per request and keeps the
// correlation id, and the OpenTelemetry setup that exports to an OTLP
// collector (the AWS Distro for OpenTelemetry sidecar forwards to CloudWatch
// and X-Ray).
//
// Nothing is exported unless OTEL_EXPORTER_OTLP_ENDPOINT is set, so tests and
// local runs need no collector: spans and trace ids still exist, they are just
// not sent anywhere.
package observability
