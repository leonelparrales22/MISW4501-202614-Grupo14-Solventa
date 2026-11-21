package billing_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/idempotency"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/resilience"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/billing"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
)

// HU-W1.6-01 (decision D-5 b): the refunds of the cancellations, sent to the
// gateway with the pattern of the premium charge (EC-D02).

// memRefunds is an in-memory ports.RefundStore with the conditional
// semantics of the PostgreSQL one (claims are compare-and-swap).
type memRefunds struct {
	mu   sync.Mutex
	rows map[string]domain.Refund
}

var _ ports.RefundStore = (*memRefunds)(nil)

func (m *memRefunds) ListDue(_ context.Context, now time.Time, _ int) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var ids []string
	for id, r := range m.rows {
		if r.Status == domain.RefundInProcess && !r.NextAttemptAt.After(now) && !r.ClaimedUntil.After(now) {
			ids = append(ids, id)
		}
	}
	return ids, nil
}

func (m *memRefunds) Claim(_ context.Context, id string, now time.Time, lease time.Duration) (domain.Refund, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.rows[id]
	if !ok || r.Status != domain.RefundInProcess || r.NextAttemptAt.After(now) || r.ClaimedUntil.After(now) {
		return domain.Refund{}, false, nil
	}
	r.ClaimedUntil = now.Add(lease)
	m.rows[id] = r
	return r, true, nil
}

func (m *memRefunds) held(r domain.Refund, change func(*domain.Refund)) (domain.Refund, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cur := m.rows[r.PolicyID]
	if cur.Status != domain.RefundInProcess || !cur.ClaimedUntil.Equal(r.ClaimedUntil) {
		return domain.Refund{}, idempotency.ErrClaimLost
	}
	change(&cur)
	cur.ClaimedUntil = time.Time{}
	m.rows[r.PolicyID] = cur
	return cur, nil
}

func (m *memRefunds) Confirm(_ context.Context, r domain.Refund, now time.Time, out idempotency.Outcome) (domain.Refund, error) {
	return m.held(r, func(c *domain.Refund) {
		c.Status, c.ProviderReference, c.CompletedAt, c.NextAttemptAt = domain.RefundCompleted, out.Reference, now, time.Time{}
	})
}

func (m *memRefunds) Reschedule(_ context.Context, r domain.Refund, _, next time.Time, cause string) (domain.Refund, error) {
	return m.held(r, func(c *domain.Refund) { c.Attempts, c.NextAttemptAt, c.LastError = c.Attempts+1, next, cause })
}

func (m *memRefunds) Fail(_ context.Context, r domain.Refund, _ time.Time, reason idempotency.FailureReason,
	cause string) (domain.Refund, error) {
	return m.held(r, func(c *domain.Refund) {
		c.Status, c.Attempts, c.FailureReason, c.LastError = domain.RefundFailed, c.Attempts+1, string(reason), cause
		c.NextAttemptAt = time.Time{}
	})
}

func (m *memRefunds) get(id string) domain.Refund {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.rows[id]
}

// refundGateway answers with err while it is set and counts the calls by key.
type refundGateway struct {
	mu    sync.Mutex
	err   error
	calls map[string]int
	// hold, when set, keeps a call open until it is closed, so two workers
	// overlap.
	hold chan struct{}
}

func (g *refundGateway) Refund(_ context.Context, r domain.Refund) (string, error) {
	if g.hold != nil {
		<-g.hold
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.calls == nil {
		g.calls = map[string]int{}
	}
	g.calls[r.IdempotencyKey]++
	if g.err != nil {
		return "", g.err
	}
	return "re-1", nil
}

func (g *refundGateway) set(err error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.err = err
}

func (g *refundGateway) count(key string) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.calls[key]
}

var registered = time.Date(2026, 11, 20, 15, 0, 0, 0, time.UTC)

func refundEnv(t *testing.T, retryUntil time.Time) (*billing.RefundWorker, *memRefunds, *refundGateway, *clock) {
	t.Helper()
	q := domain.CancellationQuote{PolicyID: "VH-2026-000101", RefundAmountMinor: 13_755_600, Currency: "COP",
		Paid: domain.PaidMonth{Seq: 1, Kind: domain.PaidByIssuance, AmountMinor: 14_230_000, ChargeReference: "ch-1"}}
	r := domain.NewRefund(q, registered, retryUntil)
	r.Reference = "REF-2026-00001"
	store := &memRefunds{rows: map[string]domain.Refund{r.PolicyID: r}}
	gw, c := &refundGateway{}, &clock{now: registered}
	w, err := billing.NewRefundWorker(store, gw, c, billing.WorkerConfig{ClaimLease: 30 * time.Second,
		Retry: resilience.DefaultRetryPolicy()})
	require.NoError(t, err)
	return w, store, gw, c
}

