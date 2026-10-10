// Package ports defines the interfaces the origination core needs from the
// outside world. Adapters implement them.
package ports

import (
	"context"
	"errors"
	"time"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
)

// ErrSourceUnavailable is returned by a RiskProfileSource that timed out,
// failed or was isolated by its circuit breaker.
var ErrSourceUnavailable = errors.New("external profile source unavailable")

// RiskProfileSource is the external data source (Open Finance) that
// calculates a fresh risk profile.
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
