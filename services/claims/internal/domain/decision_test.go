package domain_test

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/idempotency"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
)

// HU-W2.2-03: la decisión del analista sobre un siniestro en evaluación.

var decidido = time.Date(2026, 9, 16, 16, 40, 0, 0, time.FixedZone("COT", -5*3600))

var agua = domain.Coverage{Incident: domain.IncidentWaterDamage, InsuredValueMinor: 32_000_000_000, Currency: "COP",
	DeductibleBasisPoints: 500}

var carolina = domain.Holder{FullName: "Carolina Pérez Ruiz", Email: "carolina@correo.example",
	PayoutAccount: domain.PayoutAccount{BankCode: "1007", BankName: "Banco Aliado", AccountType: "savings",
		AccountNumber: "0123-4567-12"}}

func enEvaluacion() domain.ClaimRecord {
	return domain.ClaimRecord{Claim: domain.Claim{ID: "cl-1", FilingNumber: "SIN-2026-01655", Subject: "sub-carolina",
		IncidentType: domain.IncidentWaterDamage, Status: domain.StatusActive}}
}

func TestTheDeductibleIsDiscountedFromTheApprovedAmount(t *testing.T) {
	// El ejemplo de O6: $ 4.350.000 aprobados con 5 % de deducible.
	assert.EqualValues(t, 21_750_000, domain.DeductibleOf(435_000_000, 500))
	assert.EqualValues(t, 0, domain.DeductibleOf(435_000_000, 0))
	assert.EqualValues(t, 435_000_000, domain.DeductibleOf(435_000_000, 10_000))
	assert.EqualValues(t, 1, domain.DeductibleOf(15, 500), "rounded to the nearest cent: 0.75")
	assert.EqualValues(t, 0, domain.DeductibleOf(9, 500), "0.45 rounds down")
}

func TestAnApprovalPaysTheAmountLessTheDeductibleToTheAccountOfTheInsured(t *testing.T) {
	d, err := domain.Approve(enEvaluacion(), agua, carolina, 435_000_000, "analista", decidido)

	require.NoError(t, err)
	assert.Equal(t, domain.OutcomeApproved, d.Outcome)
	assert.Equal(t, "analista", d.DecidedBy)
	assert.Equal(t, time.UTC, d.DecidedAt.Location())
	assert.EqualValues(t, 21_750_000, d.DeductibleMinor())
	require.NotNil(t, d.Payout)
	assert.EqualValues(t, 413_250_000, d.Payout.AmountMinor)
	assert.Equal(t, "indemnizacion-cl-1", d.Payout.Key)
	assert.Equal(t, "sub-carolina", d.Payout.BeneficiaryID)
	assert.Equal(t, carolina.PayoutAccount, d.Payout.Destination)
	assert.Equal(t, idempotency.StatusPending, d.Payout.Status)
	assert.True(t, d.Payout.NextAttemptAt.Equal(decidido), "due at once")
	assert.Equal(t, domain.StatusApproved, d.StatusOf())
	assert.Nil(t, d.Notice)
}

func TestTheApprovedAmountIsBoundedByTheInsuredValue(t *testing.T) {
	_, err := domain.Approve(enEvaluacion(), agua, carolina, agua.InsuredValueMinor, "a", decidido)
	assert.NoError(t, err, "up to the insured value")

	for amount, want := range map[int64]error{
		0:                          domain.ErrAmountOutOfRange,
		-1:                         domain.ErrAmountOutOfRange,
		agua.InsuredValueMinor + 1: domain.ErrAmountExceedsInsuredValue,
	} {
		_, err := domain.Approve(enEvaluacion(), agua, carolina, amount, "a", decidido)
		assert.ErrorIs(t, err, want, amount)
	}
	total := agua
	total.DeductibleBasisPoints = 10_000
	_, err = domain.Approve(enEvaluacion(), total, carolina, 100, "a", decidido)
	assert.ErrorIs(t, err, domain.ErrNothingToPay)
	sinCuenta := carolina
	sinCuenta.PayoutAccount.AccountNumber = " "
	_, err = domain.Approve(enEvaluacion(), agua, sinCuenta, 100, "a", decidido)
	assert.ErrorIs(t, err, domain.ErrNoPayoutAccount)
}

