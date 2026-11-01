package policycore_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	corepolicies "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/adapters/policies"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/resilience"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/policycore"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
)

// The answers of the policy core stub, with the shape of stubs/policy-core.
const twoPolicies = `{"policies":[
 {"policy_id":"AU-2025-118204","line":"auto","product":"Autos","insured_object":"KJT-482","status":"active",
  "valid_from":"2025-10-02T00:00:00Z","valid_to":"2026-10-02T00:00:00Z","covered_incidents":["collision","theft"],
  "coverages":[{"incident":"collision","insured_value_minor":7250000000,"currency":"COP","deductible_basis_points":1000},
               {"incident":"theft","insured_value_minor":5000000000,"currency":"COP","deductible_basis_points":1000}]},
 {"policy_id":"","line":"home","status":"active","valid_from":"2026-03-01T00:00:00Z","valid_to":"2027-03-01T00:00:00Z"},
 {"policy_id":"HG-BAD","line":"home","status":"active","valid_from":"2027-03-01T00:00:00Z","valid_to":"2026-03-01T00:00:00Z"}
]}`

const holderProfile = `{"holder_id":"sub-1","full_name":"Carolina Pérez Ruiz","document_type":"CC","document_id":"1032456789",
 "email":"carolina@correo.example","phone":"+57 300 000 0000",
 "payout_account":{"bank_code":"1007","bank_name":"Banco Aliado","account_type":"savings","account_number":"0001-0002-03"}}`

func portfolioOver(t *testing.T, handler http.HandlerFunc) *policycore.Portfolio {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return policycore.NewPortfolio(corepolicies.NewClient(corepolicies.Config{
		BaseURL: srv.URL, Timeout: time.Second, Breaker: resilience.DefaultConfig(),
	}))
}

func TestHolderPoliciesMapsTheCoreAndLeavesOutTheInvalidOnes(t *testing.T) {
	p := portfolioOver(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/holders/sub-1/policies", r.URL.Path)
		_, _ = w.Write([]byte(twoPolicies))
	})

	got, err := p.HolderPolicies(context.Background(), "sub-1")

	require.NoError(t, err)
	require.Len(t, got, 1, "a policy without id or with an inverted validity is left out")
	auto := got[0]
	assert.Equal(t, "AU-2025-118204", auto.ID)
	assert.Equal(t, domain.SourcePolicyCore, auto.Source)
	assert.Equal(t, domain.LineAuto, auto.Line)
	assert.Equal(t, "Autos", auto.Product)
	assert.Equal(t, "KJT-482", auto.InsuredObject)
	assert.Equal(t, domain.PolicyActive, auto.Status)
	assert.Nil(t, auto.Premium)
	assert.Equal(t, "COP", auto.Currency)
	assert.Equal(t, 72_500_000.0, auto.InsuredAmount, "the largest coverage, in pesos and not in cents")
	assert.Equal(t, []domain.Coverage{{Code: "collision", InsuredAmount: 72_500_000}, {Code: "theft", InsuredAmount: 50_000_000}},
		auto.Coverages)
	assert.Equal(t, time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC), auto.CoverageEnd)
}

func TestHolderPoliciesReportsTheCoreUnavailable(t *testing.T) {
	p := portfolioOver(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusInternalServerError) })

	_, err := p.HolderPolicies(context.Background(), "sub-1")

	require.ErrorIs(t, err, ports.ErrPolicyPortfolioUnavailable)
}

func TestHolderKeepsWhoTheInsuredIsAndNotHowToReachThem(t *testing.T) {
	p := portfolioOver(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/holders/sub-1", r.URL.Path)
		_, _ = w.Write([]byte(holderProfile))
	})

	got, err := p.Holder(context.Background(), "sub-1")

	require.NoError(t, err)
	assert.Equal(t, domain.PolicyHolder{FullName: "Carolina Pérez Ruiz", DocumentType: "CC", DocumentID: "1032456789"}, got)
}

func TestHolderTheCoreDoesNotKnowIsUnavailable(t *testing.T) {
	p := portfolioOver(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNotFound) })

	_, err := p.Holder(context.Background(), "sub-1")

	require.ErrorIs(t, err, ports.ErrPolicyPortfolioUnavailable)
}
