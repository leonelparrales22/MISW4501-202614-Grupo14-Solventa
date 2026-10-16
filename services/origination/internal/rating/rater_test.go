package rating_test

import (
	"context"
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/rating"
)

func input(age int, score float64) ports.RatingInput {
	return ports.RatingInput{
		Age: age, InsuredAmount: 200_000_000, TermMonths: 120,
		Profile: domain.RiskProfile{CustomerID: "c1", Score: score},
	}
}

func rate(t *testing.T, in ports.RatingInput) domain.Rating {
	t.Helper()
	r, err := rating.New().Rate(context.Background(), in)
	require.NoError(t, err)
	return r
}

func TestRateReturnsPremiumCoveragesAndVersion(t *testing.T) {
	r := rate(t, input(30, 0.5))

	assert.Positive(t, r.Premium)
	assert.Equal(t, "COP", r.Currency)
	assert.Equal(t, rating.Version, r.Version)
	assert.Equal(t, []domain.Coverage{
		{Code: domain.CoverageDeath, InsuredAmount: 200_000_000},
		{Code: domain.CoverageDisability, InsuredAmount: 200_000_000},
	}, r.Coverages)
	assert.Zero(t, math.Mod(r.Premium, 100), "the premium is rounded to 100 pesos")
}

func TestRateIsPersonalizedByRiskAgeAmountAndTerm(t *testing.T) {
	base := rate(t, input(30, 0.5)).Premium

	assert.Greater(t, rate(t, input(30, 0.9)).Premium, base, "a riskier profile pays more")
	assert.Less(t, rate(t, input(30, 0.1)).Premium, base, "a safer profile pays less")
	assert.Greater(t, rate(t, input(55, 0.5)).Premium, base, "an older customer pays more")

	bigger := input(30, 0.5)
	bigger.InsuredAmount *= 2
	assert.Greater(t, rate(t, bigger).Premium, base)

	longer := input(30, 0.5)
	longer.TermMonths = 360
	assert.Greater(t, rate(t, longer).Premium, base)
}

func TestRateTheSameInputGivesTheSamePrice(t *testing.T) {
	assert.Equal(t, rate(t, input(41, 0.37)), rate(t, input(41, 0.37)))
}

func TestRateRejectsInputsItCannotPrice(t *testing.T) {
	bad := []ports.RatingInput{
		input(17, 0.5), input(30, -0.1), input(30, 1.1), input(30, math.NaN()),
		{Age: 30, InsuredAmount: 0, TermMonths: 12, Profile: domain.RiskProfile{Score: 0.5}},
		{Age: 30, InsuredAmount: 1e8, TermMonths: 0, Profile: domain.RiskProfile{Score: 0.5}},
	}
	for _, in := range bad {
		_, err := rating.New().Rate(context.Background(), in)
		assert.ErrorIs(t, err, rating.ErrInvalidInput)
	}
}
