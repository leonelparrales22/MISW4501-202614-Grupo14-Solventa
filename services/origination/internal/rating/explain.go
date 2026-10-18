package rating

import (
	"math"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
)

// Codes of the factors that Explain reports (HU-W1.2-03 CA-2). The premium
// is insured_amount * base_monthly_rate * age_factor * risk_factor *
// term_factor, rounded to the nearest rounding_step.
const (
	RuleBaseMonthlyRate = "base_monthly_rate"
	RuleAgeFactor       = "age_factor"
	RuleRiskFactor      = "risk_factor"
	RuleTermFactor      = "term_factor"
	RuleRoundingStep    = "rounding_step"
)

// Codes of the conditions of an offer. The client translates them.
const (
	ConditionValidity24h                = "validity_24h"
	ConditionOnerousBeneficiaryLender   = "onerous_beneficiary_lender"
	ConditionMonthlyPremium             = "monthly_premium"
	ConditionMaxInsuredAge70            = "max_insured_age_70"
	ConditionProvisionalRequoteRestored = "provisional_requote_when_source_restored"
)

// Explain returns the factors the v1 table applies to the input, in the order
// of the formula, so an offer can show how its price was built. It reads the
// parameters of Rate and does not change the price: a test checks that the
// factors rebuild the premium of Rate.
func Explain(in ports.RatingInput) []domain.AppliedRule {
	return []domain.AppliedRule{
		{Code: RuleBaseMonthlyRate, Value: baseMonthlyRate},
		{Code: RuleAgeFactor, Value: 1 + float64(in.Age-domain.MinInsuredAge)*agePremiumPerYear},
		{Code: RuleRiskFactor, Value: riskFloor + riskSpan*in.Profile.Score},
		{Code: RuleTermFactor, Value: 1 + float64(in.TermMonths)/12*termPremiumPerYear},
		{Code: RuleRoundingStep, Value: roundingStep},
	}
}

// PremiumFromRules rebuilds the premium from the factors Explain reported. It
// lets a reader (or a test) check an offer against its stored rules.
func PremiumFromRules(insuredAmount float64, rules []domain.AppliedRule) float64 {
	rate, step := 1.0, 1.0
	for _, r := range rules {
		if r.Code == RuleRoundingStep {
			step = r.Value
			continue
		}
		rate *= r.Value
	}
	return math.Round(insuredAmount*rate/step) * step
}

// Conditions returns the codes of the conditions of an offer priced in the
// given mode: the same for every offer, plus the requote notice when the
// offer is provisional (HU-W1.2-03 CA-3).
func Conditions(mode domain.OfferMode) []string {
	out := []string{
		ConditionValidity24h,
		ConditionOnerousBeneficiaryLender,
		ConditionMonthlyPremium,
		ConditionMaxInsuredAge70,
	}
	if mode == domain.ModeProvisional {
		out = append(out, ConditionProvisionalRequoteRestored)
	}
	return out
}
