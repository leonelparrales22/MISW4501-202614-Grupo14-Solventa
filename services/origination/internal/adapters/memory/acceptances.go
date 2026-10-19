package memory

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
)

// AcceptanceStore is an in-memory ports.AcceptanceRepository. PENDING: the
// PostgreSQL repository; until then an acceptance survives dependency
// failures but not a restart of the service.
type AcceptanceStore struct {
	mu      sync.Mutex
	byID    map[string]domain.Acceptance
	byOffer map[string]string
}

// NewAcceptanceStore builds an empty AcceptanceStore.
func NewAcceptanceStore() *AcceptanceStore {
	return &AcceptanceStore{byID: map[string]domain.Acceptance{}, byOffer: map[string]string{}}
}

// Create stores the acceptance unless its offer already has one.
func (s *AcceptanceStore) Create(_ context.Context, a domain.Acceptance) (domain.Acceptance, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if id, ok := s.byOffer[a.OfferID]; ok {
		return clone(s.byID[id]), false, nil
	}
	s.byID[a.ID] = clone(a)
	s.byOffer[a.OfferID] = a.ID
	return clone(a), true, nil
}

// Get returns the acceptance with the given id.
func (s *AcceptanceStore) Get(_ context.Context, acceptanceID string) (domain.Acceptance, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.byID[acceptanceID]
	if !ok {
		return domain.Acceptance{}, ports.ErrAcceptanceNotFound
	}
	return clone(a), nil
}

// GetByOffer returns the acceptance of the given offer.
func (s *AcceptanceStore) GetByOffer(_ context.Context, offerID string) (domain.Acceptance, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id, ok := s.byOffer[offerID]
	if !ok {
		return domain.Acceptance{}, ports.ErrAcceptanceNotFound
	}
	return clone(s.byID[id]), nil
}

// Update replaces a stored acceptance.
func (s *AcceptanceStore) Update(_ context.Context, a domain.Acceptance) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.byID[a.ID]; !ok {
		return ports.ErrAcceptanceNotFound
	}
	s.byID[a.ID] = clone(a)
	return nil
}

// ListDue returns the registered acceptances due at now, oldest first.
func (s *AcceptanceStore) ListDue(_ context.Context, now time.Time, limit int) ([]domain.Acceptance, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var due []domain.Acceptance
	for _, a := range s.byID {
		if a.Status == domain.AcceptanceRegistered && !a.NextAttemptAt.After(now) {
			due = append(due, clone(a))
		}
	}
	sort.Slice(due, func(i, j int) bool { return due[i].AcceptedAt.Before(due[j].AcceptedAt) })
	if limit > 0 && len(due) > limit {
		due = due[:limit]
	}
	return due, nil
}

// clone copies the acceptance so callers never share its decision or the
// advisor review.
func clone(a domain.Acceptance) domain.Acceptance {
	if a.Decision != nil {
		d := *a.Decision
		a.Decision = &d
	}
	if a.Review != nil {
		r := *a.Review
		r.AdjustedOffer = cloneAdjustedOffer(a.Review.AdjustedOffer)
		a.Review = &r
	}
	return a
}
