package observability

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"regexp"
)

// CorrelationHeader carries the id that ties together every log line and span
// of one business request across services.
const CorrelationHeader = "X-Correlation-Id"

type correlationKey struct{}

// validCorrelationID keeps hostile or oversized values out of the logs.
var validCorrelationID = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

// WithCorrelationID returns a context that carries the correlation id.
func WithCorrelationID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, correlationKey{}, id)
}

// CorrelationID returns the correlation id of the context, or "" if none.
func CorrelationID(ctx context.Context) string {
	id, _ := ctx.Value(correlationKey{}).(string)
	return id
}

// newCorrelationID returns a random 128-bit id in hexadecimal.
func newCorrelationID() string {
	var b [16]byte
	// crypto/rand.Read does not fail on supported platforms; an all-zero id
	// would still be a usable (if useless) correlation value.
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
