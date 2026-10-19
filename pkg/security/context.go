package security

import "context"

// claimsKey is the private context key under which the verified claims travel.
type claimsKey struct{}

// WithClaims returns a copy of ctx that carries the verified claims. The
// Authenticate middleware calls it; tests of handlers and use cases call it to
// inject a caller without a real token.
func WithClaims(ctx context.Context, c Claims) context.Context {
	return context.WithValue(ctx, claimsKey{}, c)
}

// ClaimsFromContext returns the claims stored by WithClaims and whether they
// were present.
func ClaimsFromContext(ctx context.Context) (Claims, bool) {
	c, ok := ctx.Value(claimsKey{}).(Claims)
	return c, ok
}
