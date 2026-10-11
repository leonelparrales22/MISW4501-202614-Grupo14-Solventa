// Package openfinance adapts the shared Open Finance client (pkg/adapters)
// to the RiskProfileSource port of the origination core.
package openfinance

import (
	"context"
	"errors"
	"fmt"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/adapters/openfinance"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/resilience"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
)

// Fetcher is the part of the shared client this adapter needs.
type Fetcher interface {
	FetchProfile(ctx context.Context, customerID string) (openfinance.Profile, error)
}

// Source implements ports.RiskProfileSource.
type Source struct {
	client Fetcher
}

// NewSource wraps a shared Open Finance client.
func NewSource(client Fetcher) *Source {
	return &Source{client: client}
}

// FetchProfile maps the provider profile to the domain and the provider
// failures to ports.ErrSourceUnavailable (plus ports.ErrCircuitOpen when the
// circuit breaker rejected the call, HA-L01).
func (s *Source) FetchProfile(ctx context.Context, customerID string) (domain.RiskProfile, error) {
	p, err := s.client.FetchProfile(ctx, customerID)
	if err != nil {
		if errors.Is(err, openfinance.ErrSourceUnavailable) {
			if errors.Is(err, resilience.ErrOpen) {
				return domain.RiskProfile{}, fmt.Errorf("%w: %w: %w", ports.ErrSourceUnavailable, ports.ErrCircuitOpen, err)
			}
			return domain.RiskProfile{}, fmt.Errorf("%w: %w", ports.ErrSourceUnavailable, err)
		}
		return domain.RiskProfile{}, err
	}
	return domain.RiskProfile{
		CustomerID:   customerID,
		Score:        p.Score,
		Category:     p.Category,
		CalculatedAt: p.UpdatedAt,
	}, nil
}
