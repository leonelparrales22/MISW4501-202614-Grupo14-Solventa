package domain_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
)

func TestWalletPolicyOfAnIssuedPolicyIsALifePolicyWithItsPremium(t *testing.T) {
	end := time.Date(2046, 9, 16, 0, 0, 0, 0, time.UTC)
	p := domain.Policy{ID: "pol-1", Holder: domain.Applicant{FullName: "Carolina Pérez", DocumentID: "1032456789", Email: "c@example.com"},
		Premium: 68_500, Currency: "COP", InsuredAmount: 250_000_000,
		Coverages:     []domain.Coverage{{Code: domain.CoverageDeath, InsuredAmount: 250_000_000}},
		CoverageStart: end.AddDate(-20, 0, 0), CoverageEnd: end, RenewalOf: "pol-0"}

	got := domain.WalletPolicyOf(p, end.AddDate(-1, 0, 0))

	assert.Equal(t, domain.SourceOrigination, got.Source)
	assert.Equal(t, domain.LineLife, got.Line)
	assert.Equal(t, domain.PolicyActive, got.Status)
	require.NotNil(t, got.Premium)
	assert.Equal(t, 68_500.0, *got.Premium)
	assert.Equal(t, domain.PolicyHolder{FullName: "Carolina Pérez", DocumentID: "1032456789"}, got.Holder)
	assert.Equal(t, "pol-0", got.RenewalOf)
	got.Coverages[0].InsuredAmount = 1
	assert.Equal(t, 250_000_000.0, p.Coverages[0].InsuredAmount, "the wallet copies the coverages")
}

func TestAnIssuedPolicyExpiresWhenItsCoverageEnds(t *testing.T) {
	end := time.Date(2026, 10, 2, 23, 59, 0, 0, time.UTC)
	p := domain.Policy{CoverageEnd: end}

	assert.Equal(t, domain.PolicyActive, p.StatusAt(end))
	assert.Equal(t, domain.PolicyExpired, p.StatusAt(end.Add(time.Second)))
}
