package postgres_test

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/idempotency"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/postgres"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/migrations"
)

// HU-W1.6-01: the cancellation at the request of the insured and its refund,
// in PostgreSQL.

// paidPolicy stores a policy issued at start whose issuance was charged and
// confirmed (the first month).
func paidPolicy(t *testing.T, db *sql.DB, id string, start time.Time) domain.Policy {
	t.Helper()
	ctx := context.Background()
	p := samplePolicy(id, "sub-carolina", start.AddDate(1, 0, 0))
	p.CoverageStart = start
	require.NoError(t, postgres.NewPolicyStore(db).Create(ctx, p))
	charges := postgres.NewChargeStore(db)
	c, _, err := charges.CreatePending(ctx, charge(p.AcceptanceID))
	require.NoError(t, err)
	_, err = charges.AttachPaymentMethod(ctx, c.AcceptanceID, testCard(), t0)
	require.NoError(t, err)
	held, claimed, err := charges.Claim(ctx, c.AcceptanceID, t0.Add(time.Second), time.Minute)
	require.NoError(t, err)
	require.True(t, claimed)
	_, err = charges.Confirm(ctx, held, t0.Add(2*time.Second), idempotency.Outcome{Reference: "ch-issuance-" + id})
	require.NoError(t, err)
	return p
}

// decideAt quotes the cancellation from the facts the store read, the way
// the service does, and asks for exactly that refund.
func decideAt(now time.Time, seen *ports.CancellationFacts) ports.CancellationDecision {
	return func(f ports.CancellationFacts) (domain.InsuredCancellation, error) {
		if seen != nil {
			*seen = f
		}
		if f.Cancellation != nil {
			return domain.InsuredCancellation{}, ports.ErrPolicyAlreadyCancelled
		}
		if f.InstallmentHeld {
			return domain.InsuredCancellation{}, ports.ErrPaymentInProgress
		}
		seq, _, _ := rules.CurrentMonth(f.Policy, now)
		var method domain.PaymentMethod
		if f.Issuance != nil {
			method = f.Issuance.PaymentMethod
		}
		q := domain.QuoteCancellation(f.Policy, rules, domain.PaidMonthOf(seq, f.Issuance, f.Installments), method,
			domain.PaymentUpToDate, now)
		req := domain.CancellationRequest{Reason: domain.InsuredReasonPropertySold, Comments: "vendí el inmueble",
			RefundAmountMinor: q.RefundAmountMinor, AcceptTerms: true}
		return domain.NewInsuredCancellation(q, req, "sub-carolina", now, now.Add(24*time.Hour)), nil
	}
}

func TestTheCancellationAndItsRefundAreStoredTogether(t *testing.T) {
	requireOfferDocker(t)
	db := newDB(t)
	ctx := context.Background()
	start := time.Date(2026, 11, 18, 15, 0, 0, 0, time.UTC)
	p := paidPolicy(t, db, "VH-2026-000101", start)
	now := start.Add(5 * time.Hour)
	store := postgres.NewCancellationStore(db)

	facts, err := store.Facts(ctx, p.ID, now)
	require.NoError(t, err)
	require.NotNil(t, facts.Issuance)
	assert.Equal(t, "ch-issuance-"+p.ID, facts.Issuance.ProviderReference)
	assert.Nil(t, facts.Cancellation)

	c, err := store.Cancel(ctx, p.ID, now, decideAt(now, nil))

	require.NoError(t, err)
	require.NotNil(t, c.Refund)
	assert.Equal(t, "REF-2026-00001", c.Refund.Reference)
	assert.Equal(t, int64(8_651_600), c.Refund.AmountMinor, "89.500 x 29/30, truncated to the peso")
	installments := postgres.NewInstallmentStore(db)
	cancelled, err := installments.IsCancelled(ctx, p.ID)
	require.NoError(t, err)
	assert.True(t, cancelled, "the renewal, the alerts, the change requests and the endorsements see it")

	rec, err := installments.Billing(ctx, []string{p.ID})
	require.NoError(t, err)
	got := rec[p.ID].Cancellation
	require.NotNil(t, got)
	assert.Equal(t, domain.CancellationInsuredRequest, got.Reason)
	assert.Equal(t, domain.InsuredReasonPropertySold, got.InsuredReason)
	assert.Equal(t, "vendí el inmueble", got.Comments)
	assert.Equal(t, "sub-carolina", got.RequestedBy)
	assert.Equal(t, now, got.CancelledAt)
	assert.Equal(t, domain.EndOfDayColombia(now), got.EffectiveAt)
	assert.Empty(t, got.EventID, "no event is published")
	assert.Zero(t, got.Installment.Seq)
	require.NotNil(t, got.Refund)
	assert.Equal(t, *c.Refund, *got.Refund, "what was stored is what is read")
	assert.Equal(t, domain.RefundInProcess, got.Refund.Status)
	assert.Equal(t, "reembolso-"+p.ID, got.Refund.IdempotencyKey)
	assert.Equal(t, domain.PaymentMethod{Kind: domain.PaymentCard, Brand: "visa", Last4: "1111"}, got.Refund.Method,
		"where the money goes back to, without the token")
	assert.Equal(t, domain.PaidMonth{Seq: 1, Kind: domain.PaidByIssuance, AmountMinor: 8_950_000, Currency: "COP",
		ChargeReference: "ch-issuance-" + p.ID}, got.Refund.Paid)

	notices, err := installments.ListUnnotified(ctx, 5, 10)
	require.NoError(t, err)
	assert.Empty(t, notices, "the insured does not get the e-mail of the cancellation for non-payment")
	n, err := postgres.NewExpiringPolicyStore(db).CountCancelledSince(ctx, now.Add(-time.Hour))
	require.NoError(t, err)
	assert.Zero(t, n, "the advisor does not count it as cancelled for non-payment")

	_, err = store.Cancel(ctx, p.ID, now, decideAt(now, nil))
	assert.ErrorIs(t, err, ports.ErrPolicyAlreadyCancelled)
	_, err = db.ExecContext(ctx, `UPDATE policy_refund SET amount_minor = 1 WHERE policy_id = $1`, p.ID)
	assert.ErrorContains(t, err, "only its processing can change")
	_, err = db.ExecContext(ctx, `DELETE FROM policy_refund WHERE policy_id = $1`, p.ID)
	assert.Error(t, err, "a refund is never deleted")
}

