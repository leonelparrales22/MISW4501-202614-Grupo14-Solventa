package assessors

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/ports"
)

// MaxProviders bounds the list of providers.
const MaxProviders = 200

// ProviderService is how operations keeps the registry of providers
// (criterion 2, decision D3). Every change keeps who made it, the e-mail of
// the analyst.
type ProviderService struct {
	store ports.ProviderStore
	clock ports.Clock
	newID func() (string, error)
}

// NewProviderService builds the service.
func NewProviderService(store ports.ProviderStore, clock ports.Clock) *ProviderService {
	return &ProviderService{store: store, clock: clock, newID: randomProviderID}
}

// randomProviderID is "prv-" and 12 random hexadecimal digits.
func randomProviderID() (string, error) {
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("make the id of the provider: %w", err)
	}
	return domain.ProviderIDPrefix + hex.EncodeToString(b), nil
}

// List returns the providers of the filter by name, up to MaxProviders.
func (s *ProviderService) List(ctx context.Context, f ports.ProviderFilter) ([]domain.Provider, error) {
	return s.store.List(ctx, f, MaxProviders)
}

// Get returns the provider, or ports.ErrProviderNotFound.
func (s *ProviderService) Get(ctx context.Context, id string) (domain.Provider, error) {
	return s.store.Get(ctx, id)
}

// Create registers a provider, available from now on. The service gives it
// its id.
func (s *ProviderService) Create(ctx context.Context, in domain.ProviderProfile, by string) (domain.Provider, error) {
	profile, err := in.Validate()
	if err != nil {
		return domain.Provider{}, err
	}
	for attempt := 1; ; attempt++ {
		id, err := s.newID()
		if err != nil {
			return domain.Provider{}, err
		}
		p := domain.NewProvider(id, profile, by, s.clock.Now())
		err = s.store.Create(ctx, p)
		if errors.Is(err, ports.ErrProviderExists) && attempt < maxConflicts {
			continue
		}
		if err != nil {
			return domain.Provider{}, err
		}
		return p, nil
	}
}

// UpdateProfile replaces the name, the specialties, the location and the
// contact of a provider. Its availability does not change.
func (s *ProviderService) UpdateProfile(ctx context.Context, id string, in domain.ProviderProfile,
	by string) (domain.Provider, error) {
	profile, err := in.Validate()
	if err != nil {
		return domain.Provider{}, err
	}
	return s.store.UpdateProfile(ctx, id, profile, by, s.clock.Now())
}

// SetAvailability sets a provider available or unavailable. Setting it
// available releases it from the claim it was busy with (decision D13).
func (s *ProviderService) SetAvailability(ctx context.Context, id string, in domain.AvailabilityRequest,
	by string) (domain.Provider, error) {
	now := s.clock.Now()
	a, until, err := in.Validate(now)
	if err != nil {
		return domain.Provider{}, err
	}
	return s.store.SetAvailability(ctx, id, a, until, by, now)
}
