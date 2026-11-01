package ports

import (
	"context"
	"errors"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
)

// ErrPolicyNotFound means the policy core has no such policy for the holder
// (HU-W2.2-03): it does not exist or belongs to someone else.
var ErrPolicyNotFound = errors.New("claims: the holder has no such policy")

// ErrHolderNotFound means the policy core does not know the holder.
var ErrHolderNotFound = errors.New("claims: the policy core does not know the holder")

// PolicyTermsReader is the policy core seen from the evaluation of a claim
// (HU-W2.2-03): the terms of the policy and who the insured is. Any failure
// of the core is ErrPolicyDirectoryUnavailable.
type PolicyTermsReader interface {
	// PolicyTerms returns the policy of the holder with the terms of each
	// coverage, or ErrPolicyNotFound.
	PolicyTerms(ctx context.Context, holder, policyID string) (domain.PolicyTerms, error)
	// Holder returns the profile of the holder, or ErrHolderNotFound.
	Holder(ctx context.Context, holder string) (domain.Holder, error)
}

// ClaimQueue reads the claims of every insured by status, for operations
// (HU-W2.2-03). It is apart from ClaimRecords, which reads the claims of one
// insured, so the stores implement it without touching that port.
type ClaimQueue interface {
	// ListByStatus returns up to limit claims in one of the statuses, oldest
	// filed first, after the cursor when there is one. The supports and the
	// evidence are not loaded.
	ListByStatus(ctx context.Context, statuses []domain.Status, after *ClaimCursor, limit int) ([]domain.ClaimRecord, error)
}
