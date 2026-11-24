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
	// resubmissions are the resubmissions of returned filings (HU-W2.1-04).
	resubmissions map[string][]domain.Resubmission
}

// NewReviewStore builds the store over the claims of cases.
func NewReviewStore(cases *CaseStore) *ReviewStore {
	return &ReviewStore{cases: cases, reviews: map[string][]domain.Review{},
		resubmissions: map[string][]domain.Resubmission{}}
}

var (
	_ ports.ReviewStore   = (*ReviewStore)(nil)
	_ ports.StatusHistory = (*ReviewStore)(nil)

	_ ports.ResubmissionStore = (*ReviewStore)(nil)
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

// Resubmit records the resubmission of a returned filing: it corrects the
// claim, attaches the new supports and withdraws the replaced ones, all or
// nothing, while the claim is returned by the review it answers.
func (s *ReviewStore) Resubmit(_ context.Context, r domain.Resubmission) (domain.Resubmission, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cases.mu.Lock()
	defer s.cases.mu.Unlock()
	c, ok := s.cases.byID[r.ClaimID]
	reviews := s.reviews[r.ClaimID]
	switch {
	case !ok:
		return domain.Resubmission{}, ports.ErrClaimNotFound
	case c.Status != domain.StatusReturned, len(reviews) == 0, reviews[len(reviews)-1].Seq != r.ReviewSeq:
		return domain.Resubmission{}, domain.ErrNotReturned
	}
	for _, a := range r.Added {
		if _, taken := s.cases.attachedTo[a.File.ID]; taken {
			return domain.Resubmission{}, ports.ErrSupportAlreadyAttached
		}
	}
	for _, a := range r.Added {
		s.cases.attachedTo[a.File.ID] = r.ClaimID
	}
	corrected := c
	corrected.OccurredAt, corrected.Place, corrected.Description = r.Claim.OccurredAt, r.Claim.Place, r.Claim.Description
	corrected.EstimatedAmountMinor = r.Claim.EstimatedAmountMinor
	corrected.Supports = r.Claim.Supports
	s.cases.byID[r.ClaimID] = copyRecord(corrected)
	s.cases.moveTo(r.ClaimID, domain.StatusActive, r.ResubmittedAt)

	r.Seq = len(s.resubmissions[r.ClaimID]) + 1
	r.Added = nil
	r = copyResubmission(r)
	s.resubmissions[r.ClaimID] = append(s.resubmissions[r.ClaimID], r)
	return copyResubmission(r), nil
}

// Resubmissions returns the resubmissions of a claim, oldest first.
func (s *ReviewStore) Resubmissions(_ context.Context, claimID string) ([]domain.Resubmission, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]domain.Resubmission, 0, len(s.resubmissions[claimID]))
	for _, r := range s.resubmissions[claimID] {
		out = append(out, copyResubmission(r))
	}
	return out, nil
}

func copyResubmission(r domain.Resubmission) domain.Resubmission {
	r.Facts = append([]domain.FactChange(nil), r.Facts...)
	supports := make([]domain.SupportChange, 0, len(r.Supports))
	for _, c := range r.Supports {
		c.Added = append([]string(nil), c.Added...)
		c.Withdrawn = append([]string(nil), c.Withdrawn...)
		supports = append(supports, c)
	}
	r.Supports = supports
	return r
}

func copyReview(r domain.Review) domain.Review {
	r.Supports = append([]domain.SupportAssessment(nil), r.Supports...)
	r.FactsToCorrect = append([]domain.Fact(nil), r.FactsToCorrect...)
	return r
}
