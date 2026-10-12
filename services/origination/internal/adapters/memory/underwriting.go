package memory

import (
	"context"
	"errors"
	"strings"
	"sync"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
)

// LowRiskScoreThreshold is the profile score under which UnderwriterStub
// routes a request to a human decision.
const LowRiskScoreThreshold = 0.4

// DefaultFlakyFailures is how many attempts UnderwriterStub fails for a
// demo-flaky offer before it decides.
const DefaultFlakyFailures = 2

// ErrUnderwritingUnavailable is the failure UnderwriterStub simulates for
// demo-flaky offers.
var ErrUnderwritingUnavailable = errors.New("underwriting unavailable (simulated)")

// UnderwriterStub is an in-memory ports.Underwriter with provisional rules.
// PENDING: the real automatic underwriting engine. Rules:
//   - a provisional offer goes to human review (provisional_offer);
//   - a profile score under LowRiskScoreThreshold goes to human review
//     (low_risk_score);
//   - otherwise the risk is approved.
//
// For demo-flaky offers it fails the first flakyFailures attempts of each
// acceptance, to show how the journey resumes (HU-W1.3-01 CA-5).
type UnderwriterStub struct {
	clock         ports.Clock
	flakyFailures int
	mu            sync.Mutex
	failures      map[string]int
}

// NewUnderwriterStub builds an UnderwriterStub.
func NewUnderwriterStub(clock ports.Clock, flakyFailures int) *UnderwriterStub {
	return &UnderwriterStub{clock: clock, flakyFailures: flakyFailures, failures: map[string]int{}}
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
	case offer.ProfileScore < LowRiskScoreThreshold:
		d.Outcome, d.Reason = domain.UnderwritingHumanReview, domain.ReviewReasonLowRiskScore
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
