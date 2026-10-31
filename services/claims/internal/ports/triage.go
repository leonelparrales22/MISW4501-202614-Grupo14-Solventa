package ports

import (
	"context"
	"errors"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
)

// ErrTriageConflict means another calculation of the claim was recorded in
// the same place first (HU-W2.2-01): two tasks of the service assessed the
// claim at the same time and this one lost.
var ErrTriageConflict = errors.New("claims: another triage calculation of the claim was recorded first")

// TriageCandidate is a claim and its last triage calculation, nil when it has
// none (HU-W2.2-01).
type TriageCandidate struct {
	Claim domain.ClaimRecord
	Last  *domain.TriageRecord
}

// TriageStore keeps the calculations of the level of the claims
// (HU-W2.2-01). It never changes a calculation: it only adds them.
type TriageStore interface {
	// Pending returns, oldest filed first, up to limit claims in one of the
	// statuses that have no calculation, or whose last calculation was made
	// with another estimated amount, another claim type or another version of
	// the policy. The claims come without supports nor evidence.
	Pending(ctx context.Context, statuses []domain.Status, policyVersion string, limit int) ([]TriageCandidate, error)
	// Candidate returns the claim, without supports nor evidence, and its last
	// calculation, or ErrClaimNotFound.
	Candidate(ctx context.Context, claimID string) (TriageCandidate, error)
	// Record adds the calculation, or returns ErrTriageConflict when the claim
	// already has one in that place.
	Record(ctx context.Context, r domain.TriageRecord) error
	// History returns every calculation of the claim, newest first.
	History(ctx context.Context, claimID string) ([]domain.TriageRecord, error)
	// Current returns the claims of the ids that exist with their last
	// calculation. Each claim carries only its id and its status; an id that
	// does not exist is absent from the map.
	Current(ctx context.Context, ids []string) (map[string]TriageCandidate, error)
}
