// Package review is the review of the advisor before a claim enters
// evaluation (HU-W2.1-03): the advisor approves a complete filing, which then
// gets its priority and goes to operations, or returns it to the insured with
// the reason and what is missing. It also reads the history of the status of
// a claim, which the database records as it changes.
package review

import (
	"context"
	"errors"
	"fmt"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/evaluation"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/ports"
)

// Reader is what the advisor reviews a filing with: the same detail
// operations evaluates with.
type Reader interface {
	ReviewDetail(ctx context.Context, claimID string) (evaluation.Evaluation, error)
	ReviewQueue(ctx context.Context, pageSize int, pageToken string) (evaluation.QueuePage, error)
}

// Service reviews filings and reads the history of a claim.
type Service struct {
	claims  ports.ClaimRecords
	store   ports.ReviewStore
	history ports.StatusHistory
	reader  Reader
	clock   ports.Clock
	// wake, when set, is called after an approval so the triage assesses the
	// claim at once instead of on its next sweep.
	wake func()
	// The resubmission of a returned filing (HU-W2.1-04); nil until
	// WithResubmissions.
	resubmissions ports.ResubmissionStore
	supports      ports.SupportStore
	// The decision and the provider of a claim as the insured reads them
	// (HU-W2.1-02); nil until WithOutcome.
	decisions   DecisionReader
	assignments AssignmentReader
}

// DecisionReader reads the decision of operations on a claim, or
// ports.ErrDecisionNotFound.
type DecisionReader interface {
	Decision(ctx context.Context, claimID string) (domain.Decision, error)
}

// AssignmentReader reads the claim with its assignment.
type AssignmentReader interface {
	Case(ctx context.Context, claimID string) (ports.AssignmentCase, error)
}

// WithOutcome makes the history carry the decision on the claim and the
// provider assigned, as the insured reads them (HU-W2.1-02, screen A8).
// Either may be nil.
func (s *Service) WithOutcome(decisions DecisionReader, assignments AssignmentReader) *Service {
	s.decisions, s.assignments = decisions, assignments
	return s
}

// NewService builds the service. wake may be nil.
func NewService(claims ports.ClaimRecords, store ports.ReviewStore, history ports.StatusHistory,
	reader Reader, clock ports.Clock, wake func()) *Service {
	if wake == nil {
		wake = func() {}
	}
	return &Service{claims: claims, store: store, history: history, reader: reader, clock: clock, wake: wake}
}

// Queue returns the filings waiting for review, oldest filed first.
func (s *Service) Queue(ctx context.Context, pageSize int, pageToken string) (evaluation.QueuePage, error) {
	return s.reader.ReviewQueue(ctx, pageSize, pageToken)
}

// Detail is a filing with everything the advisor reviews it with (screen
// S8): the supports to give a verdict on, the other claims of the same policy
// and the reviews it already had.
type Detail struct {
	evaluation.Evaluation
	Checklist []domain.ChecklistItem
	// PriorClaims are the other claims the insured filed on the same policy,
	// newest first.
	PriorClaims []domain.ClaimRecord
	Reviews     []domain.Review
}

// MaxPriorClaims bounds the other claims of the policy the detail lists.
const MaxPriorClaims = 20

// Detail returns the filing to review, or ports.ErrClaimNotFound.
func (s *Service) Detail(ctx context.Context, claimID string) (Detail, error) {
	e, err := s.reader.ReviewDetail(ctx, claimID)
	if err != nil {
		return Detail{}, err
	}
	reviews, err := s.store.Reviews(ctx, claimID)
	if err != nil {
		return Detail{}, err
	}
	same, err := s.claims.ListByOwner(ctx, e.Claim.Subject, ports.ClaimFilter{PolicyID: e.Claim.PolicyID}, nil,
		MaxPriorClaims+1)
	if err != nil {
		return Detail{}, fmt.Errorf("read the other claims of the policy: %w", err)
	}
	d := Detail{Evaluation: e, Checklist: domain.Checklist(e.Claim), Reviews: reviews}
	for _, c := range same {
		if c.ID != claimID && len(d.PriorClaims) < MaxPriorClaims {
			d.PriorClaims = append(d.PriorClaims, c)
		}
	}
	return d, nil
}

