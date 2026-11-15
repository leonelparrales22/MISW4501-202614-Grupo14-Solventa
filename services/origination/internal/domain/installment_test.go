package domain_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
)

// HU-W1.6-02: the calendar of the instalments, the grace period and the
// cancellation for non-payment.

func colombia(y int, m time.Month, d, h, min int) time.Time {
	return time.Date(y, m, d, h, min, 0, 0, domain.ColombiaTime)
}

func lifePolicy(start time.Time, months int) domain.Policy {
	return domain.Policy{ID: "VH-2026-004512", AcceptanceID: "acc-1", CustomerID: "sub-carolina", Premium: 68_500,
		Currency: "COP", CoverageStart: start.UTC(), CoverageEnd: start.AddDate(0, months, 0).UTC()}
}

func TestEachMonthFallsDueOnTheDayTheCoverageStarted(t *testing.T) {
	r := domain.DefaultInstallmentRules
	p := lifePolicy(colombia(2026, 9, 16, 14, 6), 12)

	assert.Equal(t, colombia(2026, 9, 16, 14, 6).UTC(), r.DueAt(p, 1), "the first month is the issuance")
	assert.Equal(t, colombia(2026, 10, 16, 14, 6).UTC(), r.DueAt(p, 2), "A2: issued on 16/09, next charge on 16/10")
	assert.Equal(t, colombia(2027, 1, 16, 14, 6).UTC(), r.DueAt(p, 5), "across the end of the year")
	assert.Equal(t, 12, r.Months(p))
}

func TestAMonthWithoutTheDayFallsDueOnItsLastDay(t *testing.T) {
	r := domain.DefaultInstallmentRules
	p := lifePolicy(colombia(2026, 1, 31, 9, 0), 12)

	assert.Equal(t, colombia(2026, 2, 28, 9, 0).UTC(), r.DueAt(p, 2), "February has no 31st")
	assert.Equal(t, colombia(2026, 3, 31, 9, 0).UTC(), r.DueAt(p, 3), "March has it again")
	assert.Equal(t, colombia(2026, 4, 30, 9, 0).UTC(), r.DueAt(p, 4))
}

func TestTheGraceTheAttemptsAndTheCancellationCountFromTheDueDate(t *testing.T) {
	r := domain.DefaultInstallmentRules
	due := colombia(2026, 8, 12, 0, 0)

	assert.Equal(t, colombia(2026, 9, 11, 0, 0).UTC(), r.GraceEndsAt(due), "30 days of grace: 13/08 to 11/09")
	assert.Equal(t, colombia(2026, 9, 12, 0, 0).UTC(), r.CancelsAt(due), "cancelled on day 31")
	var attempts []time.Time
	for n := 1; n <= r.MaxAttempts(); n++ {
		at, ok := r.AttemptAt(due, n)
		require.True(t, ok)
		attempts = append(attempts, at)
	}
	assert.Equal(t, []time.Time{colombia(2026, 8, 13, 0, 0).UTC(), colombia(2026, 8, 20, 0, 0).UTC(), colombia(2026, 8, 27, 0, 0).UTC()},
		attempts, "A2b: 13/08, 20/08 and 27/08")
	_, ok := r.AttemptAt(due, 4)
	assert.False(t, ok, "three attempts and no more")
}

func TestADemoDayShortensTheWholeCalendar(t *testing.T) {
	r := domain.DefaultInstallmentRules
	r.Day = time.Minute
	start := time.Date(2026, 11, 15, 10, 0, 0, 0, time.UTC)
	p := lifePolicy(start, 12)

	assert.Equal(t, start.Add(30*time.Minute), r.DueAt(p, 2), "a month is 30 days of a minute")
	assert.Equal(t, start.Add(61*time.Minute), r.CancelsAt(r.DueAt(p, 2)))
	at, _ := r.AttemptAt(r.DueAt(p, 2), 3)
	assert.Equal(t, start.Add(45*time.Minute), at)
	require.NoError(t, r.Validate())
}

func TestOnlyTheMonthsDueSinceTheBillingStartedAreBilled(t *testing.T) {
	r := domain.DefaultInstallmentRules
	p := lifePolicy(colombia(2026, 5, 10, 8, 0), 12)

	got := r.DueInstallments(p, colombia(2026, 7, 1, 0, 0), colombia(2026, 9, 10, 8, 0))

	assert.Equal(t, []int{3, 4, 5}, got, "June fell due before the billing started; September is due on the dot")
	assert.Empty(t, r.DueInstallments(p, colombia(2026, 7, 1, 0, 0), colombia(2026, 7, 9, 0, 0)))
	assert.Empty(t, r.DueInstallments(lifePolicy(colombia(2026, 5, 10, 8, 0), 1), time.Time{}, colombia(2027, 1, 1, 0, 0)),
		"a policy of one month has only the issuance")
}

