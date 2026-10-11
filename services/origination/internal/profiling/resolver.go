// Package profiling resolves the risk profile used to price an offer. When the
// external source is down, slow or isolated, it degrades to the cached profile
// and finally to the base actuarial profile, so every request gets an answer.
package profiling

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
)

// MaxCachedProfileAge is the oldest cached profile that may back a provisional offer.
const MaxCachedProfileAge = 30 * 24 * time.Hour

// ErrConsentRequired is returned when the customer has no active consent: no
// external source and no cached profile may be used.
var ErrConsentRequired = errors.New("active customer consent is required")

// Resolver implements the degradation chain: source, cache, base profile.
type Resolver struct {
	source ports.RiskProfileSource
	cache  ports.RiskProfileCache
	clock  ports.Clock
}

// NewResolver builds a Resolver.
func NewResolver(source ports.RiskProfileSource, cache ports.RiskProfileCache, clock ports.Clock) *Resolver {
	return &Resolver{source: source, cache: cache, clock: clock}
}

// Resolve returns a profile for the customer. Apart from ErrConsentRequired
// and a canceled context, it never fails: source and cache faults degrade
// the answer instead of aborting the journey.
func (r *Resolver) Resolve(ctx context.Context, customerID string, consentActive bool) (domain.ProfileResolution, error) {
	if !consentActive {
		return domain.ProfileResolution{}, ErrConsentRequired
	}

	profile, err := r.source.FetchProfile(ctx, customerID)
	if err == nil {
		if cerr := r.cache.Set(ctx, profile); cerr != nil {
			slog.Warn("could not refresh the risk profile cache", "error", cerr)
		}
		return domain.ProfileResolution{Profile: profile, Mode: domain.ModeNormal, Source: domain.SourceOpenFinance}, nil
	}
	if ctx.Err() != nil {
		return domain.ProfileResolution{}, ctx.Err()
	}
	slog.Warn("external profile source failed, degrading", "error", err)

	if cached, found := r.validCachedProfile(ctx, customerID); found {
		return domain.ProfileResolution{
			Profile:           cached,
			Mode:              domain.ModeProvisional,
			Source:            domain.SourceCache,
			UnavailableSource: domain.ExternalSourceOpenFinance,
		}, nil
	}
	return domain.ProfileResolution{
		Profile:           domain.BaseActuarialProfile(customerID),
		Mode:              domain.ModeProvisional,
		Source:            domain.SourceBaseActuarial,
		UnavailableSource: domain.ExternalSourceOpenFinance,
	}, nil
}

func (r *Resolver) validCachedProfile(ctx context.Context, customerID string) (domain.RiskProfile, bool) {
	cached, found, err := r.cache.Get(ctx, customerID)
	if err != nil {
		slog.Warn("could not read the risk profile cache, using the base profile", "error", err)
		return domain.RiskProfile{}, false
	}
	if !found || r.clock.Now().Sub(cached.CalculatedAt) > MaxCachedProfileAge {
		return domain.RiskProfile{}, false
	}
	return cached, true
}