func TestARefundIsCompletedWithTheAnswerOfTheGateway(t *testing.T) {
	w, store, gw, _ := refundEnv(t, registered.Add(24*time.Hour))

	assert.Equal(t, 1, w.ProcessDue(context.Background()))

	r := store.get("VH-2026-000101")
	assert.Equal(t, domain.RefundCompleted, r.Status)
	assert.Equal(t, "re-1", r.ProviderReference)
	assert.Equal(t, registered, r.CompletedAt)
	assert.Equal(t, 1, gw.count("reembolso-VH-2026-000101"))
	assert.Zero(t, w.ProcessDue(context.Background()), "a completed refund is never sent again")
}

func TestARefundWithAnUnknownOutcomeIsRetriedWithTheSameKeyUntilItIsCompleted(t *testing.T) {
	w, store, gw, c := refundEnv(t, registered.Add(24*time.Hour))
	gw.set(fmt.Errorf("connection refused: %w", idempotency.ErrAmbiguous))

	w.ProcessDue(context.Background())

	r := store.get("VH-2026-000101")
	assert.Equal(t, domain.RefundInProcess, r.Status, "the gateway is down: the refund stays in process")
	assert.Equal(t, 1, r.Attempts)
	assert.Equal(t, registered.Add(resilience.DefaultRetryBase), r.NextAttemptAt, "the growing wait of the charge")
	assert.Zero(t, w.ProcessDue(context.Background()), "not due before its wait")

	gw.set(nil)
	c.set(r.NextAttemptAt)
	w.ProcessDue(context.Background())

	assert.Equal(t, domain.RefundCompleted, store.get("VH-2026-000101").Status)
	assert.Equal(t, 2, gw.count("reembolso-VH-2026-000101"), "the retry carries the same key: the gateway refunds it once")
}

func TestARejectedRefundOrOneOutOfItsWindowEndsFailed(t *testing.T) {
	w, store, gw, _ := refundEnv(t, registered.Add(24*time.Hour))
	gw.set(fmt.Errorf("422 idempotency_key_reused: %w", idempotency.ErrRejected))

	w.ProcessDue(context.Background())

	r := store.get("VH-2026-000101")
	assert.Equal(t, domain.RefundFailed, r.Status)
	assert.Equal(t, string(idempotency.ReasonRejected), r.FailureReason)

	w, store, gw, c := refundEnv(t, registered.Add(time.Minute))
	gw.set(fmt.Errorf("timeout: %w", idempotency.ErrAmbiguous))
	c.set(registered.Add(2 * time.Minute))

	w.ProcessDue(context.Background())

	r = store.get("VH-2026-000101")
	assert.Equal(t, domain.RefundFailed, r.Status, "never in limbo (EC-D02)")
	assert.Equal(t, string(idempotency.ReasonWindowExpired), r.FailureReason)
}

func TestTwoWorkersSendARefundOnce(t *testing.T) {
	w, store, gw, _ := refundEnv(t, registered.Add(24*time.Hour))
	gw.hold = make(chan struct{})
	var wg sync.WaitGroup
	for k := 0; k < 2; k++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w.ProcessDue(context.Background())
		}()
	}
	time.Sleep(50 * time.Millisecond)
	close(gw.hold)
	wg.Wait()

	assert.Equal(t, 1, gw.count("reembolso-VH-2026-000101"), "the claim lets one of them call the gateway")
	assert.Equal(t, domain.RefundCompleted, store.get("VH-2026-000101").Status)
}

func TestACancellationWakesTheWorkerToSendItsRefundAtOnce(t *testing.T) {
	store := &memRefunds{rows: map[string]domain.Refund{}}
	gw, c := &refundGateway{}, &clock{now: registered}
	w, err := billing.NewRefundWorker(store, gw, c, billing.WorkerConfig{PollInterval: time.Hour})
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		w.Run(ctx)
	}()

	r := domain.NewRefund(domain.CancellationQuote{PolicyID: "VH-2026-000101", RefundAmountMinor: 100, Currency: "COP"},
		registered, registered.Add(time.Hour))
	store.mu.Lock()
	store.rows[r.PolicyID] = r
	store.mu.Unlock()
	w.Notify()
	w.Notify() // a second wake while one is pending never blocks

	require.Eventually(t, func() bool { return store.get(r.PolicyID).Status == domain.RefundCompleted },
		2*time.Second, 10*time.Millisecond, "without waiting an hour for the next poll")
	cancel()
	<-done
	_, err = billing.NewRefundWorker(nil, gw, c, billing.WorkerConfig{})
	assert.Error(t, err)
}
