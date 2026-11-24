package memory

import (
	"time"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/ports"
)

// MoveForTest moves a claim to the status as the decision would, for the
// tests of the reads that depend on the status.
func (s *CaseStore) MoveForTest(claimID string, st domain.Status, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.byID[claimID]; !ok {
		return ports.ErrClaimNotFound
	}
	s.moveTo(claimID, st, at)
	return nil
}
