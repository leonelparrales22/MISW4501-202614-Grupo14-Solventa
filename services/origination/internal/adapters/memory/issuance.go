package memory

import (
	"context"
	"log/slog"
	"sync"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
)

// IssuanceStub is an in-memory ports.IssuanceStarter that only records which
// acceptances started issuance. PENDING: the charge of the premium and the
// policy issuance of HU-W1.3-02 and HA-D02.
type IssuanceStub struct {
	mu      sync.Mutex
	started map[string]bool
}

// NewIssuanceStub builds an empty IssuanceStub.
func NewIssuanceStub() *IssuanceStub {
	return &IssuanceStub{started: map[string]bool{}}
}

// Start records the acceptance once.
func (s *IssuanceStub) Start(_ context.Context, a domain.Acceptance, offer domain.Offer) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.started[a.ID] {
		return nil
	}
	s.started[a.ID] = true
	slog.Info("issuance started", "acceptance_id", a.ID, "offer_id", offer.ID)
	return nil
}

// Started tells whether issuance started for the acceptance.
func (s *IssuanceStub) Started(acceptanceID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.started[acceptanceID]
}
