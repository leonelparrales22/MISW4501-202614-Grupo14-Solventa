package postgres_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/events"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/postgres"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
)

// HU-W1.6-02: the monthly instalments and the cancellation for non-payment,
// in PostgreSQL.

var (
	rules     = domain.DefaultInstallmentRules
	billedAt  = time.Date(2026, 10, 16, 20, 0, 0, 0, time.UTC)
	issuedAt  = time.Date(2026, 9, 16, 19, 6, 0, 0, time.UTC)
	policyEnd = issuedAt.AddDate(1, 0, 0)
)

// billedPolicy stores a policy issued on 16/09 and its second month, due on
// 16/10.
func billedPolicy(t *testing.T, db *sql.DB, id string) (*postgres.InstallmentStore, domain.Policy, domain.Installment) {
	t.Helper()
	ctx := context.Background()
	p := samplePolicy(id, "sub-carolina", policyEnd)
	p.CoverageStart = issuedAt
	require.NoError(t, postgres.NewPolicyStore(db).Create(ctx, p))
	s := postgres.NewInstallmentStore(db)
	i := domain.NewInstallment(p, 2, rules, billedAt)
	created, err := s.CreateInstallment(ctx, i)
	require.NoError(t, err)
	require.True(t, created)
	return s, p, i
}

// hold claims the instalments due at the time.
func hold(t *testing.T, s *postgres.InstallmentStore, at time.Time) []ports.PolicyInstallment {
	t.Helper()
	got, err := s.ClaimDueAttempts(context.Background(), at, time.Minute, 10)
	require.NoError(t, err)
	return got
}

func declineAll(t *testing.T, s *postgres.InstallmentStore, i domain.Installment) domain.Installment {
	t.Helper()
	for n := 1; n <= rules.MaxAttempts(); n++ {
		at, _ := rules.AttemptAt(i.DueAt, n)
		held := hold(t, s, at)
		require.Len(t, held, 1, "attempt %d", n)
		i = held[0].Installment
		next, _ := i.AfterDecline(rules)
		require.NoError(t, s.RecordDeclined(context.Background(), i, domain.InstallmentAttempt{Attempt: n,
			IdempotencyKey: i.IdempotencyKey(), Outcome: domain.AttemptDeclined, DeclineCode: "51", AttemptedAt: at}, next))
	}
	rec, err := s.Billing(context.Background(), []string{i.PolicyID})
	require.NoError(t, err)
	return rec[i.PolicyID].Installments[0]
}

func TestTheInstalmentsAreBilledFromWhenTheMigrationRan(t *testing.T) {
	requireOfferDocker(t)
	db := newDB(t)

	since, err := postgres.NewInstallmentStore(db).BillingStart(context.Background())

	require.NoError(t, err)
	assert.WithinDuration(t, time.Now(), since, time.Minute)
}

func TestAMonthIsBilledOnceAndHeldByOneTaskAtATime(t *testing.T) {
	requireOfferDocker(t)
	db := newDB(t)
	s, p, i := billedPolicy(t, db, "VH-2026-004512")
	ctx := context.Background()

	again, err := s.CreateInstallment(ctx, i)
	require.NoError(t, err)
	assert.False(t, again, "the same month is billed once")
	assert.Empty(t, hold(t, s, i.DueAt), "the first attempt is on day 1 of the grace")

	first, _ := rules.AttemptAt(i.DueAt, 1)
	var wg sync.WaitGroup
	results := make([]int, 4)
	for k := range results {
		wg.Add(1)
		go func(k int) {
			defer wg.Done()
			got, err := s.ClaimDueAttempts(ctx, first, time.Minute, 10)
			assert.NoError(t, err)
			results[k] = len(got)
		}(k)
	}
	wg.Wait()
	total := 0
	for _, n := range results {
		total += n
	}
	assert.Equal(t, 1, total, "exactly one task holds the attempt")
	assert.Empty(t, hold(t, s, first.Add(30*time.Second)), "held until the lease ends")
	again2 := hold(t, s, first.Add(2*time.Minute))
	require.Len(t, again2, 1, "a task that died leaves it after its lease")
	assert.Equal(t, p.ID, again2[0].Policy.ID)
	assert.Equal(t, p.Holder.Email, again2[0].Policy.Holder.Email)

	billable, err := s.ListBillable(ctx, billedAt)
	require.NoError(t, err)
	assert.Empty(t, billable, "a policy with a month pending is not billed the next one")
}

