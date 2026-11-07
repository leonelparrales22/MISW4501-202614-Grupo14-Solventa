package memory

import (
	"context"
	"sync"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/ports"
)

// ReviewStore keeps the reviews of the advisor over the claims of a
// CaseStore (HU-W2.1-03) with the same rules as PostgreSQL: recording one
// moves the status of the claim too. Everything is lost when the service
// restarts.
type ReviewStore struct {
	cases *CaseStore

	mu      sync.Mutex
	reviews map[string][]domain.Review
}

// NewReviewStore builds the store over the claims of cases.
func NewReviewStore(cases *CaseStore) *ReviewStore {
	return &ReviewStore{cases: cases, reviews: map[string][]domain.Review{}}
}

var (
	_ ports.ReviewStore   = (*ReviewStore)(nil)
	_ ports.StatusHistory = (*ReviewStore)(nil)
)

// Review records the review of a filing waiting for review.
func (s *ReviewStore) Review(_ context.Context, r domain.Review) (domain.Review, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cases.mu.Lock()
	defer s.cases.mu.Unlock()
	c, ok := s.cases.byID[r.ClaimID]
	switch {
	case !ok:
		return domain.Review{}, ports.ErrClaimNotFound
	case !domain.Reviewable(c.Status):
		return domain.Review{}, domain.ErrNotReviewable
	}
	r.FilingNumber, r.Seq = c.FilingNumber, len(s.reviews[r.ClaimID])+1
	r = copyReview(r)
	s.reviews[r.ClaimID] = append(s.reviews[r.ClaimID], r)
	s.cases.moveTo(r.ClaimID, r.StatusOf(), r.ReviewedAt)
	return copyReview(r), nil
}

// Reviews returns the reviews of a claim, oldest first.
func (s *ReviewStore) Reviews(_ context.Context, claimID string) ([]domain.Review, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]domain.Review, 0, len(s.reviews[claimID]))
	for _, r := range s.reviews[claimID] {
		out = append(out, copyReview(r))
	}
	return out, nil
}

// History returns the changes of status of a claim, oldest first.
func (s *ReviewStore) History(ctx context.Context, claimID string) ([]domain.StatusChange, error) {
	return s.cases.History(ctx, claimID)
}

func copyReview(r domain.Review) domain.Review {
	r.Supports = append([]domain.SupportAssessment(nil), r.Supports...)
	r.FactsToCorrect = append([]domain.Fact(nil), r.FactsToCorrect...)
	return r
}