func TestAMonthInItsGraceIsClosedWithNothingToRefund(t *testing.T) {
	requireOfferDocker(t)
	db := newDB(t)
	ctx := context.Background()
	s, p, i := billedPolicy(t, db, "VH-2026-004512")
	now := i.DueAt.Add(3 * 24 * time.Hour)
	store := postgres.NewCancellationStore(db)

	c, err := store.Cancel(ctx, p.ID, now, decideAt(now, nil))

	require.NoError(t, err)
	assert.Equal(t, domain.RefundStatusNotApplicable, c.Refund.Status)
	assert.Zero(t, c.Refund.AmountMinor)
	assert.Empty(t, c.Refund.Reference, "nothing moves: no reference is taken")
	rec, err := s.Billing(ctx, []string{p.ID})
	require.NoError(t, err)
	assert.Equal(t, domain.InstallmentUnpaid, rec[p.ID].Installments[0].Status, "the month is closed without charging it")
	assert.Empty(t, hold(t, s, i.DueAt.AddDate(0, 0, 8)), "the sweep charges nothing more")
	overdue, err := s.ListOverdue(ctx, i.CancelsAt, 10)
	require.NoError(t, err)
	assert.Empty(t, overdue, "and does not cancel it again for non-payment")
}

func TestOneOfManyConfirmationsCancelsAndTheOthersFindItCancelled(t *testing.T) {
	requireOfferDocker(t)
	db := newDB(t)
	ctx := context.Background()
	start := time.Date(2026, 11, 18, 15, 0, 0, 0, time.UTC)
	p := paidPolicy(t, db, "VH-2026-000102", start)
	now := start.Add(time.Hour)
	store := postgres.NewCancellationStore(db)

	var (
		wg               sync.WaitGroup
		mu               sync.Mutex
		won, lost, other int
	)
	for k := 0; k < 20; k++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := store.Cancel(ctx, p.ID, now, decideAt(now, nil))
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				won++
			case errors.Is(err, ports.ErrPolicyAlreadyCancelled):
				lost++
			default:
				other++
				t.Log(err)
			}
		}()
	}
	wg.Wait()

	assert.Equal(t, 1, won)
	assert.Equal(t, 19, lost)
	assert.Zero(t, other)
	var refunds, last int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM policy_refund`).Scan(&refunds))
	require.NoError(t, db.QueryRowContext(ctx, `SELECT last_number FROM refund_counter`).Scan(&last))
	assert.Equal(t, 1, refunds)
	assert.Equal(t, 1, last, "the confirmations that lost took no number")
}

func TestAMonthTheSweepIsChargingBlocksTheCancellation(t *testing.T) {
	requireOfferDocker(t)
	db := newDB(t)
	ctx := context.Background()
	s, p, i := billedPolicy(t, db, "VH-2026-004512")
	first, _ := rules.AttemptAt(i.DueAt, 1)
	require.Len(t, hold(t, s, first), 1)
	store := postgres.NewCancellationStore(db)
	var seen ports.CancellationFacts

	_, err := store.Cancel(ctx, p.ID, first.Add(time.Second), decideAt(first.Add(time.Second), &seen))

	assert.ErrorIs(t, err, ports.ErrPaymentInProgress)
	assert.True(t, seen.InstallmentHeld)
	cancelled, err := s.IsCancelled(ctx, p.ID)
	require.NoError(t, err)
	assert.False(t, cancelled, "nothing was written")

	// A month billed by a sweep that read the policy before it was cancelled
	// is never charged.
	_, err = store.Cancel(ctx, p.ID, first.Add(2*time.Minute), decideAt(first.Add(2*time.Minute), nil))
	require.NoError(t, err)
	late := domain.NewInstallment(p, 3, rules, first.Add(3*time.Minute))
	created, err := s.CreateInstallment(ctx, late)
	require.NoError(t, err)
	require.True(t, created)
	at, _ := rules.AttemptAt(late.DueAt, 1)
	assert.Empty(t, hold(t, s, at))
}

func TestMigration0020KeepsTheCancellationsForNonPayment(t *testing.T) {
	db := newUnmigratedDB(t)
	ctx := context.Background()
	old := fstest.MapFS{}
	entries, err := migrations.FS.ReadDir(".")
	require.NoError(t, err)
	for _, e := range entries {
		if name := e.Name(); strings.HasSuffix(name, ".sql") && name < "0020_" {
			data, err := migrations.FS.ReadFile(name)
			require.NoError(t, err)
			old[name] = &fstest.MapFile{Data: data}
		}
	}
	require.NoError(t, postgres.MigrateFS(ctx, db, old))
	s, p, i := billedPolicy(t, db, "VH-2026-004512")
	_, err = db.ExecContext(ctx, `INSERT INTO policy_cancellation (policy_id, reason, installment_seq, cancelled_at, event_id)
		VALUES ($1, 'non_payment', $2, $3, 'policy-cancelled-non-payment-VH-2026-004512')`, p.ID, i.Seq, i.CancelsAt)
	require.NoError(t, err)

	require.NoError(t, postgres.Migrate(ctx, db))

	rec, err := s.Billing(ctx, []string{p.ID})
	require.NoError(t, err)
	c := rec[p.ID].Cancellation
	require.NotNil(t, c)
	assert.Equal(t, domain.CancellationNonPayment, c.Reason)
	assert.Equal(t, i.CancelsAt, c.EffectiveAt, "it took effect when it was made")
	assert.Equal(t, 2, c.Installment.Seq)
	assert.Nil(t, c.Refund)
	_, err = db.ExecContext(ctx, `UPDATE policy_cancellation SET event_id = NULL WHERE policy_id = $1`, p.ID)
	assert.ErrorContains(t, err, "policy_cancellation_reason_columns", "a cancellation for non-payment keeps its event")
}

func TestARefundIsHeldByOneWorkerAndEndsCompletedOrFailed(t *testing.T) {
	requireOfferDocker(t)
	db := newDB(t)
	ctx := context.Background()
	start := time.Date(2026, 11, 18, 15, 0, 0, 0, time.UTC)
	store := postgres.NewCancellationStore(db)
	now := start.Add(time.Hour)
	for _, id := range []string{"VH-2026-000201", "VH-2026-000202"} {
		p := paidPolicy(t, db, id, start)
		_, err := store.Cancel(ctx, p.ID, now, decideAt(now, nil))
		require.NoError(t, err)
	}

	due, err := store.ListDue(ctx, now, 10)
	require.NoError(t, err)
	assert.Equal(t, []string{"VH-2026-000201", "VH-2026-000202"}, due)
	held, claimed, err := store.Claim(ctx, "VH-2026-000201", now, time.Minute)
	require.NoError(t, err)
	require.True(t, claimed)
	_, again, err := store.Claim(ctx, "VH-2026-000201", now.Add(time.Second), time.Minute)
	require.NoError(t, err)
	assert.False(t, again, "held by one worker at a time")

	later, err := store.Reschedule(ctx, held, now, now.Add(time.Second), "connection refused")
	require.NoError(t, err)
	assert.Equal(t, 1, later.Attempts)
	assert.Equal(t, domain.RefundInProcess, later.Status)
	_, err = store.Confirm(ctx, held, now, idempotency.Outcome{Reference: "re-1"})
	assert.ErrorIs(t, err, idempotency.ErrClaimLost, "a worker that lost its claim records nothing")

	held, claimed, err = store.Claim(ctx, "VH-2026-000201", now.Add(time.Second), time.Minute)
	require.NoError(t, err)
	require.True(t, claimed)
	done, err := store.Confirm(ctx, held, now.Add(2*time.Second), idempotency.Outcome{Reference: "re-1"})
	require.NoError(t, err)
	assert.Equal(t, domain.RefundCompleted, done.Status)
	assert.Equal(t, "re-1", done.ProviderReference)
	assert.Equal(t, now.Add(2*time.Second), done.CompletedAt)

	other, claimed, err := store.Claim(ctx, "VH-2026-000202", now, time.Minute)
	require.NoError(t, err)
	require.True(t, claimed)
	failed, err := store.Fail(ctx, other, now, idempotency.ReasonWindowExpired, "timeout")
	require.NoError(t, err)
	assert.Equal(t, domain.RefundFailed, failed.Status)
	assert.Equal(t, string(idempotency.ReasonWindowExpired), failed.FailureReason)

	due, err = store.ListDue(ctx, now.Add(time.Hour), 10)
	require.NoError(t, err)
	assert.Empty(t, due, "completed and failed are terminal")
}
