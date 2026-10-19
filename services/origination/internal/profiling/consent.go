package profiling

import (
	"context"
	"fmt"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
)

// ProfileResolver is the use case ConsentCheckedResolver decorates; the
// Resolver implements it.
type ProfileResolver interface {
	Resolve(ctx context.Context, customerID string, consentActive bool) (domain.ProfileResolution, error)
}

// ConsentCheckedResolver makes the consent a fact verified with the audit
// service instead of a flag the caller asserts (HU-W1.2-01, decision D1). It
// asks first and only then lets the wrapped resolver query the cache or any
// source:
//
//   - the consent is not active: ErrConsentRequired (the handler answers 422);
//   - the consent cannot be verified: ports.ErrConsentUnverifiable, and the
//     wrapped resolver is not called (fail-closed; the handler answers 503
//     request_aborted);
//   - the consent is active: the wrapped resolver runs.
//
// The consent_active the caller sent is ignored when there is a verifier.
type ConsentCheckedResolver struct {
	next     ProfileResolver
	verifier ports.ConsentVerifier
}

// NewConsentCheckedResolver wraps next. With a nil verifier it is transparent
// and the consent_active asserted by the caller is still what counts (the
// behavior before HU-W1.2-01).
func NewConsentCheckedResolver(next ProfileResolver, verifier ports.ConsentVerifier) *ConsentCheckedResolver {
	return &ConsentCheckedResolver{next: next, verifier: verifier}
}

// Resolve verifies the consent and then resolves the profile.
func (r *ConsentCheckedResolver) Resolve(ctx context.Context, customerID string, consentActive bool) (domain.ProfileResolution, error) {
	if r.verifier == nil {
		return r.next.Resolve(ctx, customerID, consentActive)
	}
	status, err := r.verifier.Verify(ctx, customerID)
	if err != nil {
		return domain.ProfileResolution{}, fmt.Errorf("verify the consent: %w", err)
	}
	if !status.Active {
		return domain.ProfileResolution{}, ErrConsentRequired
	}
	return r.next.Resolve(ctx, customerID, true)
}