func TestADeclinedAttemptIsCountedAndAPaidMonthStopsBeingCharged(t *testing.T) {
	requireOfferDocker(t)
	db := newDB(t)
	s, p, i := billedPolicy(t, db, "VH-2026-004512")
	ctx := context.Background()
	first, _ := rules.AttemptAt(i.DueAt, 1)
	held := hold(t, s, first)[0].Installment
	second, _ := held.AfterDecline(rules)

	require.NoError(t, s.RecordDeclined(ctx, held, domain.InstallmentAttempt{Attempt: 1, IdempotencyKey: held.IdempotencyKey(),
		Outcome: domain.AttemptDeclined, DeclineCode: "51", ProviderReference: "ch_1", AttemptedAt: first}, second))
	assert.ErrorIs(t, s.RecordDeclined(ctx, held, domain.InstallmentAttempt{Attempt: 1, IdempotencyKey: "otra",
		Outcome: domain.AttemptDeclined, AttemptedAt: first}, second), ports.ErrInstallmentClaimLost,
		"a task that lost the attempt writes nothing")
	assert.Empty(t, hold(t, s, first.Add(time.Hour)), "the next attempt is on day 8")

	held = hold(t, s, second)[0].Installment
	assert.Equal(t, 1, held.Attempts)
	assert.Equal(t, "cuota-VH-2026-004512-2-2", held.IdempotencyKey())
	require.NoError(t, s.RescheduleUnknown(ctx, held, second.Add(time.Hour)))
	held = hold(t, s, second.Add(time.Hour))[0].Installment
	assert.Equal(t, 1, held.Attempts, "an unknown outcome is not counted")
	require.NoError(t, s.RecordApproved(ctx, held, domain.InstallmentAttempt{Attempt: 2, IdempotencyKey: held.IdempotencyKey(),
		Outcome: domain.AttemptApproved, ProviderReference: "ch_2", AttemptedAt: second.Add(time.Hour)}))

	assert.Empty(t, hold(t, s, i.CancelsAt), "a paid month is not charged again")
	overdue, err := s.ListOverdue(ctx, i.CancelsAt, 10)
	require.NoError(t, err)
	assert.Empty(t, overdue, "nor does it cancel the policy")
	rec, err := s.Billing(ctx, []string{p.ID, "otra"})
	require.NoError(t, err)
	require.Len(t, rec, 1)
	got := rec[p.ID].Installments[0]
	assert.Equal(t, domain.InstallmentPaid, got.Status)
	assert.Equal(t, second.Add(time.Hour), got.PaidAt)
	assert.Equal(t, "ch_2", got.ProviderReference)
	require.Len(t, got.History, 2)
	assert.Equal(t, domain.AttemptDeclined, got.History[0].Outcome)
	assert.Equal(t, "51", got.History[0].DeclineCode)
	assert.Equal(t, domain.AttemptApproved, got.History[1].Outcome)
	assert.Nil(t, rec[p.ID].Cancellation)
}

func TestAnUnpaidMonthCancelsThePolicyOnceWithItsEvent(t *testing.T) {
	requireOfferDocker(t)
	db := newDB(t)
	s, p, i := billedPolicy(t, db, "VH-2026-004512")
	ctx := context.Background()
	later := domain.NewInstallment(p, 3, rules, billedAt)
	_, err := s.CreateInstallment(ctx, later)
	require.NoError(t, err)
	i = declineAll(t, s, i)
	assert.Equal(t, 3, i.Attempts)
	assert.True(t, i.NextAttemptAt.IsZero(), "no attempt left")

	none, err := s.ListOverdue(ctx, i.CancelsAt.Add(-time.Second), 10)
	require.NoError(t, err)
	assert.Empty(t, none, "the grace period has not ended")
	overdue, err := s.ListOverdue(ctx, i.CancelsAt, 10)
	require.NoError(t, err)
	require.Len(t, overdue, 1)

	var wg sync.WaitGroup
	created := make([]bool, 3)
	for k := range created {
		wg.Add(1)
		go func(k int) {
			defer wg.Done()
			ok, err := s.Cancel(ctx, overdue[0].Policy, overdue[0].Installment, i.CancelsAt)
			assert.NoError(t, err)
			created[k] = ok
		}(k)
	}
	wg.Wait()
	n := 0
	for _, ok := range created {
		if ok {
			n++
		}
	}
	assert.Equal(t, 1, n, "the policy is cancelled once")

	var (
		eventType string
		payload   []byte
	)
	require.NoError(t, db.QueryRowContext(ctx, `SELECT event_type, payload FROM outbox_event WHERE event_id = $1`,
		"policy-cancelled-non-payment-VH-2026-004512").Scan(&eventType, &payload))
	assert.Equal(t, domain.EventTypePolicyCancelledForNonPayment, eventType)
	var env events.Envelope
	require.NoError(t, json.Unmarshal(payload, &env))
	var body map[string]any
	require.NoError(t, env.DecodePayload(&body))
	assert.Equal(t, "not_applicable", body["refund"])
	assert.Equal(t, "sub-carolina", body["subject"])

	rec, err := s.Billing(ctx, []string{p.ID})
	require.NoError(t, err)
	c := rec[p.ID].Cancellation
	require.NotNil(t, c)
	assert.Equal(t, domain.CancellationNonPayment, c.Reason)
	assert.Equal(t, i.CancelsAt, c.CancelledAt)
	assert.Equal(t, 2, c.Installment.Seq)
	assert.Len(t, c.Installment.History, 3)
	for _, m := range rec[p.ID].Installments {
		assert.Equal(t, domain.InstallmentUnpaid, m.Status, "month %d", m.Seq)
	}
	billable, err := s.ListBillable(ctx, i.CancelsAt)
	require.NoError(t, err)
	assert.Empty(t, billable, "a cancelled policy is not billed again")
	assert.Empty(t, hold(t, s, policyEnd), "nor charged")
}

