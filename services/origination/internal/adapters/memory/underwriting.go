package memory

import (
	"context"
	"errors"
	"strings"
	"sync"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
)

// DefaultReviewScoreThreshold is the default profile score above which
// UnderwriterStub routes a normal offer to a human decision
// (UNDERWRITING_REVIEW_SCORE_THRESHOLD). A score of 0 is the lowest risk. It
// is a review rule of its own, independent of the pricing categories of
// domain.CategoryForScore.
const DefaultReviewScoreThreshold = 0.8

// DefaultFlakyFailures is how many attempts UnderwriterStub fails for a
// demo-flaky offer before it decides.
const DefaultFlakyFailures = 2

// ErrUnderwritingUnavailable is the failure UnderwriterStub simulates for
// demo-flaky offers.
var ErrUnderwritingUnavailable = errors.New("underwriting unavailable (simulated)")

// UnderwriterStub is an in-memory ports.Underwriter with provisional rules.
// PENDING: the real automatic underwriting engine. Rules:
//   - a provisional offer goes to human review (provisional_offer);
//   - a profile score above reviewScoreThreshold goes to human review
//     (high_risk_score);
//   - otherwise the risk is approved.
//
// For demo-flaky offers it fails the first flakyFailures attempts of each
// acceptance, to show how the journey resumes (HU-W1.3-01 CA-5).
type UnderwriterStub struct {
	clock                ports.Clock
	flakyFailures        int
	reviewScoreThreshold float64
	mu                   sync.Mutex
	failures             map[string]int
}

// NewUnderwriterStub builds an UnderwriterStub that routes the normal offers
// with a profile score above reviewScoreThreshold to a human decision.
func NewUnderwriterStub(clock ports.Clock, flakyFailures int, reviewScoreThreshold float64) *UnderwriterStub {
	return &UnderwriterStub{
		clock:                clock,
		flakyFailures:        flakyFailures,
		reviewScoreThreshold: reviewScoreThreshold,
		failures:             map[string]int{},
	}
}

// Decide applies the provisional rules.
func (u *UnderwriterStub) Decide(_ context.Context, offer domain.Offer, a domain.Acceptance) (domain.UnderwritingDecision, error) {
	if strings.HasPrefix(offer.ID, "demo-"+ScenarioFlaky+"-") && u.failNow(a.ID) {
		return domain.UnderwritingDecision{}, ErrUnderwritingUnavailable
	}

	d := domain.UnderwritingDecision{Outcome: domain.UnderwritingApproved, DecidedAt: u.clock.Now().UTC()}
	switch {
	case offer.Mode == domain.ModeProvisional:
		d.Outcome, d.Reason = domain.UnderwritingHumanReview, domain.ReviewReasonProvisionalOffer
	case offer.ProfileScore > u.reviewScoreThreshold:
		d.Outcome, d.Reason = domain.UnderwritingHumanReview, domain.ReviewReasonHighRiskScore
	}
	return d, nil
}

// failNow counts one simulated failure for the acceptance while it has not
// reached flakyFailures.
func (u *UnderwriterStub) failNow(acceptanceID string) bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.failures[acceptanceID] >= u.flakyFailures {
		return false
	}
	u.failures[acceptanceID]++
	return true
}
