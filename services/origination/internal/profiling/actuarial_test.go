package profiling_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/profiling"
)

func signal(score float64, at time.Time) domain.RiskProfile {
	return domain.RiskProfile{Score: score, Category: "ignored", CalculatedAt: at}
}

func TestDefaultWeights(t *testing.T) {
	w := profiling.DefaultWeights()

	assert.Equal(t, 0.5, w[domain.ExternalSourceOpenFinance])
	assert.Equal(t, 0.3, w[domain.ExternalSourceOpenData])
	assert.Equal(t, 0.2, w[domain.ExternalSourceKYC])
}

func TestCombineWeightedUsesTheActuarialWeights(t *testing.T) {
	at := now.Add(-time.Hour)

	got := profiling.CombineWeighted("c1",
		[]domain.RiskProfile{signal(0.2, at), signal(0.4, now), signal(0.6, now)},
		[]string{domain.ExternalSourceOpenFinance, domain.ExternalSourceOpenData, domain.ExternalSourceKYC},
		profiling.DefaultWeights())

	assert.Equal(t, "c1", got.CustomerID)
	assert.InDelta(t, 0.5*0.2+0.3*0.4+0.2*0.6, got.Score, 1e-9)
	assert.Equal(t, "standard", got.Category, "the category follows the combined score, not a source")
	assert.Equal(t, at, got.CalculatedAt, "the date of the highest-priority source present")
}

func TestCombineWeightedRenormalizesOverTheSourcesPresent(t *testing.T) {
	got := profiling.CombineWeighted("c1",
		[]domain.RiskProfile{signal(0.4, now), signal(0.6, now)},
		[]string{domain.ExternalSourceOpenData, domain.ExternalSourceKYC},
		profiling.DefaultWeights())

	assert.InDelta(t, (0.3*0.4+0.2*0.6)/0.5, got.Score, 1e-9)
}

func TestCombineWeightedIgnoresSourcesWithoutWeight(t *testing.T) {
	weights := map[string]float64{domain.ExternalSourceOpenFinance: 1, domain.ExternalSourceKYC: 0}

	got := profiling.CombineWeighted("c1",
		[]domain.RiskProfile{signal(0.2, now), signal(0.9, now)},
		[]string{domain.ExternalSourceOpenFinance, domain.ExternalSourceKYC}, weights)

	assert.InDelta(t, 0.2, got.Score, 1e-9, "a zero weight leaves the source out of the score")
}

func TestCombineWeightedWithoutUsableWeightsFallsBackToTheAverage(t *testing.T) {
	got := profiling.CombineWeighted("c1",
		[]domain.RiskProfile{signal(0.2, now), signal(0.6, now)},
		[]string{domain.ExternalSourceOpenFinance, domain.ExternalSourceOpenData}, map[string]float64{"other": 1})

	assert.InDelta(t, 0.4, got.Score, 1e-9)
}

func TestParallelSourceWithWeightsAppliesThemToTheSignalsThatArrived(t *testing.T) {
	cfg := openFinanceRequired()
	cfg.Weights = profiling.DefaultWeights()
	src := profiling.NewParallelSource(named(fast(0.2, "preferred"), fast(0.4, "standard"), slowSource()), cfg)

	got, err := src.FetchProfile(context.Background(), "c1")

	require.NoError(t, err)
	assert.True(t, got.Partial)
	assert.InDelta(t, (0.5*0.2+0.3*0.4)/0.8, got.Score, 1e-9)
}

func TestParallelSourceCustomCombineTakesPrecedenceOverWeights(t *testing.T) {
	cfg := openFinanceRequired()
	cfg.Weights = profiling.DefaultWeights()
	cfg.Combine = func(id string, _ []domain.RiskProfile) domain.RiskProfile {
		return domain.RiskProfile{CustomerID: id, Score: 0.99}
	}
	src := profiling.NewParallelSource(named(fast(0.2, "preferred"), fast(0.4, "standard"), fast(0.6, "standard")), cfg)

	got, err := src.FetchProfile(context.Background(), "c1")

	require.NoError(t, err)
	assert.Equal(t, 0.99, got.Score)
}
