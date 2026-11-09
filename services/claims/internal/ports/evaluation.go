package ports

import (
	"context"
	"errors"
	"time"

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

// Ranks of the queue by priority (HU-W2.2-02): the level of the triage, and
// after the three levels the claims that have none yet, which wait for the
// triage to assess them.
const (
	RankUnassessed = 4
)

// PriorityCursor is where a page of the queue by priority ends: the rank, when
// the claim was filed and its id.
type PriorityCursor struct {
	Rank    int
	FiledAt time.Time
	ID      string
}

// PriorityQuery is one page of the queue by priority. Rank and IncidentTypes
// filter it when they are set: Rank 1 to 3 is a level, RankUnassessed the
// claims without a level.
type PriorityQuery struct {
	Statuses      []domain.Status
	Rank          int
	IncidentTypes []domain.IncidentType
	After         *PriorityCursor
	Limit         int
}

// PrioritizedClaim is a claim of the queue with its rank.
type PrioritizedClaim struct {
	Claim domain.ClaimRecord
	Rank  int
}

// PriorityQueue reads the claims waiting for the decision of operations
// ordered by priority (HU-W2.2-02): Triage 1, 2 and 3, then the ones without a
// level, and in each rank the one that waits the longest first.
type PriorityQueue interface {
	// ListByPriority returns up to limit claims of the query, in the order of
	// the queue, after the cursor when there is one. The supports and the
	// evidence are not loaded.
	ListByPriority(ctx context.Context, q PriorityQuery) ([]PrioritizedClaim, error)
	// CountByRank counts the claims in the statuses by rank, without the
	// other filters of the query, and how many match all of them.
	CountByRank(ctx context.Context, q PriorityQuery) (byRank map[int]int, matching int, err error)
	// MeanResolution is the mean time from the filing to the decision of the
	// claims decided since then, and how many they are.
	MeanResolution(ctx context.Context, since time.Time) (mean time.Duration, decided int, err error)
}
