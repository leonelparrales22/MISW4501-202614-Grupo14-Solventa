package memory

import (
	"context"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/ports"
)

var _ ports.OpenClaimsReader = (*CaseStore)(nil)

// OpenClaims counts the claims of the policy that are not closed (HU-W1.4-02).
func (s *CaseStore) OpenClaims(_ context.Context, policyID string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	open := 0
	for _, c := range s.byID {
		if c.PolicyID == policyID && !domain.Closed(c.Status) {
			open++
		}
	}
	return open, nil
}
