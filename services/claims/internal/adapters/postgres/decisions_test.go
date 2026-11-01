package postgres_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/idempotency"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/adapters/postgres"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/ports"
)

// HU-W2.2-03: la decisión sobre un siniestro y el pago de su indemnización.

var decidedAt = time.Date(2026, 10, 31, 15, 0, 0, 0, time.UTC)

const decidedClaim = "8f14e45f-ceea-467a-9f3a-5a2d0b1c7e90"

type decisionStores struct {
	webStores
	decisions *postgres.DecisionStore
}

func newDecisionStores(t *testing.T) decisionStores {
	t.Helper()
	s := newWebStores(t)
	_, _, err := s.claims.CreateWeb(context.Background(), webClaim(decidedClaim, "sub-carolina", "form-1"))
	require.NoError(t, err)
	return decisionStores{webStores: s, decisions: postgres.DecisionStoreOf(s.claims)}
}

func (s decisionStores) status(t *testing.T) domain.Status {
	t.Helper()
	c, err := s.claims.Get(context.Background(), decidedClaim)
	require.NoError(t, err)
	return c.Status
}

func approval(t *testing.T) domain.Decision {
	t.Helper()
	c := domain.ClaimRecord{Claim: domain.Claim{ID: decidedClaim, Subject: "sub-carolina", Status: domain.StatusActive}}
	cov := domain.Coverage{Incident: domain.IncidentCollision, InsuredValueMinor: 32_000_000_000, Currency: "COP",
		DeductibleBasisPoints: 500}
	holder := domain.Holder{Email: "carolina@correo.example", PayoutAccount: domain.PayoutAccount{BankCode: "1007",
		BankName: "Banco Aliado", AccountType: "savings", AccountNumber: "0123-4567-89"}}
	d, err := domain.Approve(c, cov, holder, 435_000_000, "analista@solventa.example", decidedAt)
	require.NoError(t, err)
	return d
}

func TestAnApprovalIsRecordedOnceAndMovesTheClaim(t *testing.T) {
	s := newDecisionStores(t)
	ctx := context.Background()

	d, err := s.decisions.Decide(ctx, approval(t))

	require.NoError(t, err)
	assert.Equal(t, domain.StatusApproved, s.status(t))
	assert.Equal(t, "analista@solventa.example", d.DecidedBy)
	assert.True(t, decidedAt.Equal(d.DecidedAt))
	assert.NotEmpty(t, d.FilingNumber)
	require.NotNil(t, d.Payout)
	assert.Equal(t, domain.PayoutKey(decidedClaim), d.Payout.Key)
	assert.EqualValues(t, 413_250_000, d.Payout.AmountMinor)
	assert.Equal(t, "0123-4567-89", d.Payout.Destination.AccountNumber, "the store keeps what is needed to pay")
	assert.Equal(t, idempotency.StatusPending, d.Payout.Status)

	_, err = s.decisions.Decide(ctx, approval(t))
	assert.ErrorIs(t, err, ports.ErrNotDecidable, "a claim is decided once")
	other := approval(t)
	other.ClaimID = "8f14e45f-ceea-467a-9f3a-5a2d0b1c7e99"
	_, err = s.decisions.Decide(ctx, other)
	assert.ErrorIs(t, err, ports.ErrClaimNotFound)
}

func TestTwoAnalystsDecidingAtOnceRecordOneDecision(t *testing.T) {
	s := newDecisionStores(t)
	var wg sync.WaitGroup
	errs := make([]error, 8)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = s.decisions.Decide(context.Background(), approval(t))
		}(i)
	}
	wg.Wait()
	won := 0
	for _, err := range errs {
		if err == nil {
			won++
		} else {
			assert.ErrorIs(t, err, ports.ErrNotDecidable)
		}
	}
	assert.Equal(t, 1, won)
}

func TestThePayoutIsClaimedByOneWorkerAndPaid(t *testing.T) {
	s := newDecisionStores(t)
	ctx := context.Background()
	_, err := s.decisions.Decide(ctx, approval(t))
	require.NoError(t, err)

	due, err := s.decisions.ListPayoutsDue(ctx, decidedAt, 10)
	require.NoError(t, err)
	assert.Equal(t, []string{decidedClaim}, due)

	claimed, ok, err := s.decisions.Claim(ctx, decidedClaim, decidedAt, 30*time.Second)
	require.NoError(t, err)
	require.True(t, ok)
	_, again, err := s.decisions.Claim(ctx, decidedClaim, decidedAt.Add(time.Second), 30*time.Second)
	require.NoError(t, err)
	assert.False(t, again, "a held payout is not claimed twice")

	paid, err := s.decisions.Confirm(ctx, claimed, decidedAt.Add(2*time.Second), idempotency.Outcome{Reference: "po-1"})
	require.NoError(t, err)
	assert.Equal(t, idempotency.StatusConfirmed, paid.Payout.Status)
	assert.Equal(t, "po-1", paid.Payout.Reference)
	assert.Equal(t, domain.StatusPaid, s.status(t))

	_, err = s.decisions.Confirm(ctx, claimed, decidedAt.Add(3*time.Second), idempotency.Outcome{})
	assert.ErrorIs(t, err, idempotency.ErrClaimLost, "a result is recorded only while its claim is held")
}

