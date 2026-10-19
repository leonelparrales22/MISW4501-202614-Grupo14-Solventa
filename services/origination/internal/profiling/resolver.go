// Package profiling resolves the risk profile used to price an offer. A
// complete cached profile (at most 30 days old) is reused without querying the
// external sources. When the sources are down, slow or isolated, it degrades to
// the cached profile and finally to the base actuarial profile, so every
// request gets an answer.
package profiling

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
)

// MaxCachedProfileAge is the oldest cached profile that may be reused or back a provisional offer.
const MaxCachedProfileAge = 30 * 24 * time.Hour

// ErrConsentRequired is returned when the customer has no active consent: no
// external source and no cached profile may be used.
var ErrConsentRequired = errors.New("active customer consent is required")

// Resolver implements the profile resolution: reuse of the cached profile,
// calculation from the sources, and the degradation chain cache, base profile.
type Resolver struct {
	source ports.RiskProfileSource
	cache  ports.RiskProfileCache
	clock  ports.Clock
	flight singleflight.Group
	// reuse enables the reuse of a complete cached profile (HU-W1.2-02).
	reuse bool
}

// Option tunes a Resolver.
type Option func(*Resolver)

// WithCachedProfileReuse makes the Resolver answer with a complete cached
// profile of at most 30 days without querying the external sources
// (HU-W1.2-02, criterion 1). Without it the sources are always queried first
// and the cache only backs a provisional answer (HA-D01, HA-L01).
func WithCachedProfileReuse() Option {
	return func(r *Resolver) { r.reuse = true }
}

// NewResolver builds a Resolver.
func NewResolver(source ports.RiskProfileSource, cache ports.RiskProfileCache, clock ports.Clock, opts ...Option) *Resolver {
	r := &Resolver{source: source, cache: cache, clock: clock}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

// Resolve returns a profile for the customer. Apart from ErrConsentRequired
// and a canceled context, it never fails: source and cache faults degrade
// the answer instead of aborting the journey.
//
// With WithCachedProfileReuse, a complete cached profile of at most 30 days
// is reused as a normal answer and no external source is queried
// (HU-W1.2-02). A partial one is not: it was built without some source, so
// the sources are queried again and the partial profile only backs a
// provisional answer if they fail.
func (r *Resolver) Resolve(ctx context.Context, customerID string, consentActive bool) (domain.ProfileResolution, error) {
	if !consentActive {
		return domain.ProfileResolution{}, ErrConsentRequired
	}
	if r.reuse {
		if cached, found := r.validCachedProfile(ctx, customerID); found && !cached.Partial {
			return domain.ProfileResolution{Profile: cached, Mode: domain.ModeNormal, Source: domain.SourceCache}, nil
		}
	}

	// Simultaneous requests for the same customer share one calculation. It
	// runs detached from any single caller, bounded by the fan-out deadline, so
	// one disconnect does not fail the others.
	ch := r.flight.DoChan(customerID, func() (any, error) {
		return r.calculate(context.WithoutCancel(ctx), customerID)
	})
	select {
	case <-ctx.Done():
		return domain.ProfileResolution{}, ctx.Err()
	case res := <-ch:
		if res.Err != nil {
			return domain.ProfileResolution{}, res.Err
		}
		return res.Val.(domain.ProfileResolution), nil
	}
}

// Invalidate drops the customer's cached profile so the next resolution
// queries the sources again. It is called when the customer is requoted.
func (r *Resolver) Invalidate(ctx context.Context, customerID string) error {
	return r.cache.Invalidate(ctx, customerID)
}

func (r *Resolver) calculate(ctx context.Context, customerID string) (domain.ProfileResolution, error) {
	profile, err := r.source.FetchProfile(ctx, customerID)
	if err == nil {
		if cerr := r.cache.Set(ctx, profile); cerr != nil {
			slog.Warn("could not refresh the risk profile cache", "error", cerr)
		}
		return domain.ProfileResolution{
			Profile: profile,
			Mode:    domain.ModeNormal,
			Source:  profile.FreshSource(),
			Sources: profile.Sources,
		}, nil
	}
	if ctx.Err() != nil {
		return domain.ProfileResolution{}, ctx.Err()
	}
	slog.Warn("external profile source failed, degrading", "error", err)
	outcomes := sourceOutcomes(err)

	if cached, found := r.validCachedProfile(ctx, customerID); found {
		return domain.ProfileResolution{
			Profile:           cached,
			Mode:              domain.ModeProvisional,
			Source:            domain.SourceCache,
			UnavailableSource: domain.ExternalSourceOpenFinance,
			Sources:           outcomes,
		}, nil
	}
	return domain.ProfileResolution{
		Profile:           domain.BaseActuarialProfile(customerID),
		Mode:              domain.ModeProvisional,
		Source:            domain.SourceBaseActuarial,
		UnavailableSource: domain.ExternalSourceOpenFinance,
		Sources:           outcomes,
	}, nil
}

// sourceOutcomes returns the per-source outcomes carried by the error of the
// parallel profiling (HA-L01), so a degraded answer still shows which source
// failed. It is nil for a single source (HA-D01).
func sourceOutcomes(err error) []domain.SourceOutcome {
	var unavailable *ports.SourcesUnavailableError
	if errors.As(err, &unavailable) {
		return unavailable.Outcomes
	}
	return nil
}

func (r *Resolver) validCachedProfile(ctx context.Context, customerID string) (domain.RiskProfile, bool) {
	cached, found, err := r.cache.Get(ctx, customerID)
	if err != nil {
		slog.Warn("could not read the risk profile cache", "error", err)
		return domain.RiskProfile{}, false
	}
	if !found || r.clock.Now().Sub(cached.CalculatedAt) > MaxCachedProfileAge {
		return domain.RiskProfile{}, false
	}
	return cached, true
}
