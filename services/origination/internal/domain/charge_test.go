package domain_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/idempotency"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
)

var chargeT0 = time.Date(2026, 10, 16, 15, 4, 5, 0, time.UTC)

func demoTerms() domain.CoverageTerms {
	return domain.CoverageTerms{InsuredAmount: 250_000_000, TermMonths: 120, Coverages: []domain.Coverage{
		{Code: domain.CoverageDeath, InsuredAmount: 250_000_000},
		{Code: domain.CoverageDisability, InsuredAmount: 250_000_000},
	}}
}

func pendingCharge() domain.Charge {
	acc := domain.Acceptance{ID: "acc-1", OfferID: "demo-active-001", CustomerID: "cust-1", Channel: domain.ChannelWeb,
		AcceptedAt: chargeT0.Add(-time.Minute)}
	offer := domain.Offer{ID: "demo-active-001", CustomerID: "cust-1", Premium: 89_500, Currency: "COP",
		Applicant: domain.Applicant{FullName: "Ana Pérez", DocumentID: "demo-document", Email: "ana@example.com"}}
	return domain.NewCharge(acc, offer, demoTerms(), chargeT0, chargeT0.Add(24*time.Hour))
}

func TestNewChargeKeepsTheSnapshotAndTheDeterministicKey(t *testing.T) {
	c := pendingCharge()

	assert.Equal(t, "acc-1", c.AcceptanceID)
	assert.Equal(t, "cobro-acc-1", c.IdempotencyKey, "ADR-01: cobro- + acceptance id")
	assert.Equal(t, "demo-active-001", c.OfferID)
	assert.Equal(t, "cust-1", c.CustomerID)
	assert.Equal(t, domain.ChannelWeb, c.Channel)
	assert.EqualValues(t, 8_950_000, c.AmountMinor, "89 500 COP in cents")
	assert.Equal(t, "COP", c.Currency)
	assert.Equal(t, "ana@example.com", c.Holder.Email)
	assert.Equal(t, domain.ChargePending, c.Status)
	// HU-W1.3-02: it is not due until the customer hands over a payment
	// method. The charge is still persisted the moment the acceptance is
	// approved, so the no-loss point of HA-D02 does not move.
	assert.True(t, c.NextAttemptAt.IsZero(), "not due until there is a payment method")
	assert.Equal(t, chargeT0, c.WindowStartedAt)
	assert.Equal(t, chargeT0.Add(24*time.Hour), c.WindowEndsAt)
	assert.Empty(t, c.PolicyID, "no policy before the payment")
	assert.True(t, c.ActivatedAt.IsZero())
}

func TestTheSnapshotDoesNotShareTheCoveragesOfTheCaller(t *testing.T) {
	terms := demoTerms()
	c := domain.NewCharge(domain.Acceptance{ID: "acc-1"}, domain.Offer{Premium: 1}, terms, chargeT0, time.Time{})
	terms.Coverages[0].Code = "changed"
	assert.Equal(t, domain.CoverageDeath, c.Terms.Coverages[0].Code)
}

func TestConfirmedSetsThePolicyAndTheCoveragePeriod(t *testing.T) {
	at := chargeT0.Add(3 * time.Minute)

	c := pendingCharge().Confirmed(at, "ch-7")

	assert.Equal(t, domain.ChargeConfirmed, c.Status)
	assert.Equal(t, at, c.ConfirmedAt)
	assert.Equal(t, "ch-7", c.ProviderReference)
	assert.Equal(t, "pol-acc-1", c.PolicyID)
	assert.Equal(t, "evt-coverage-activated-acc-1", c.ActivationEventID)
	assert.Equal(t, at, c.ActivatedAt)
	assert.Equal(t, at, c.CoverageStart)
	assert.Equal(t, at.AddDate(0, 120, 0), c.CoverageEnd)
	assert.Equal(t, at, c.ActivationNextAttemptAt, "the publication is due at once")
	assert.True(t, c.NextAttemptAt.IsZero())
	assert.False(t, c.ActivationPublished())
}

