// Package rating is the rating engine: it prices a mortgage life quote from
// the individual risk profile. The embedded channel and the advisor portal
// share it, so a customer gets the same price in any channel (HU-W1.1-01).
//
// Version v1 is a provisional actuarial table: HU-W1.2-03 can refine it
// behind the ports.Rater contract without changing any caller.
package rating

import (
	"context"
	"errors"
	"math"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
)

// Version identifies the table, so a quote tells how it was priced.
const Version = "v1"

// Parameters of rating v1.
const (
	// baseMonthlyRate is the monthly premium over the insured amount for a
	// customer of the minimum age and an average risk (0.03 %).
	baseMonthlyRate = 0.0003
	// agePremiumPerYear raises the rate for each year over the minimum age.
	agePremiumPerYear = 0.025
	// A score of 0 (lowest risk) pays riskFloor times the rate; a score of 1
	// pays riskFloor+riskSpan times.
	riskFloor = 0.8
	riskSpan  = 0.8
	// termPremiumPerYear raises the rate for long terms (the exposure to
	// disability and death grows with the years).
	termPremiumPerYear = 0.004
	// roundingStep rounds the premium to the nearest 100 pesos.
	roundingStep = 100
)

// ErrInvalidInput is returned for an input the table cannot price.
var ErrInvalidInput = errors.New("rating: invalid input")

// Rater implements ports.Rater with the v1 table.
type Rater struct{}

var _ ports.Rater = Rater{}

// New returns the v1 Rater.
func New() Rater { return Rater{} }

// Rate prices the quote: monthly premium and the two coverages of the product.
func (Rater) Rate(_ context.Context, in ports.RatingInput) (domain.Rating, error) {
	p := in.Profile
	if in.Age < domain.MinInsuredAge || in.InsuredAmount <= 0 || in.TermMonths <= 0 ||
		math.IsNaN(p.Score) || p.Score < 0 || p.Score > 1 {
		return domain.Rating{}, ErrInvalidInput
	}
	rate := baseMonthlyRate *
		(1 + float64(in.Age-domain.MinInsuredAge)*agePremiumPerYear) *
		(riskFloor + riskSpan*p.Score) *
		(1 + float64(in.TermMonths)/12*termPremiumPerYear)
	premium := math.Round(in.InsuredAmount*rate/roundingStep) * roundingStep
	return domain.Rating{
		Premium:  premium,
		Currency: domain.DefaultCurrency,
		Coverages: []domain.Coverage{
			{Code: domain.CoverageDeath, InsuredAmount: in.InsuredAmount},
			{Code: domain.CoverageDisability, InsuredAmount: in.InsuredAmount},
		},
		Version: Version,
	}, nil
}
