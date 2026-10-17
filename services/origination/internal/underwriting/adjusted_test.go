package underwriting_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/memory"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/underwriting"
)

// D-5 a: the offer adjusted by an advisor never goes back to the inbox.
func TestHumanAdjustedUnderwriterApprovesTheAdjustedOffer(t *testing.T) {
	e := newEnv(t, false)
	e.route(t, "acc-1", "demo-review-1", domain.ReviewReasonProvisionalOffer)
	_, err := e.svc.Decide(advisor, "acc-1", adjust(10, "x"))
	require.NoError(t, err)
	adjusted, err := e.offers.OfferStub.Get(context.Background(), "adj-acc-1")
	require.NoError(t, err)
	require.Equal(t, domain.ModeProvisional, adjusted.Mode, "the stub alone would route it again")

	u := underwriting.NewHumanAdjustedUnderwriter(e.reviews, memory.NewUnderwriterStub(e.clock, 0, memory.DefaultReviewScoreThreshold), e.clock)
	d, err := u.Decide(context.Background(), adjusted, domain.Acceptance{ID: "acc-2", OfferID: adjusted.ID})
	require.NoError(t, err)
	assert.Equal(t, domain.UnderwritingDecision{Outcome: domain.UnderwritingApproved, DecidedAt: t0}, d)
}

func TestHumanAdjustedUnderwriterDelegatesEveryOtherOffer(t *testing.T) {
	e := newEnv(t, false)
	offer := e.route(t, "acc-1", "demo-highrisk-1", domain.ReviewReasonHighRiskScore)

	u := underwriting.NewHumanAdjustedUnderwriter(e.reviews, memory.NewUnderwriterStub(e.clock, 0, memory.DefaultReviewScoreThreshold), e.clock)
	d, err := u.Decide(context.Background(), offer, domain.Acceptance{ID: "acc-1"})
	require.NoError(t, err)
	assert.Equal(t, domain.UnderwritingHumanReview, d.Outcome)
	assert.Equal(t, domain.ReviewReasonHighRiskScore, d.Reason)
}

func TestHumanAdjustedUnderwriterReturnsARepositoryFailure(t *testing.T) {
	e := newEnv(t, false)
	boom := errors.New("down")
	e.reviews.adjustedErr = boom

	u := underwriting.NewHumanAdjustedUnderwriter(e.reviews, memory.NewUnderwriterStub(e.clock, 0, memory.DefaultReviewScoreThreshold), e.clock)
	_, err := u.Decide(context.Background(), domain.Offer{ID: "adj-acc-1"}, domain.Acceptance{ID: "acc-2"})
	assert.ErrorIs(t, err, boom, "the processor retries it")
}