func TestAFailedPayoutIsAnExceptionThatIsRetriedWithTheSameKey(t *testing.T) {
	s := newDecisionStores(t)
	ctx := context.Background()
	_, err := s.decisions.Decide(ctx, approval(t))
	require.NoError(t, err)
	claimed, _, err := s.decisions.Claim(ctx, decidedClaim, decidedAt, 30*time.Second)
	require.NoError(t, err)

	failed, err := s.decisions.Fail(ctx, claimed, decidedAt.Add(time.Second), idempotency.ReasonRejected, "invalid account")
	require.NoError(t, err)
	require.NoError(t, s.decisions.RecordGatewayCode(ctx, decidedClaim, "invalid_destination_account"))

	assert.Equal(t, domain.PayoutRejected, failed.Payout.Failure)
	assert.Equal(t, 1, failed.Payout.Attempts)
	assert.Equal(t, domain.StatusPaymentException, s.status(t))
	read, err := s.decisions.Decision(ctx, decidedClaim)
	require.NoError(t, err)
	assert.Equal(t, "invalid_destination_account", read.Payout.GatewayCode)

	corrected := domain.PayoutAccount{BankCode: "1007", BankName: "Banco Aliado", AccountType: "savings",
		AccountNumber: "0123-4567-12"}
	retried, err := s.decisions.RetryPayout(ctx, decidedClaim,
		domain.PayoutRetry{By: "analista@solventa.example", At: decidedAt.Add(time.Minute), Destination: &corrected})

	require.NoError(t, err)
	assert.Equal(t, domain.StatusApproved, s.status(t))
	assert.Equal(t, domain.PayoutKey(decidedClaim), retried.Payout.Key, "the key never changes")
	assert.Equal(t, corrected, retried.Payout.Destination)
	assert.Equal(t, idempotency.StatusPending, retried.Payout.Status)
	assert.Zero(t, retried.Payout.Attempts)
	assert.Empty(t, retried.Payout.GatewayCode)
	assert.Equal(t, 1, retried.Payout.ManualRetries)
	assert.Equal(t, "analista@solventa.example", retried.Payout.LastRetriedBy)
	var logged int
	require.NoError(t, s.db.QueryRowContext(ctx, `SELECT count(*) FROM claim_payout_retry WHERE claim_id = $1 AND destination_changed`,
		decidedClaim).Scan(&logged))
	assert.Equal(t, 1, logged)

	_, err = s.decisions.RetryPayout(ctx, decidedClaim, domain.PayoutRetry{By: "x", At: decidedAt.Add(2 * time.Minute)})
	assert.ErrorIs(t, err, ports.ErrPayoutNotRetryable, "a payout in course is not retried")
}

func TestAnAttemptThatFailsAmbiguouslyIsRescheduled(t *testing.T) {
	s := newDecisionStores(t)
	ctx := context.Background()
	_, err := s.decisions.Decide(ctx, approval(t))
	require.NoError(t, err)
	claimed, _, err := s.decisions.Claim(ctx, decidedClaim, decidedAt, 30*time.Second)
	require.NoError(t, err)

	next := decidedAt.Add(2 * time.Second)
	again, err := s.decisions.Reschedule(ctx, claimed, decidedAt.Add(time.Second), next, "timeout")

	require.NoError(t, err)
	assert.Equal(t, idempotency.StatusPending, again.Payout.Status)
	assert.Equal(t, 1, again.Payout.Attempts)
	assert.True(t, next.Equal(again.Payout.NextAttemptAt))
	assert.Equal(t, domain.StatusApproved, s.status(t))
	due, err := s.decisions.ListPayoutsDue(ctx, decidedAt.Add(time.Second), 10)
	require.NoError(t, err)
	assert.Empty(t, due, "not before its next attempt")
}

func TestARejectionLeavesItsNoticeDue(t *testing.T) {
	s := newDecisionStores(t)
	ctx := context.Background()
	c := domain.ClaimRecord{Claim: domain.Claim{ID: decidedClaim}}
	rejection, err := domain.Reject(c, "La póliza excluye el daño reportado.", "carolina@correo.example", "analista", decidedAt)
	require.NoError(t, err)

	d, err := s.decisions.Decide(ctx, rejection)

	require.NoError(t, err)
	assert.Equal(t, domain.StatusRejected, s.status(t))
	assert.Equal(t, "La póliza excluye el daño reportado.", d.Reason)
	assert.Nil(t, d.Payout)
	due, err := s.decisions.ListPayoutsDue(ctx, decidedAt.Add(time.Hour), 10)
	require.NoError(t, err)
	assert.Empty(t, due, "a rejection pays nothing")

	notices, err := s.decisions.ListNoticesDue(ctx, decidedAt, 10)
	require.NoError(t, err)
	require.Len(t, notices, 1)
	assert.Equal(t, "carolina@correo.example", notices[0].Notice.To)

	require.NoError(t, s.decisions.NoticeFailed(ctx, decidedClaim, decidedAt, decidedAt.Add(time.Minute), "down"))
	notices, err = s.decisions.ListNoticesDue(ctx, decidedAt.Add(time.Second), 10)
	require.NoError(t, err)
	assert.Empty(t, notices, "a failed notice waits for its next attempt")

	require.NoError(t, s.decisions.NoticeSent(ctx, decidedClaim, decidedAt.Add(time.Minute)))
	notices, err = s.decisions.ListNoticesDue(ctx, decidedAt.Add(time.Hour), 10)
	require.NoError(t, err)
	assert.Empty(t, notices)
	read, err := s.decisions.Decision(ctx, decidedClaim)
	require.NoError(t, err)
	assert.Equal(t, 2, read.Notice.Attempts)
	assert.False(t, read.Notice.SentAt.IsZero())
}
