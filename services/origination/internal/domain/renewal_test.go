package domain_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
)

var policyEnd = time.Date(2026, 10, 2, 23, 59, 0, 0, time.UTC)

func samplePolicy() domain.Policy {
	return domain.Policy{
		ID: "pol-1", AcceptanceID: "acc-1", CustomerID: "sub-1",
		Holder:  domain.Applicant{FullName: "Carolina Pérez", DocumentID: "1032456789", Email: "carolina@example.com"},
		Premium: 142_300, Currency: "COP", InsuredAmount: 68_000_000,
		Coverages:     []domain.Coverage{{Code: domain.CoverageDeath, InsuredAmount: 68_000_000}},
		CoverageStart: policyEnd.AddDate(-1, 0, 0), CoverageEnd: policyEnd,
	}
}

func TestRenewalWindowOpensBeforeTheEndAndClosesWithIt(t *testing.T) {
	rules := domain.DefaultRenewalPolicy
	p := samplePolicy()

	assert.Equal(t, policyEnd.AddDate(0, 0, -30), rules.WindowOpensAt(p))
	assert.False(t, rules.InWindow(p, policyEnd.AddDate(0, 0, -31)), "before the window opens")
	assert.True(t, rules.InWindow(p, policyEnd.AddDate(0, 0, -30)), "the window opens at its start")
	assert.True(t, rules.InWindow(p, policyEnd.Add(-time.Second)))
	assert.False(t, rules.InWindow(p, policyEnd), "the window closes when the coverage ends")
	assert.False(t, rules.InWindow(p, policyEnd.Add(time.Hour)))
}

func TestRenewalPolicyValidate(t *testing.T) {
	require.NoError(t, domain.DefaultRenewalPolicy.Validate())
	for name, mutate := range map[string]func(*domain.RenewalPolicy){
		"window":  func(p *domain.RenewalPolicy) { p.WindowDays = 0 },
		"term":    func(p *domain.RenewalPolicy) { p.TermMonths = 0 },
		"retry":   func(p *domain.RenewalPolicy) { p.RetryWithin = 0 },
		"grace":   func(p *domain.RenewalPolicy) { p.GraceDays = -1 },
		"too big": func(p *domain.RenewalPolicy) { p.WindowDays = 366 },
	} {
		p := domain.DefaultRenewalPolicy
		mutate(&p)
		assert.Error(t, p.Validate(), name)
	}
}

func TestRenewalIDIsStablePerPolicyAndPeriod(t *testing.T) {
	id := domain.RenewalID("pol-1", policyEnd)
	assert.Equal(t, "ren-pol-1-20261002", id)
	assert.Equal(t, id, domain.RenewalID("pol-1", policyEnd.In(time.FixedZone("x", -5*3600))))
	assert.True(t, domain.IsRenewalCharge(domain.RenewalChargeID(id)))
	assert.False(t, domain.IsRenewalCharge("acc-1"))
}

func TestPolicyFromAnActivationKeepsWhatThePortalShows(t *testing.T) {
	e := domain.CoverageActivated{
		PolicyID: "pol-2", AcceptanceID: "ren-pol-1-20261002", CustomerID: "sub-1", Holder: samplePolicy().Holder,
		Premium: 149_800, Currency: "COP", InsuredAmount: 72_500_000, Coverages: samplePolicy().Coverages,
		CoverageStart: policyEnd, CoverageEnd: policyEnd.AddDate(1, 0, 0), RenewalOf: "pol-1",
	}
	p := domain.PolicyFrom(e)
	assert.Equal(t, "pol-2", p.ID)
	assert.Equal(t, "pol-1", p.RenewalOf)
	assert.Equal(t, e.CoverageEnd, p.CoverageEnd)
}

func TestTheChargeOfARenewalStartsItsCoverageWhenTheOldOneEnds(t *testing.T) {
	p := samplePolicy()
	paidAt := policyEnd.AddDate(0, 0, -10)
	r := domain.Renewal{
		ID: domain.RenewalID(p.ID, p.CoverageEnd), PolicyID: p.ID, CustomerID: p.CustomerID, PeriodEnd: p.CoverageEnd,
		Status: domain.RenewalAccepted, Premium: 149_800, Currency: "COP",
		Terms:    domain.CoverageTerms{InsuredAmount: 72_500_000, TermMonths: 12, Coverages: p.Coverages},
		ChargeID: "ren-pol-1-20261002", AcceptedAt: paidAt, RetryUntil: paidAt.Add(72 * time.Hour),
	}
	c := domain.NewRenewalCharge(p, r, paidAt)

	assert.Equal(t, domain.ChannelRenewal, c.Channel)
	assert.Equal(t, int64(14_980_000), c.AmountMinor)
	assert.Equal(t, r.RetryUntil, c.WindowEndsAt, "the charge window is the deadline to fix the payment")
	assert.True(t, c.NextAttemptAt.IsZero(), "it is not due until a payment method is attached")

	confirmed := c.Confirmed(paidAt.Add(time.Minute), "ref-1")
	assert.Equal(t, policyEnd, confirmed.CoverageStart, "no gap and no overlap with the renewed policy")
	assert.Equal(t, policyEnd.AddDate(1, 0, 0), confirmed.CoverageEnd)
	assert.Equal(t, "pol-1", confirmed.ActivationEvent().RenewalOf)
	require.NoError(t, c.ValidateActivation(paidAt))
}

func TestAChargeConfirmedAfterTheOldCoverageEndedStartsAtOnce(t *testing.T) {
	p := samplePolicy()
	r := domain.Renewal{ChargeID: "ren-x", Premium: 100, Currency: "COP", AcceptedAt: policyEnd,
		Terms: domain.CoverageTerms{InsuredAmount: 1, TermMonths: 12, Coverages: p.Coverages}}
	late := policyEnd.Add(2 * time.Hour)

	confirmed := domain.NewRenewalCharge(p, r, late).Confirmed(late, "ref")

	assert.Equal(t, late, confirmed.CoverageStart)
}

func TestRenewalValidateRequiresPolicyCustomerAndPeriod(t *testing.T) {
	assert.Error(t, domain.Renewal{ID: "ren-1"}.Validate())
	assert.NoError(t, domain.Renewal{ID: "ren-1", PolicyID: "pol-1", CustomerID: "sub-1", PeriodEnd: policyEnd}.Validate())
}

func TestTheIssuanceFilterAcceptsTheRenewalChannel(t *testing.T) {
	assert.NoError(t, domain.IssuanceFilter{Channel: domain.ChannelRenewal}.Validate())
	assert.Error(t, domain.IssuanceFilter{Channel: "fax"}.Validate())
}
