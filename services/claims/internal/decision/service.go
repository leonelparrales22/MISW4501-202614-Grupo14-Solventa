// Package decision is how operations resolves a claim in evaluation
// (HU-W2.2-03): it approves it with the amount to indemnify, which is paid at
// once to the account of the insured, or rejects it with a reason, which the
// insured is told by e-mail. The payout reuses the mechanism of HA-D02
// (pkg/idempotency): one approval is exactly one disbursement, however many
// times it is retried, and a payout that fails leaves the claim as a payment
// exception the analyst can retry.
package decision

import (
	"context"
	"errors"
	"fmt"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/ports"
)

// ErrPolicyUnavailable means the policy core did not answer, or is not
// configured: without the terms of the coverage nothing can be decided.
var ErrPolicyUnavailable = errors.New("claims: the terms of the policy are unavailable")

// ErrNotCovered means the policy does not cover the incident of the claim
// today, or the policy core no longer knows the policy or the insured.
var ErrNotCovered = errors.New("claims: the policy does not cover the incident of the claim")

// Service decides claims.
type Service struct {
	claims ports.ClaimRecords
	store  ports.DecisionStore
	terms  ports.PolicyTermsReader
	clock  ports.Clock
	wake   func()
}

// NewService builds the service. terms may be nil: then nothing can be
// decided. wake, when set, is called after an approval or a retry so the
// worker pays at once instead of on its next poll.
func NewService(claims ports.ClaimRecords, store ports.DecisionStore, terms ports.PolicyTermsReader,
	clock ports.Clock, wake func()) *Service {
	if wake == nil {
		wake = func() {}
	}
	return &Service{claims: claims, store: store, terms: terms, clock: clock, wake: wake}
}

// Decision returns the decision of a claim, or ports.ErrDecisionNotFound.
func (s *Service) Decision(ctx context.Context, claimID string) (domain.Decision, error) {
	return s.store.Decision(ctx, claimID)
}

// Approve approves a claim in evaluation for approvedMinor, checked against
// the coverage of its incident as the policy core describes it now, and
// leaves its payout due. The worker pays it.
func (s *Service) Approve(ctx context.Context, claimID, analyst string, approvedMinor int64) (domain.Decision, error) {
	c, err := s.decidable(ctx, claimID)
	if err != nil {
		return domain.Decision{}, err
	}
	terms, holder, err := s.termsOf(ctx, c)
	if err != nil {
		return domain.Decision{}, err
	}
	cov, ok := terms.CoverageOf(c.IncidentType)
	if !ok {
		return domain.Decision{}, ErrNotCovered
	}
	d, err := domain.Approve(c, cov, holder, approvedMinor, analyst, s.clock.Now())
	if err != nil {
		return domain.Decision{}, err
	}
	stored, err := s.store.Decide(ctx, d)
	if err != nil {
		return domain.Decision{}, err
	}
	s.wake()
	return stored, nil
}

// Reject rejects a claim in evaluation with its reason and leaves the notice
// to the insured due. The worker sends it.
func (s *Service) Reject(ctx context.Context, claimID, analyst, reason string) (domain.Decision, error) {
	c, err := s.decidable(ctx, claimID)
	if err != nil {
		return domain.Decision{}, err
	}
	// The reason is checked before the core is asked: an empty one is the
	// analyst's to correct whatever the core says.
	if _, err := domain.RejectionReason(reason); err != nil {
		return domain.Decision{}, err
	}
	_, holder, err := s.termsOf(ctx, c)
	if err != nil {
		return domain.Decision{}, err
	}
	d, err := domain.Reject(c, reason, holder.Email, analyst, s.clock.Now())
	if err != nil {
		return domain.Decision{}, err
	}
	stored, err := s.store.Decide(ctx, d)
	if err != nil {
		return domain.Decision{}, err
	}
	s.wake()
	return stored, nil
}

// RetryPayout retries the payout of a payment exception with the same key,
// to the corrected destination when the analyst gives one.
func (s *Service) RetryPayout(ctx context.Context, claimID, analyst string, destination *domain.PayoutAccount) (domain.Decision, error) {
	d, err := s.store.RetryPayout(ctx, claimID, domain.PayoutRetry{By: analyst, At: s.clock.Now().UTC(), Destination: destination})
	if err != nil {
		return domain.Decision{}, err
	}
	s.wake()
	return d, nil
}

// decidable reads the claim and checks it is in evaluation. The store checks
// it again when it records the decision, so two analysts deciding at once
// cannot both win.
func (s *Service) decidable(ctx context.Context, claimID string) (domain.ClaimRecord, error) {
	c, err := s.claims.Get(ctx, claimID)
	if err != nil {
		return domain.ClaimRecord{}, err
	}
	if !domain.Decidable(c.Status) {
		return domain.ClaimRecord{}, ports.ErrNotDecidable
	}
	return c, nil
}

// termsOf reads the policy of the claim with its terms and the insured.
func (s *Service) termsOf(ctx context.Context, c domain.ClaimRecord) (domain.PolicyTerms, domain.Holder, error) {
	if s.terms == nil {
		return domain.PolicyTerms{}, domain.Holder{}, ErrPolicyUnavailable
	}
	terms, err := s.terms.PolicyTerms(ctx, c.Subject, c.PolicyID)
	if err != nil {
		return domain.PolicyTerms{}, domain.Holder{}, coreError(err)
	}
	holder, err := s.terms.Holder(ctx, c.Subject)
	if err != nil {
		return domain.PolicyTerms{}, domain.Holder{}, coreError(err)
	}
	return terms, holder, nil
}

func coreError(err error) error {
	if errors.Is(err, ports.ErrPolicyNotFound) || errors.Is(err, ports.ErrHolderNotFound) {
		return fmt.Errorf("%w: %w", ErrNotCovered, err)
	}
	return fmt.Errorf("%w: %w", ErrPolicyUnavailable, err)
}