func TestTheActivationEventIsValidAndAlwaysTheSame(t *testing.T) {
	c := pendingCharge().Confirmed(chargeT0.Add(time.Minute), "ch-1")

	ev := c.ActivationEvent()
	again := c.ActivationEvent()

	require.NoError(t, ev.Validate())
	assert.Equal(t, ev, again, "every publication is the same event")
	assert.Equal(t, "evt-coverage-activated-acc-1", ev.EventID)
	assert.Equal(t, "pol-acc-1", ev.PolicyID)
	assert.Equal(t, "acc-1", ev.AcceptanceID)
	assert.Equal(t, "cust-1", ev.CustomerID)
	assert.Equal(t, "Ana Pérez", ev.Holder.FullName)
	assert.InDelta(t, 89_500, ev.Premium, 0.001)
	assert.Equal(t, "COP", ev.Currency)
	assert.InDelta(t, 250_000_000, ev.InsuredAmount, 0.001)
	assert.Len(t, ev.Coverages, 2)
	assert.Equal(t, c.ActivatedAt.AddDate(0, 120, 0), ev.CoverageEnd)

	ev.Coverages[0].Code = "tampered"
	assert.Equal(t, domain.CoverageDeath, c.ActivationEvent().Coverages[0].Code, "the event does not share the snapshot")
}

func TestValidateActivationRejectsASnapshotThatCannotActivate(t *testing.T) {
	ok := pendingCharge()
	assert.NoError(t, ok.ValidateActivation(chargeT0))

	noEmail := pendingCharge()
	noEmail.Holder.Email = ""
	assert.Error(t, noEmail.ValidateActivation(chargeT0), "the notification needs the e-mail")

	noCoverages := pendingCharge()
	noCoverages.Terms.Coverages = nil
	assert.Error(t, noCoverages.ValidateActivation(chargeT0))

	noTerm := pendingCharge()
	noTerm.Terms.TermMonths = 0
	assert.Error(t, noTerm.ValidateActivation(chargeT0), "a coverage period is required")

	noPremium := pendingCharge()
	noPremium.AmountMinor = 0
	assert.Error(t, noPremium.ValidateActivation(chargeT0))
}

func TestMinorUnits(t *testing.T) {
	assert.EqualValues(t, 8_950_000, domain.ToMinorUnits(89_500))
	assert.EqualValues(t, 1_999, domain.ToMinorUnits(19.99))
	assert.InDelta(t, 89_500, domain.FromMinorUnits(8_950_000), 0.0001)
	assert.InDelta(t, 19.99, domain.FromMinorUnits(1_999), 0.0001)
}

func TestAttemptStateAndFailureReasons(t *testing.T) {
	c := pendingCharge()
	c.Attempts, c.ClaimedUntil = 3, chargeT0.Add(30*time.Second)

	st := c.AttemptState()

	assert.Equal(t, idempotency.Attempt{
		ID: "acc-1", Key: "cobro-acc-1", Status: idempotency.StatusPending, FailedAttempts: 3,
		WindowEndsAt: c.WindowEndsAt, NextAttemptAt: c.NextAttemptAt, ClaimedUntil: c.ClaimedUntil,
	}, st)
	assert.Equal(t, domain.ChargePaymentRejected, domain.ChargeFailureReasonFor(idempotency.ReasonRejected))
	assert.Equal(t, domain.ChargeRetryWindowExpired, domain.ChargeFailureReasonFor(idempotency.ReasonWindowExpired))
	assert.Equal(t, "pol-x", domain.PolicyIDFor("x"))
	assert.Equal(t, "evt-coverage-activated-x", domain.ActivationEventIDFor("x"))
}

// --- Payment method (HU-W1.3-02) -------------------------------------------

func chargeAwaitingCard(t *testing.T) domain.Charge {
	t.Helper()
	return domain.Charge{
		AcceptanceID:   "acc-1",
		IdempotencyKey: "cobro-acc-1",
		Status:         domain.ChargePending,
	}
}

func card() domain.PaymentMethod {
	return domain.PaymentMethod{
		Kind: domain.PaymentCard, Token: "tok_7b3c1f9a",
		Brand: "visa", Last4: "1111", Installments: 1,
	}
}

// The charge is stored the moment the acceptance is approved -the no-loss
// point does not move- but it waits for the card before it is charged.
func TestANewChargeIsNotDueUntilThereIsAPaymentMethod(t *testing.T) {
	now := time.Date(2026, 10, 17, 14, 0, 0, 0, time.UTC)

	c := domain.NewCharge(
		domain.Acceptance{ID: "acc-1", AcceptedAt: now},
		domain.Offer{ID: "offer-1", CustomerID: "cust-1", Premium: 89500, Currency: "COP"},
		domain.CoverageTerms{}, now, now.Add(24*time.Hour))

	assert.Equal(t, domain.ChargePending, c.Status)
	assert.True(t, c.NextAttemptAt.IsZero(), "it is not due: ListDue skips a charge with no due date")
	assert.False(t, c.PaymentMethod.Captured())
	assert.Zero(t, c.PaymentAttempt)
	assert.Equal(t, now, c.WindowStartedAt, "the 24-hour window runs from the acceptance")
}