func TestEveryAttemptHasItsOwnKey(t *testing.T) {
	r := domain.DefaultInstallmentRules
	p := lifePolicy(colombia(2026, 9, 16, 14, 6), 12)
	i := domain.NewInstallment(p, 2, r, colombia(2026, 10, 16, 15, 0))

	assert.Equal(t, "cuota-VH-2026-004512-2-1", i.IdempotencyKey())
	assert.Equal(t, int64(6_850_000), i.AmountMinor)
	assert.Equal(t, domain.InstallmentPending, i.Status)
	first, _ := r.AttemptAt(i.DueAt, 1)
	assert.Equal(t, first, i.NextAttemptAt)
	i.Attempts = 1
	assert.Equal(t, "cuota-VH-2026-004512-2-2", i.IdempotencyKey(), "a declined key is never reused")
	next, ok := i.AfterDecline(r)
	require.True(t, ok)
	third, _ := r.AttemptAt(i.DueAt, 3)
	assert.Equal(t, third, next)
	i.Attempts = 2
	_, ok = i.AfterDecline(r)
	assert.False(t, ok, "after the third decline the policy waits for its cancellation")
}

func TestTheBillingSaysWhereThePremiumStands(t *testing.T) {
	r := domain.DefaultInstallmentRules
	p := lifePolicy(colombia(2026, 9, 16, 14, 6), 12)
	now := colombia(2026, 9, 20, 0, 0)

	upToDate := domain.BillingOf(p, r, nil, nil, now)
	assert.Equal(t, domain.PaymentUpToDate, upToDate.Status)
	assert.Equal(t, colombia(2026, 10, 16, 14, 6).UTC(), upToDate.NextDueAt)

	pending := domain.NewInstallment(p, 2, r, now)
	b := domain.BillingOf(p, r, []domain.Installment{pending}, nil, colombia(2026, 10, 20, 0, 0))
	assert.Equal(t, domain.PaymentPending, b.Status)
	require.NotNil(t, b.Pending)
	assert.Equal(t, 2, b.Pending.Seq)

	cancelled := domain.BillingOf(p, r, []domain.Installment{pending},
		&domain.PolicyCancellation{PolicyID: p.ID, Reason: domain.CancellationNonPayment}, colombia(2026, 11, 20, 0, 0))
	assert.Equal(t, domain.PaymentCancelled, cancelled.Status)
}

func TestTheRulesMustBeApplicable(t *testing.T) {
	ok := domain.DefaultInstallmentRules
	require.NoError(t, ok.Validate())
	for name, r := range map[string]domain.InstallmentRules{
		"no grace":           {GraceDays: 0, AttemptDays: []int{1}, Day: time.Hour, RetryUnknownAfter: time.Minute},
		"attempt after":      {GraceDays: 10, AttemptDays: []int{1, 11}, Day: time.Hour, RetryUnknownAfter: time.Minute},
		"attempts unordered": {GraceDays: 30, AttemptDays: []int{8, 1}, Day: time.Hour, RetryUnknownAfter: time.Minute},
		"day too long":       {GraceDays: 30, AttemptDays: []int{1}, Day: 48 * time.Hour, RetryUnknownAfter: time.Minute},
		"no attempts":        {GraceDays: 30, Day: time.Hour, RetryUnknownAfter: time.Minute},
		"no unknown retry":   {GraceDays: 30, AttemptDays: []int{1}, Day: time.Hour},
	} {
		assert.ErrorIs(t, r.Validate(), domain.ErrInvalidInstallmentRules, name)
	}
}

func TestTheCancellationEventCarriesNoRefund(t *testing.T) {
	r := domain.DefaultInstallmentRules
	p := lifePolicy(colombia(2026, 7, 2, 10, 0), 12)
	i := domain.NewInstallment(p, 2, r, colombia(2026, 8, 2, 10, 0))
	e := domain.NewPolicyCancelledForNonPayment(p, i, r.CancelsAt(i.DueAt))

	env, err := e.Envelope()

	require.NoError(t, err)
	assert.Equal(t, "policy-cancelled-non-payment-VH-2026-004512", env.EventID)
	assert.Equal(t, domain.EventTypePolicyCancelledForNonPayment, env.EventType)
	var payload map[string]any
	require.NoError(t, env.DecodePayload(&payload))
	assert.Equal(t, "not_applicable", payload["refund"])
	assert.Equal(t, "non_payment", payload["reason"])
	assert.Equal(t, "sub-carolina", payload["subject"])
	assert.EqualValues(t, 2, payload["installment_seq"])

	e.CancelledAt = e.DueAt
	_, err = e.Envelope()
	assert.ErrorIs(t, err, domain.ErrInvalidCancellation, "a policy is cancelled after the month fell due")
}
