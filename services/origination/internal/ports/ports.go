// Package ports defines the interfaces the origination core needs from the
// outside world. Adapters implement them.
package ports

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
)

// ErrSourceUnavailable is returned by a RiskProfileSource that timed out,
// failed or was isolated by its circuit breaker.
var ErrSourceUnavailable = errors.New("external profile source unavailable")

// ErrCircuitOpen is returned, together with ErrSourceUnavailable, by a
// RiskProfileSource whose circuit breaker is isolating the provider: the call
// was rejected without waiting (HA-L01 reports it per source).
var ErrCircuitOpen = errors.New("external profile source isolated by its circuit breaker")

// SourcesUnavailableError is returned by a composite RiskProfileSource (the
// parallel profiling of HA-L01) when no profile can be calculated online: no
// source answered in time, or a required one did not. It wraps
// ErrSourceUnavailable, so errors.Is keeps working, and keeps the outcome of
// every source so the degraded answer can still show which one failed.
type SourcesUnavailableError struct {
	Outcomes []domain.SourceOutcome
}

func (e *SourcesUnavailableError) Error() string {
	parts := make([]string, 0, len(e.Outcomes))
	for _, o := range e.Outcomes {
		parts = append(parts, o.Source+"="+string(o.Status))
	}
	return ErrSourceUnavailable.Error() + " (" + strings.Join(parts, ", ") + ")"
}

// Unwrap lets errors.Is match ErrSourceUnavailable.
func (e *SourcesUnavailableError) Unwrap() error { return ErrSourceUnavailable }

// RiskProfileSource is an external data source (Open Finance, Open Data, KYC),
// or the parallel fan-out over them, that calculates a fresh risk profile.
type RiskProfileSource interface {
	FetchProfile(ctx context.Context, customerID string) (domain.RiskProfile, error)
}

// RiskProfileCache is Perfil_Riesgo_Cache: the last valid profile per customer.
type RiskProfileCache interface {
	// Get returns found=false on a miss; a miss is not an error.
	Get(ctx context.Context, customerID string) (profile domain.RiskProfile, found bool, err error)
	Set(ctx context.Context, profile domain.RiskProfile) error
}

// Clock allows the cached-profile age to be tested deterministically.
type Clock interface {
	Now() time.Time
}
