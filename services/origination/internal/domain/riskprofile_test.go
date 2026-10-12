package domain_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
)

func TestBaseActuarialProfileIsNeutralAndUndated(t *testing.T) {
	p := domain.BaseActuarialProfile("c1")

	assert.Equal(t, "c1", p.CustomerID)
	assert.Equal(t, 0.5, p.Score)
	assert.Equal(t, domain.BaseCategory, p.Category)
	assert.True(t, p.CalculatedAt.IsZero())
}

func TestFreshSourceTellsOpenFinanceAloneFromTheFanOut(t *testing.T) {
	ok := func(name string) domain.SourceOutcome {
		return domain.SourceOutcome{Source: name, Status: domain.SourceStatusOK}
	}
	tests := map[string]struct {
		sources []domain.SourceOutcome
		want    domain.ProfileSource
	}{
		"single source without outcomes (HA-D01)": {sources: nil, want: domain.SourceOpenFinance},
		"fan-out with only Open Finance":          {sources: []domain.SourceOutcome{ok(domain.ExternalSourceOpenFinance)}, want: domain.SourceOpenFinance},
		"fan-out with only another source":        {sources: []domain.SourceOutcome{ok(domain.ExternalSourceKYC)}, want: domain.SourceExternal},
		"fan-out with three sources": {
			sources: []domain.SourceOutcome{ok(domain.ExternalSourceOpenFinance), ok(domain.ExternalSourceOpenData), ok(domain.ExternalSourceKYC)},
			want:    domain.SourceExternal,
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			p := domain.RiskProfile{CustomerID: "c1", Sources: tc.sources}
			assert.Equal(t, tc.want, p.FreshSource())
		})
	}
}

func TestContractValuesOfTheParallelProfiling(t *testing.T) {
	// These strings are part of the public contract (openapi.yaml 0.3.0).
	assert.Equal(t, domain.ProfileSource("external_sources"), domain.SourceExternal)
	assert.Equal(t, "open_data", domain.ExternalSourceOpenData)
	assert.Equal(t, "kyc", domain.ExternalSourceKYC)
	assert.Equal(t, domain.SourceStatus("ok"), domain.SourceStatusOK)
	assert.Equal(t, domain.SourceStatus("timeout"), domain.SourceStatusTimeout)
	assert.Equal(t, domain.SourceStatus("error"), domain.SourceStatusError)
	assert.Equal(t, domain.SourceStatus("circuit_open"), domain.SourceStatusCircuitOpen)
}

func TestCategoryForScore(t *testing.T) {
	tests := map[string]struct {
		score float64
		want  string
	}{
		"lowest":    {0, "preferred"},
		"below 0.2": {0.199, "preferred"},
		"at 0.2":    {0.2, "standard"},
		"at 0.8":    {0.8, "standard"},
		"above 0.8": {0.801, "high_risk"},
		"highest":   {1, "high_risk"},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tc.want, domain.CategoryForScore(tc.score))
		})
	}
}