func TestARejectionNeedsItsReasonAndAContact(t *testing.T) {
	d, err := domain.Reject(enEvaluacion(), "  Daño preexistente.  ", "carolina@correo.example", "analista", decidido)

	require.NoError(t, err)
	assert.Equal(t, domain.OutcomeRejected, d.Outcome)
	assert.Equal(t, "Daño preexistente.", d.Reason)
	assert.Equal(t, domain.StatusRejected, d.StatusOf())
	assert.Nil(t, d.Payout)
	require.NotNil(t, d.Notice)
	assert.Equal(t, "carolina@correo.example", d.Notice.To)

	for reason, want := range map[string]error{
		"":                        domain.ErrReasonRequired,
		"   ":                     domain.ErrReasonRequired,
		strings.Repeat("ñ", 1001): domain.ErrReasonTooLong,
	} {
		_, err := domain.Reject(enEvaluacion(), reason, "c@x", "a", decidido)
		assert.ErrorIs(t, err, want)
	}
	_, err = domain.Reject(enEvaluacion(), strings.Repeat("ñ", 1000), "c@x", "a", decidido)
	assert.NoError(t, err, "1000 characters, not bytes")
	_, err = domain.Reject(enEvaluacion(), "motivo", " ", "a", decidido)
	assert.ErrorIs(t, err, domain.ErrNoContact)
}

func TestTheStatusOfTheClaimFollowsItsPayout(t *testing.T) {
	d, err := domain.Approve(enEvaluacion(), agua, carolina, 100_000, "a", decidido)
	require.NoError(t, err)
	for status, want := range map[idempotency.Status]domain.Status{
		idempotency.StatusPending:    domain.StatusApproved,
		idempotency.StatusInProgress: domain.StatusApproved,
		idempotency.StatusConfirmed:  domain.StatusPaid,
		idempotency.StatusFailed:     domain.StatusPaymentException,
	} {
		d.Payout.Status = status
		assert.Equal(t, want, d.StatusOf(), status)
	}
}

func TestOnlyAClaimInEvaluationIsDecidable(t *testing.T) {
	assert.True(t, domain.Decidable(domain.StatusActive))
	assert.True(t, domain.Decidable(domain.StatusInEvaluation))
	for _, s := range []domain.Status{domain.StatusApproved, domain.StatusPaid, domain.StatusRejected,
		domain.StatusPaymentException} {
		assert.False(t, domain.Decidable(s), s)
		assert.False(t, domain.Triageable(s), "%s leaves the triage", s)
	}
}

func TestThePayoutIsAnAttemptOfTheSharedRunner(t *testing.T) {
	d, err := domain.Approve(enEvaluacion(), agua, carolina, 100_000, "a", decidido)
	require.NoError(t, err)
	d.Payout.Attempts = 2

	a := d.AttemptState()

	assert.Equal(t, "cl-1", a.ID)
	assert.Equal(t, "indemnizacion-cl-1", a.Key)
	assert.Equal(t, 2, a.FailedAttempts)
	assert.True(t, a.WindowEndsAt.IsZero(), "the attempts are bounded by count, not by time")
	assert.Equal(t, domain.PayoutRejected, domain.PayoutFailureFor(idempotency.ReasonRejected))
	assert.Equal(t, domain.PayoutRetriesExhausted, domain.PayoutFailureFor(idempotency.ReasonWindowExpired))
}

func TestTheKnownStatusesAreTheOnesOfTheJourney(t *testing.T) {
	for _, s := range []domain.Status{domain.StatusActive, domain.StatusInEvaluation, domain.StatusApproved,
		domain.StatusPaid, domain.StatusRejected, domain.StatusPaymentException} {
		assert.True(t, domain.Known(s), s)
	}
	assert.False(t, domain.Known("closed"))
	assert.False(t, domain.Known(""))
}
