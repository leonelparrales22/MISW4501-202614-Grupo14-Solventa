// Package memory holds in-memory adapters. They are what the service uses
// when it runs without its database, and what the use-case tests run against.
package memory

import (
	"context"
	"sync"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/ports"
)

// ClaimStore keeps the filed cases in memory. They are lost when the service
// restarts.
type ClaimStore struct {
	mu       sync.Mutex
	byID     map[string]domain.Claim
	byKey    map[string]string
	sequence int
}

// NewClaimStore builds an empty store.
func NewClaimStore() *ClaimStore {
	return &ClaimStore{byID: map[string]domain.Claim{}, byKey: map[string]string{}}
}

// Create stores the claim and assigns its filing number.
func (s *ClaimStore) Create(_ context.Context, c domain.Claim) (domain.Claim, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	key := c.Subject + "\x00" + c.FilingKey
	if id, ok := s.byKey[key]; ok {
		existing := s.byID[id]
		if existing.Fingerprint() != c.Fingerprint() {
			return domain.Claim{}, false, ports.ErrFilingKeyReused
		}
		return existing, false, nil
	}

	s.sequence++
	c.FilingNumber = domain.FilingNumber(c.FiledAt, int64(s.sequence))
	s.byID[c.ID] = c
	s.byKey[key] = c.ID
	return c, true, nil
}
