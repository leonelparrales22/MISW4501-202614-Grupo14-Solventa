package rating_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/rating"
)

func ruleValues(rules []domain.AppliedRule) map[string]float64 {
	out := map[string]float64{}
	for _, r := range rules {
		out[r.Code] = r.Value
	}
	return out
}

func TestExplainReportsTheFactorsInTheOrderOfTheFormula(t *testing.T) {
	rules := rating.Explain(ports.RatingInput{Age: 28, InsuredAmount: 100_000_000, TermMonths: 120, Profile: domain.RiskProfile{Score: 0.5}})

	codes := make([]string, 0, len(rules))
	for _, r := range rules {
		codes = append(codes, r.Code)
	}
	assert.Equal(t, []string{"base_monthly_rate", "age_factor", "risk_factor", "term_factor", "rounding_step"}, codes)
	v := ruleValues(rules)
	assert.InDelta(t, 0.0003, v["base_monthly_rate"], 1e-12)
	assert.InDelta(t, 1.25, v["age_factor"], 1e-12, "10 years over 18 at 2.5 % each")
	assert.InDelta(t, 1.2, v["risk_factor"], 1e-12, "0.8 + 0.8 * 0.5")
	assert.InDelta(t, 1.04, v["term_factor"], 1e-12, "10 years at 0.4 % each")
	assert.EqualValues(t, 100, v["rounding_step"])
}

// Regression of the price (decision D7): the factors Explain reports rebuild
// exactly the premium of Rate, over the limits of the table, so the rules kept
// with an offer always explain its price.
func TestExplainRebuildsThePremiumOfRate(t *testing.T) {
	rater := rating.New()
	ages := []int{domain.MinInsuredAge, 19, 35, 69, domain.MaxInsuredAge}
	amounts := []float64{domain.MinInsuredAmount, 87_654_321, 250_000_000, domain.MaxInsuredAmount}
	terms := []int{domain.MinTermMonths, 13, 120, 359, domain.MaxTermMonths}
	scores := []float64{0, 0.19, 0.5, 0.81, 1}

	for _, age := range ages {
		for _, amount := range amounts {
			for _, term := range terms {
				for _, score := range scores {
					in := ports.RatingInput{Age: age, InsuredAmount: amount, TermMonths: term, Profile: domain.RiskProfile{Score: score}}
					name := fmt.Sprintf("age=%d amount=%.0f term=%d score=%.2f", age, amount, term, score)

					r, err := rater.Rate(context.Background(), in)

					require.NoError(t, err, name)
					assert.Equal(t, r.Premium, rating.PremiumFromRules(amount, rating.Explain(in)), name)
					assert.Equal(t, rating.Version, r.Version, name)
				}
			}
		}
	}
}

// The v1 table is not modified by HU-W1.2-03: these premiums were produced by
// the table before the story and must not change. They are worked out by
// hand from the formula: 0.0003 * 1.5 * 1.2 * 1.04 * 250e6 = 140 400 and
// 0.0003 * 2.3 * 1.6 * 1.12 * 2e9 = 2 472 960, rounded to 2 473 000.
func TestTheV1PriceDoesNotChange(t *testing.T) {
	cases := []struct {
		in      ports.RatingInput
		premium float64
	}{
		{ports.RatingInput{Age: 18, InsuredAmount: 100_000_000, TermMonths: 12, Profile: domain.RiskProfile{Score: 0}}, 24_100},
		{ports.RatingInput{Age: 38, InsuredAmount: 250_000_000, TermMonths: 120, Profile: domain.RiskProfile{Score: 0.5}}, 140_400},
		{ports.RatingInput{Age: 70, InsuredAmount: 2_000_000_000, TermMonths: 360, Profile: domain.RiskProfile{Score: 1}}, 2_473_000},
	}
	for _, tc := range cases {
		r, err := rating.New().Rate(context.Background(), tc.in)

		require.NoError(t, err)
		assert.Equal(t, tc.premium, r.Premium, fmt.Sprintf("%+v", tc.in))
	}
}

func TestConditionsOfANormalOffer(t *testing.T) {
	assert.Equal(t, []string{"validity_24h", "onerous_beneficiary_lender", "monthly_premium", "max_insured_age_70"},
		rating.Conditions(domain.ModeNormal))
}

func TestConditionsOfAProvisionalOfferAddTheRequoteNotice(t *testing.T) {
	got := rating.Conditions(domain.ModeProvisional)

	assert.Equal(t, "provisional_requote_when_source_restored", got[len(got)-1])
	assert.Len(t, got, 5)
}
