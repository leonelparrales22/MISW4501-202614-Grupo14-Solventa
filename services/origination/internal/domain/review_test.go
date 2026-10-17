package domain_test

import (
	"math"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
)

func pct(v float64) *float64 { return &v }

func TestParseDecisionType(t *testing.T) {
	for _, s := range []string{"accept", "adjust", "reject"} {
		d, ok := domain.ParseDecisionType(s)
		assert.True(t, ok, s)
		assert.Equal(t, domain.DecisionType(s), d)
	}
	for _, s := range []string{"", "Accept", "approve", "decline"} {
		_, ok := domain.ParseDecisionType(s)
		assert.False(t, ok, s)
	}
}

func TestParseReviewStatus(t *testing.T) {
	for _, s := range []string{"pending", "decided"} {
		r, ok := domain.ParseReviewStatus(s)
		assert.True(t, ok, s)
		assert.Equal(t, domain.ReviewStatus(s), r)
	}
	for _, s := range []string{"", "Pending", "closed"} {
		_, ok := domain.ParseReviewStatus(s)
		assert.False(t, ok, s)
	}
}

func TestIsReviewReasonFollowsTheRoutingReasons(t *testing.T) {
	assert.True(t, domain.IsReviewReason(domain.ReviewReasonProvisionalOffer))
	assert.True(t, domain.IsReviewReason(domain.ReviewReasonHighRiskScore))
	for _, s := range []string{"", "low_risk_score", "Provisional_offer"} {
		assert.False(t, domain.IsReviewReason(s), s)
	}
}

// CA-2: valid partitions of the decision input.
func TestDecisionInputValidAccepts(t *testing.T) {
	valid := []domain.DecisionInput{
		{Type: domain.DecisionAccept},
		{Type: domain.DecisionAccept, Observations: "Perfil verificado por teléfono"},
		{Type: domain.DecisionReject, Observations: "Riesgo fuera de política"},
		{Type: domain.DecisionAdjust, Observations: "Recargo por ingreso no verificado", PremiumAdjustmentPct: pct(10)},
		{Type: domain.DecisionAdjust, Observations: "x", PremiumAdjustmentPct: pct(0.01)},
		{Type: domain.DecisionAdjust, Observations: "x", PremiumAdjustmentPct: pct(100)},
		{Type: domain.DecisionAdjust, Observations: "x", PremiumAdjustmentPct: pct(12.5)},
		{Type: domain.DecisionReject, Observations: strings.Repeat("ñ", domain.MaxObservationsLength)},
	}
	for _, in := range valid {
		assert.NoError(t, in.Validate(), "%+v", in)
	}
}

// CA-2: observations are required to adjust or reject.
func TestDecisionInputRequiresObservationsToAdjustOrReject(t *testing.T) {
	for _, obs := range []string{"", "   ", "\t\n"} {
		assert.ErrorIs(t, domain.DecisionInput{Type: domain.DecisionReject, Observations: obs}.Validate(),
			domain.ErrObservationsRequired, "reject %q", obs)
		assert.ErrorIs(t, domain.DecisionInput{Type: domain.DecisionAdjust, Observations: obs, PremiumAdjustmentPct: pct(5)}.Validate(),
			domain.ErrObservationsRequired, "adjust %q", obs)
	}
}

func TestDecisionInputLimitsTheObservations(t *testing.T) {
	long := strings.Repeat("a", domain.MaxObservationsLength+1)
	for _, typ := range []domain.DecisionType{domain.DecisionAccept, domain.DecisionReject} {
		assert.ErrorIs(t, domain.DecisionInput{Type: typ, Observations: long}.Validate(), domain.ErrInvalidDecision, typ)
	}
}

func TestDecisionInputRejectsAnUnknownType(t *testing.T) {
	for _, typ := range []domain.DecisionType{"", "approve"} {
		assert.ErrorIs(t, domain.DecisionInput{Type: typ, Observations: "x"}.Validate(), domain.ErrInvalidDecision)
	}
}

// CA-2 and D-4 a: boundary values of the premium adjustment.
func TestDecisionInputChecksThePremiumAdjustment(t *testing.T) {
	invalid := []*float64{nil, pct(0), pct(-1), pct(-0.01), pct(100.01), pct(150), pct(10.005), pct(0.001),
		pct(math.NaN()), pct(math.Inf(1))}
	for _, p := range invalid {
		in := domain.DecisionInput{Type: domain.DecisionAdjust, Observations: "x", PremiumAdjustmentPct: p}
		assert.ErrorIs(t, in.Validate(), domain.ErrInvalidPremiumAdjustment, "%v", p)
	}
	for _, typ := range []domain.DecisionType{domain.DecisionAccept, domain.DecisionReject} {
		in := domain.DecisionInput{Type: typ, Observations: "x", PremiumAdjustmentPct: pct(10)}
		assert.ErrorIs(t, in.Validate(), domain.ErrInvalidPremiumAdjustment, "%s with an adjustment", typ)
	}
}

func TestAdjustPremiumRoundsToTwoDecimals(t *testing.T) {
	assert.InDelta(t, 98450.0, domain.AdjustPremium(89500, 10), 1e-9)
	assert.InDelta(t, 96212.5, domain.AdjustPremium(89500, 7.5), 1e-9)
	assert.InDelta(t, 179000.0, domain.AdjustPremium(89500, 100), 1e-9)
	assert.InDelta(t, 100.01, domain.AdjustPremium(100, 0.01), 1e-9)
	assert.InDelta(t, 33.37, domain.AdjustPremium(33.333, 0.1), 1e-9)
}

func TestAdjustedOfferIDAndNoticeKeyAreDeterministic(t *testing.T) {
	assert.Equal(t, "adj-acc-1", domain.AdjustedOfferID("acc-1"))
	assert.Equal(t, domain.AdjustedOfferID("acc-1"), domain.AdjustedOfferID("acc-1"))
	assert.Equal(t, "review-outcome:acc-1", domain.ReviewOutcomeIdempotencyKey("acc-1"))
}
