package underwriting

import (
	"context"
	"log/slog"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
)

// HumanAdjustedUnderwriter decorates the automatic underwriting (decision
// D-5 a): an offer created by an advisor adjust decision is approved, because
// an advisor already decided its risk, so it never goes back to the inbox.
// Every other offer is decided by the next underwriter. It works the same
// with any routing rule the automatic underwriting uses.
type HumanAdjustedUnderwriter struct {
	reviews ports.ReviewRepository
	next    ports.Underwriter
	clock   ports.Clock
}

var _ ports.Underwriter = (*HumanAdjustedUnderwriter)(nil)

// NewHumanAdjustedUnderwriter builds a HumanAdjustedUnderwriter over next.
func NewHumanAdjustedUnderwriter(reviews ports.ReviewRepository, next ports.Underwriter, clock ports.Clock) *HumanAdjustedUnderwriter {
	return &HumanAdjustedUnderwriter{reviews: reviews, next: next, clock: clock}
}

// Decide approves an adjusted offer and delegates the rest. A failure to read
// the decisions is returned, so the acceptance processor retries it.
func (u *HumanAdjustedUnderwriter) Decide(ctx context.Context, offer domain.Offer, a domain.Acceptance) (domain.UnderwritingDecision, error) {
	d, found, err := u.reviews.DecisionForAdjustedOffer(ctx, offer.ID)
	if err != nil {
		return domain.UnderwritingDecision{}, err
	}
	if !found {
		return u.next.Decide(ctx, offer, a)
	}
	slog.InfoContext(ctx, "adjusted offer approved by the advisor decision", "acceptance_id", a.ID, "offer_id", offer.ID,
		"decision_id", d.ID)
	return domain.UnderwritingDecision{Outcome: domain.UnderwritingApproved, DecidedAt: u.clock.Now().UTC()}, nil
}