func TestThePaymentMethodMakesTheChargeDue(t *testing.T) {
	now := time.Date(2026, 10, 17, 14, 5, 0, 0, time.UTC)

	c := chargeAwaitingCard(t).WithPaymentMethod(card(), now)

	assert.Equal(t, now, c.NextAttemptAt)
	assert.Equal(t, domain.ChargePending, c.Status)
	assert.Equal(t, 1, c.PaymentAttempt)
	assert.Equal(t, "1111", c.PaymentMethod.Last4)
	assert.Equal(t, now, c.PaymentMethod.CapturedAt)
	assert.Equal(t, "cobro-acc-1", c.IdempotencyKey, "the first attempt keeps the key of HA-D02")
}

// This is screen E4c: a decline the customer can fix without waiting on an
// operations analyst.
func TestADeclinedChargeTakesAnotherCardWithANewKey(t *testing.T) {
	now := time.Date(2026, 10, 17, 14, 10, 0, 0, time.UTC)
	declined := chargeAwaitingCard(t)
	declined.Status = domain.ChargeFailed
	declined.FailureReason = domain.ChargePaymentRejected
	declined.PaymentAttempt, declined.Attempts = 1, 1
	declined.LastError = "the issuer declined the charge"

	c := declined.WithPaymentMethod(card(), now)

	assert.True(t, declined.TakesPaymentMethod(), "a declined charge does take another card")
	assert.Equal(t, domain.ChargePending, c.Status)
	assert.Equal(t, "cobro-acc-1-2", c.IdempotencyKey, "a new card needs a new key or the gateway replays the decline")
	assert.Equal(t, 2, c.PaymentAttempt)
	assert.Zero(t, c.Attempts)
	assert.Empty(t, c.FailureReason)
	assert.Empty(t, c.LastError)
}

// The retry window is not reopened by a decline: it has been running since
// the acceptance, which is what the team decided.
func TestADeclineDoesNotBuyMoreTime(t *testing.T) {
	started := time.Date(2026, 10, 17, 14, 0, 0, 0, time.UTC)
	declined := chargeAwaitingCard(t)
	declined.Status, declined.FailureReason = domain.ChargeFailed, domain.ChargePaymentRejected
	declined.WindowStartedAt, declined.WindowEndsAt = started, started.Add(24*time.Hour)

	c := declined.WithPaymentMethod(card(), started.Add(time.Hour))

	assert.Equal(t, started, c.WindowStartedAt)
	assert.Equal(t, started.Add(24*time.Hour), c.WindowEndsAt)
}

// A charge whose window ran out never got an answer from the gateway, so
// another card could end up charging twice. Only operations may resume it.
func TestAChargeThatRanOutOfWindowTakesNoOtherCard(t *testing.T) {
	c := chargeAwaitingCard(t)
	c.Status, c.FailureReason = domain.ChargeFailed, domain.ChargeRetryWindowExpired

	assert.False(t, c.TakesPaymentMethod())
}

func TestAConfirmedOrRunningChargeTakesNoPaymentMethod(t *testing.T) {
	for _, status := range []domain.ChargeStatus{domain.ChargeConfirmed, domain.ChargeInProgress} {
		c := chargeAwaitingCard(t)
		c.Status = status
		assert.False(t, c.TakesPaymentMethod(), "status %s", status)
	}
}

// Telling the two apart is what keeps the operations tray honest: nobody
// should go looking for a gateway problem that never happened.
func TestTheWindowFailureSaysWhetherThereWasEverACard(t *testing.T) {
	withoutCard := chargeAwaitingCard(t)
	withCard := chargeAwaitingCard(t).WithPaymentMethod(card(), time.Now())

	assert.Equal(t, domain.ChargePaymentMethodMissing, withoutCard.FailureReasonForWindow())
	assert.Equal(t, domain.ChargeRetryWindowExpired, withCard.FailureReasonForWindow())
}

// A charge the gateway may already have taken is ambiguous: another card
// would need another key, and another key on an ambiguous result is how a
// customer gets charged twice.
func TestAPendingChargeWithAttemptsTakesNoOtherCard(t *testing.T) {
	c := chargeAwaitingCard(t).WithPaymentMethod(card(), time.Now())
	c.Attempts = 1

	assert.False(t, c.TakesPaymentMethod())
}

func TestAPendingChargeWithNoAttemptTakesAnotherCard(t *testing.T) {
	c := chargeAwaitingCard(t).WithPaymentMethod(card(), time.Now())

	assert.True(t, c.TakesPaymentMethod(), "nothing was sent yet, so the card can still change")
}