// Approve approves a filing waiting for review: the claim enters evaluation.
// A filing with a required support missing is not approved
// (domain.ErrSupportsIncomplete).
func (s *Service) Approve(ctx context.Context, claimID string, advisor domain.Reviewer) (domain.Review, error) {
	c, err := s.claims.Get(ctx, claimID)
	if err != nil {
		return domain.Review{}, err
	}
	r, err := domain.ApproveFiling(c, advisor, s.clock.Now())
	if err != nil {
		return domain.Review{}, err
	}
	stored, err := s.store.Review(ctx, r)
	if err != nil {
		return domain.Review{}, err
	}
	s.wake()
	return stored, nil
}

// Return returns a filing waiting for review to the insured with the reason,
// the message and the verdict on the supports that do not serve.
func (s *Service) Return(ctx context.Context, claimID string, advisor domain.Reviewer, in domain.ReturnRequest) (domain.Review, error) {
	c, err := s.claims.Get(ctx, claimID)
	if err != nil {
		return domain.Review{}, err
	}
	r, err := domain.ReturnFiling(c, advisor, in, s.clock.Now())
	if err != nil {
		return domain.Review{}, err
	}
	return s.store.Review(ctx, r)
}

// History is every status a claim went through.
type History struct {
	Claim   domain.ClaimRecord
	Changes []domain.StatusChange
	// Reviews are the reviews of the advisor, oldest first: who approved or
	// returned the filing and why (screens A8 and A9).
	Reviews []domain.Review
	// LastReturn is the review that returned the claim, while it is
	// returned; nil otherwise.
	LastReturn *domain.Review
	// Resubmissions are the times the insured sent the claim again after a
	// return, oldest first (HU-W2.1-04).
	Resubmissions []domain.Resubmission
	// Requirements are the supports the claim can have, with their rules:
	// what the insured uploads to correct a returned filing (screen A9).
	Requirements []domain.Requirement
	// Decision is the decision of operations and Provider the provider
	// assigned, as the insured reads them (HU-W2.1-02); nil when there is
	// none yet.
	Decision *domain.DecisionView
	Provider *domain.ProviderView
}

// ErrNotOwner means an insured asked for the history of a claim that is not
// theirs. It reads as not found, so nobody learns the claim exists.
var ErrNotOwner = errors.New("claims: the claim is not of the insured")

// History returns the history of a claim. When owner is not empty only the
// claim of that subject is read: an insured reads only their own claims.
func (s *Service) History(ctx context.Context, claimID, owner string) (History, error) {
	c, err := s.claims.Get(ctx, claimID)
	if err != nil {
		return History{}, err
	}
	if owner != "" && c.Subject != owner {
		return History{}, ErrNotOwner
	}
	changes, err := s.history.History(ctx, claimID)
	if err != nil {
		return History{}, err
	}
	out := History{Claim: c, Changes: changes, Requirements: domain.RequirementsOf(c)}
	reviews, err := s.store.Reviews(ctx, claimID)
	if err != nil {
		return History{}, fmt.Errorf("read the reviews of the claim: %w", err)
	}
	out.Reviews = reviews
	if s.resubmissions != nil {
		if out.Resubmissions, err = s.resubmissions.Resubmissions(ctx, claimID); err != nil {
			return History{}, fmt.Errorf("read the resubmissions of the claim: %w", err)
		}
	}
	if err := s.withOutcome(ctx, &out); err != nil {
		return History{}, err
	}
	if c.Status == domain.StatusReturned {
		for i := len(reviews) - 1; i >= 0; i-- {
			if reviews[i].Outcome == domain.ReviewReturned {
				r := reviews[i]
				out.LastReturn = &r
				break
			}
		}
	}
	return out, nil
}

// withOutcome adds the decision and the provider assigned, when the claim has
// them.
func (s *Service) withOutcome(ctx context.Context, h *History) error {
	if s.decisions != nil {
		d, err := s.decisions.Decision(ctx, h.Claim.ID)
		switch {
		case err == nil:
			v := domain.ViewOf(d)
			h.Decision = &v
		case !errors.Is(err, ports.ErrDecisionNotFound):
			return fmt.Errorf("read the decision of the claim: %w", err)
		}
	}
	if s.assignments != nil {
		c, err := s.assignments.Case(ctx, h.Claim.ID)
		if err != nil {
			return fmt.Errorf("read the assignment of the claim: %w", err)
		}
		if c.Assignment != nil {
			v := domain.ProviderViewOf(*c.Assignment)
			h.Provider = &v
		}
	}
	return nil
}
