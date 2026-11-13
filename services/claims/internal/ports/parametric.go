package ports

import (
	"context"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
)

// ParametricStore keeps the claims the parametric engine resolves
// (HU-M2.4-01). The claim, its automatic resolution and its approval are
// written in one transaction, so a claim is never left filed without its
// payout or paid twice; when the payout is confirmed the store writes
// ParametricPayoutCompleted to its outbox in the same statement.
type ParametricStore interface {
	// Resolve files the claim, records its resolution and its approval, and
	// returns the stored claim with created true. When the observation was
	// already resolved it returns the claim of that resolution with created
	// false and writes nothing.
	Resolve(ctx context.Context, c domain.ClaimRecord, r domain.ParametricResolution,
		d domain.Decision) (stored domain.ClaimRecord, created bool, err error)
}