func TestTheEmailOfACancellationIsTriedUntilItGoesOut(t *testing.T) {
	requireOfferDocker(t)
	db := newDB(t)
	s, p, i := billedPolicy(t, db, "VH-2026-004512")
	ctx := context.Background()
	i = declineAll(t, s, i)
	created, err := s.Cancel(ctx, p, i, i.CancelsAt)
	require.NoError(t, err)
	require.True(t, created)

	pending, err := s.ListUnnotified(ctx, 2, 10)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	assert.Equal(t, "carolina@example.com", pending[0].Policy.Holder.Email)
	assert.Equal(t, "policy-cancelled-non-payment-VH-2026-004512", pending[0].Cancellation.EventID)
	assert.Equal(t, int64(14_230_050), pending[0].Cancellation.Installment.AmountMinor)

	require.NoError(t, s.CountEmailFailure(ctx, p.ID))
	require.NoError(t, s.CountEmailFailure(ctx, p.ID))
	pending, err = s.ListUnnotified(ctx, 2, 10)
	require.NoError(t, err)
	assert.Empty(t, pending, "given up after the maximum attempts")
	pending, err = s.ListUnnotified(ctx, 5, 10)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	require.NoError(t, s.MarkEmailed(ctx, p.ID, i.CancelsAt.Add(time.Minute)))
	pending, err = s.ListUnnotified(ctx, 5, 10)
	require.NoError(t, err)
	assert.Empty(t, pending)
	rec, err := s.Billing(ctx, []string{p.ID})
	require.NoError(t, err)
	assert.Equal(t, i.CancelsAt.Add(time.Minute), rec[p.ID].Cancellation.EmailSentAt)
}

func TestAMonthBeingChargedIsNotCancelled(t *testing.T) {
	requireOfferDocker(t)
	db := newDB(t)
	s, p, i := billedPolicy(t, db, "VH-2026-004512")
	ctx := context.Background()
	_ = hold(t, s, i.CancelsAt)

	created, err := s.Cancel(ctx, p, i, i.CancelsAt)

	require.NoError(t, err)
	assert.False(t, created, "it waits until the task that holds it answers")
	overdue, err := s.ListOverdue(ctx, i.CancelsAt, 10)
	require.NoError(t, err)
	assert.Empty(t, overdue)
}

func TestTheInstalmentsAreChargedToTheCardOfTheIssuance(t *testing.T) {
	requireOfferDocker(t)
	db := newDB(t)
	charges := postgres.NewChargeStore(db)
	ctx := context.Background()
	c, _, err := charges.CreatePending(ctx, charge("acc-1"))
	require.NoError(t, err)
	_, err = charges.AttachPaymentMethod(ctx, c.AcceptanceID, testCard(), t0)
	require.NoError(t, err)
	s := postgres.NewInstallmentStore(db)

	m, err := s.PaymentMethodOf(ctx, "acc-1")
	require.NoError(t, err)
	assert.Equal(t, testCard().Token, m.Token)
	assert.Equal(t, testCard().Last4, m.Last4)
	none, err := s.PaymentMethodOf(ctx, "acc-unknown")
	require.NoError(t, err)
	assert.False(t, none.Captured())
}
