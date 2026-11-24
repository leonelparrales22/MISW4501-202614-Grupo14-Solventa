package domain_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/idempotency"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
)

// HU-W2.1-02: what the insured reads of the stage of their claims.

func TestTheClaimsInProgressAreTheOnesNotClosed(t *testing.T) {
	tally := domain.ClaimTally{ByStatus: map[domain.Status]int{
		domain.StatusActive: 1, domain.StatusReturned: 2, domain.StatusInEvaluation: 3, domain.StatusApproved: 1,
		domain.StatusPaymentException: 1, domain.StatusPaid: 5, domain.StatusRejected: 4,
	}}

	assert.Equal(t, 8, tally.InProgress())
	assert.True(t, domain.Closed(domain.StatusPaid))
	assert.True(t, domain.Closed(domain.StatusRejected))
	assert.False(t, domain.Closed(domain.StatusPaymentException), "a payout that failed is retried")
	assert.Zero(t, domain.ClaimTally{}.InProgress())
}

func TestTheYearIsTheOneOfColombia(t *testing.T) {
	// 1 a.m. of January 1st in UTC is still December 31st in Bogotá.
	assert.Equal(t, time.Date(2025, 1, 1, 5, 0, 0, 0, time.UTC),
		domain.StartOfYear(time.Date(2026, 1, 1, 1, 0, 0, 0, time.UTC)))
	assert.Equal(t, time.Date(2026, 1, 1, 5, 0, 0, 0, time.UTC),
		domain.StartOfYear(time.Date(2026, 11, 12, 15, 0, 0, 0, time.UTC)))
}

func TestTheInsuredReadsThePayoutButNotTheAccount(t *testing.T) {
	decided := time.Date(2026, 9, 18, 15, 0, 0, 0, time.FixedZone("UTC-5", -5*60*60))
	paid := decided.Add(time.Hour)
	d := domain.Decision{Outcome: domain.OutcomeApproved, DecidedBy: "analista@solventa.example", DecidedAt: decided,
		GrossAmountMinor: 500_000_000, Currency: "COP",
		Payout: &domain.Payout{AmountMinor: 435_000_000, Currency: "COP", Status: idempotency.StatusConfirmed,
			ConfirmedAt: paid, Destination: domain.PayoutAccount{AccountNumber: "0012345678"}}}

	v := domain.ViewOf(d)

	assert.Equal(t, domain.OutcomeApproved, v.Outcome)
	assert.Equal(t, decided.UTC(), v.DecidedAt)
	assert.Equal(t, int64(435_000_000), v.AmountMinor, "the payout, net of the deductible")
	assert.Equal(t, "COP", v.Currency)
	assert.Equal(t, idempotency.StatusConfirmed, v.PayoutStatus)
	require.NotNil(t, v.PaidAt)
	assert.Equal(t, paid.UTC(), *v.PaidAt)

	d.Payout.Status, d.Payout.Currency = idempotency.StatusPending, ""
	pending := domain.ViewOf(d)
	assert.Nil(t, pending.PaidAt, "not paid yet")
	assert.Equal(t, "COP", pending.Currency, "the currency of the approval")
}

func TestARejectionIsReadWithItsReason(t *testing.T) {
	v := domain.ViewOf(domain.Decision{Outcome: domain.OutcomeRejected, DecidedAt: time.Now(),
		Reason: "El daño no está cubierto."})

	assert.Equal(t, domain.OutcomeRejected, v.Outcome)
	assert.Equal(t, "El daño no está cubierto.", v.Reason)
	assert.Zero(t, v.AmountMinor)
	assert.Empty(t, v.PayoutStatus)
	assert.Nil(t, v.PaidAt)
}

func TestTheInsuredReadsWhoAttendsButNotHowToReachThem(t *testing.T) {
	phone := "+576015550102"
	at := time.Date(2026, 9, 5, 15, 2, 0, 0, time.UTC)
	v := domain.ProviderViewOf(domain.Assignment{ProviderName: "Servicios Hogar Norte", DistanceKm: 2.3, AssignedAt: at,
		ContactEmail: "despachos@hogarnorte.example.com", ContactPhone: &phone})

	assert.Equal(t, domain.ProviderView{Name: "Servicios Hogar Norte", DistanceKm: 2.3, AssignedAt: at}, v)
}
