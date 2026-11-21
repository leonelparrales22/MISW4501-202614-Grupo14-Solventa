// This file checks the acceptance criteria of the cancellation of a policy at
// the request of its insured (HU-W1.6-01) end to end, in process: the quote
// and the confirmation over HTTP, the transaction over a real PostgreSQL
// (Testcontainers), the refund worker against a payment gateway over HTTP that
// refunds a key once (like stubs/payment-gateway) and can be down, and what
// the insured and the rest of origination read afterwards.
package integration_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/adapters/payments"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/resilience"
	httpadapter "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/http"
	paymentsadapter "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/payments"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/postgres"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/billing"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/cancellation"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/endorsements"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/profiling"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/renewal"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/wallet"
)

// refundGateway refunds a key once, like stubs/payment-gateway, counts the
// requests and the real refunds, and can be down (503: the outcome is
// unknown, so the refund is retried with the same key).
type refundGateway struct {
	mu       sync.Mutex
	down     bool
	requests map[string]int
	refunded map[string]int
	srv      *httptest.Server
}

func newRefundGateway(t *testing.T) *refundGateway {
	t.Helper()
	g := &refundGateway{requests: map[string]int{}, refunded: map[string]int{}}
	g.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.Header.Get("Idempotency-Key")
		g.mu.Lock()
		defer g.mu.Unlock()
		g.requests[key]++
		if g.down {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		status := http.StatusOK
		if g.refunded[key] == 0 {
			g.refunded[key]++
			status = http.StatusCreated
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]any{"refund_id": "re-" + key, "status": "succeeded", "idempotency_key": key})
	}))
	t.Cleanup(g.srv.Close)
	return g
}

func (g *refundGateway) setDown(down bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.down = down
}

func (g *refundGateway) counts(key string) (requests, refunded int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.requests[key], g.refunded[key]
}

type cancellationEnv struct {
	*renewalEnv
	portal  *acceptanceEnv
	refunds *refundGateway
	store   *postgres.InstallmentStore
}

// newCancellationEnv wires the cancellation, the wallet with the billing of
// HU-W1.6-02 and workers refunding in the background.
func newCancellationEnv(t *testing.T, workers int) *cancellationEnv {
	t.Helper()
	e := &cancellationEnv{renewalEnv: newRenewalEnv(t), refunds: newRefundGateway(t)}
	e.store = postgres.NewInstallmentStore(e.db)
	cancellations := postgres.NewCancellationStore(e.db)
	gateway := paymentsadapter.NewRefundGateway(payments.NewClient(payments.Config{BaseURL: e.refunds.srv.URL,
		Timeout: 300 * time.Millisecond, Breaker: resilience.Config{ConsecutiveFailures: 10_000, OpenTimeout: time.Second,
			HalfOpenSuccesses: 1}}))
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	var first *billing.RefundWorker
	for k := 0; k < workers; k++ {
		w, err := billing.NewRefundWorker(cancellations, gateway, e.clock, billing.WorkerConfig{PollInterval: 10 * time.Millisecond,
			ClaimLease: 2 * time.Second, Retry: resilience.RetryPolicy{Base: 20 * time.Millisecond, Max: 100 * time.Millisecond}})
		require.NoError(t, err)
		go w.Run(ctx)
		if first == nil {
			first = w
		}
	}
	svc, err := cancellation.NewService(cancellation.Config{Store: cancellations, Rules: domain.DefaultInstallmentRules,
		Clock: e.clock, RetryWindow: 24 * time.Hour, Refunds: first})
	require.NoError(t, err)

	policyWallet, err := wallet.NewService(e.svc, nil, e.clock)
	require.NoError(t, err)
	policyWallet = policyWallet.WithBilling(e.store, e.store, domain.DefaultInstallmentRules)
	expiring, err := renewal.NewExpiringService(postgres.NewExpiringPolicyStore(e.db), domain.DefaultRenewalPolicy, e.clock)
	require.NoError(t, err)
	srv := httptest.NewServer(httpadapter.NewRouter(profiling.NewResolver(nil, nil, e.clock),
		httpadapter.WithObservability("origination"),
		httpadapter.WithPolicyWallet(policyWallet, subjectVerifier{}),
		httpadapter.WithExpiringPolicies(expiring, subjectVerifier{}),
		httpadapter.WithCancellations(svc, subjectVerifier{})))
	t.Cleanup(srv.Close)
	e.portal = &acceptanceEnv{srv: srv}
	return e
}

// paidToday seeds a policy of carolina bought today, whose issuance was
// charged with the gateway's id ch-<id>.
func (e *cancellationEnv) paidToday(t *testing.T, id string) domain.Policy {
	t.Helper()
	p := e.seedPolicy(t, id, 365*24*time.Hour)
	_, err := e.db.ExecContext(context.Background(), `UPDATE charge_attempt SET provider_reference = $2
		WHERE acceptance_id = $1`, p.AcceptanceID, "ch-"+id)
	require.NoError(t, err)
	return p
}

func (e *cancellationEnv) quote(t *testing.T, id string) map[string]any {
	t.Helper()
	code, q := e.portal.do(t, http.MethodGet, "/v1/policies/"+id+"/cancellation/quote", carolina, "")
	require.Equal(t, http.StatusOK, code, "%v", q)
	return q
}

func confirmation(amount any) string {
	return fmt.Sprintf(`{"reason":"property_sold","comments":"vendí el inmueble","refund_amount":%v,"accept_terms":true}`, amount)
}

func (e *cancellationEnv) detail(t *testing.T, id string) map[string]any {
	t.Helper()
	code, d := e.portal.do(t, http.MethodGet, "/v1/policies/"+id, carolina, "")
	require.Equal(t, http.StatusOK, code)
	return d
}

// CA-1: the insured sees the refund before confirming. CA-2: when they
// confirm, the policy is "Cancelada" and the refund is registered with its
// amount, date and payment method; it is then refunded once.
func TestCancellationCA1AndCA2TheRefundIsShownThenRegisteredWithThePolicyCancelled(t *testing.T) {
	e := newCancellationEnv(t, 1)
	p := e.paidToday(t, "VH-2026-000101")
	other := e.paidToday(t, "VH-2026-000102")

	q := e.quote(t, p.ID)
	period, unused := int64(q["period_days"].(float64)), int64(q["unused_days"].(float64))
	assert.Equal(t, period-1, unused, "bought today: only today is used")
	want := float64(14_230_000*unused/period/100) * 1.0
	assert.Equal(t, want, q["refund_amount"], "142.300 x N / D, truncated to the peso")
	assert.Equal(t, "1111", q["refund_method"].(map[string]any)["last4"])
	_, refunded := e.refunds.counts("reembolso-" + p.ID)
	assert.Zero(t, refunded, "quoting changes nothing")

	code, body := e.portal.do(t, http.MethodPost, "/v1/policies/"+p.ID+"/cancellation", carolina, confirmation(q["refund_amount"]))

	require.Equal(t, http.StatusCreated, code, "%v", body)
	assert.Equal(t, "cancelled", body["status"])
	registered := body["cancellation"].(map[string]any)["refund_detail"].(map[string]any)
	year := e.clock.Now().In(domain.ColombiaTime).Year()
	assert.Equal(t, fmt.Sprintf("REF-%d-00001", year), registered["reference"])
	assert.Equal(t, want, registered["amount"])
	assert.NotEmpty(t, registered["registered_at"])
	assert.Equal(t, "visa", registered["method"].(map[string]any)["brand"])

	require.Eventually(t, func() bool {
		return e.detail(t, p.ID)["cancellation"].(map[string]any)["refund"] == "completed"
	}, 10*time.Second, 20*time.Millisecond, "the refund is sent to the gateway in the background")
	_, refunded = e.refunds.counts("reembolso-" + p.ID)
	assert.Equal(t, 1, refunded)

	item := e.walletItem(t, p.ID)
	assert.Equal(t, "cancelled", item["status"], "A1: Cancelada")
	assert.Equal(t, false, item["can_renew"])
	c := item["cancellation"].(map[string]any)
	assert.Equal(t, "insured_request", c["reason"])
	assert.Equal(t, want, c["refund_detail"].(map[string]any)["amount"])
	assert.Equal(t, "active", e.walletItem(t, other.ID)["status"], "the other policy is untouched")
	code, _ = e.portal.do(t, http.MethodGet, "/v1/policies/"+p.ID+"/cancellation/quote", carolina, "")
	assert.Equal(t, http.StatusConflict, code, "it cannot be cancelled twice")

	ctx := context.Background()
	inForce, err := endorsements.NewTerms(nil, nil).WithCancellations(e.store).InForce(ctx, p, e.clock.Now())
	require.NoError(t, err)
	assert.False(t, inForce, "it can no longer change (HU-W1.4-01 and HU-W1.4-02)")
	notices, err := e.store.ListUnnotified(ctx, 5, 10)
	require.NoError(t, err)
	assert.Empty(t, notices, "no e-mail of a cancellation for non-payment")
	code, list := e.portal.do(t, http.MethodGet, "/v1/policies/expiring?status=cancelled_non_payment", laura, "")
	require.Equal(t, http.StatusOK, code, "%v", list)
	assert.EqualValues(t, 0, list["cancelled_non_payment_last_30_days"], "the advisor does not count it as non-payment")
	assert.Empty(t, itemsOf(list))
}

func (e *cancellationEnv) walletItem(t *testing.T, id string) map[string]any {
	t.Helper()
	code, body := e.portal.do(t, http.MethodGet, "/v1/policies", carolina, "")
	require.Equal(t, http.StatusOK, code)
	for _, it := range itemsOf(body) {
		if it["policy_id"] == id {
			return it
		}
	}
	t.Fatalf("policy %s is not in the wallet", id)
	return nil
}

func TestCancellationOneOfTwentyConfirmationsCancelsAndRefundsOnce(t *testing.T) {
	e := newCancellationEnv(t, 2)
	p := e.paidToday(t, "VH-2026-000103")
	amount := e.quote(t, p.ID)["refund_amount"]

	codes := make(chan int, 20)
	var wg sync.WaitGroup
	for k := 0; k < 20; k++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			code, _ := e.portal.do(t, http.MethodPost, "/v1/policies/"+p.ID+"/cancellation", carolina, confirmation(amount))
			codes <- code
		}()
	}
	wg.Wait()
	close(codes)

	got := map[int]int{}
	for c := range codes {
		got[c]++
	}
	assert.Equal(t, map[int]int{http.StatusCreated: 1, http.StatusConflict: 19}, got)
	require.Eventually(t, func() bool {
		return e.detail(t, p.ID)["cancellation"].(map[string]any)["refund"] == "completed"
	}, 10*time.Second, 20*time.Millisecond)
	var refunds int
	require.NoError(t, e.db.QueryRowContext(context.Background(), `SELECT count(*) FROM policy_refund`).Scan(&refunds))
	assert.Equal(t, 1, refunds, "one cancellation, one refund")
	_, refunded := e.refunds.counts("reembolso-" + p.ID)
	assert.Equal(t, 1, refunded, "two workers, one real refund")
}

func TestCancellationTheRefundWaitsForTheGatewayAndIsMadeOnceWhenItIsBack(t *testing.T) {
	e := newCancellationEnv(t, 2)
	p := e.paidToday(t, "VH-2026-000104")
	e.refunds.setDown(true)

	code, body := e.portal.do(t, http.MethodPost, "/v1/policies/"+p.ID+"/cancellation", carolina,
		confirmation(e.quote(t, p.ID)["refund_amount"]))
	require.Equal(t, http.StatusCreated, code, "%v", body)
	require.Eventually(t, func() bool {
		requests, _ := e.refunds.counts("reembolso-" + p.ID)
		return requests >= 3
	}, 10*time.Second, 10*time.Millisecond, "it keeps trying with the same key")
	assert.Equal(t, "in_process", e.detail(t, p.ID)["cancellation"].(map[string]any)["refund"],
		"the cancellation does not depend on the gateway: the refund is En proceso")

	e.refunds.setDown(false)

	require.Eventually(t, func() bool {
		return e.detail(t, p.ID)["cancellation"].(map[string]any)["refund"] == "completed"
	}, 10*time.Second, 20*time.Millisecond, "Reembolsado when the gateway is back")
	_, refunded := e.refunds.counts("reembolso-" + p.ID)
	assert.Equal(t, 1, refunded)
}

func TestCancellationARefundThatChangedOrAMonthBeingChargedWritesNothing(t *testing.T) {
	e := newCancellationEnv(t, 1)
	ctx := context.Background()
	p := e.paidToday(t, "VH-2026-000105")

	code, body := e.portal.do(t, http.MethodPost, "/v1/policies/"+p.ID+"/cancellation", carolina, confirmation(1))

	assert.Equal(t, http.StatusConflict, code)
	assert.Equal(t, "refund_changed", body["error"])
	assert.Equal(t, e.quote(t, p.ID)["refund_amount"], body["quote"].(map[string]any)["refund_amount"],
		"the amount to confirm comes back")
	cancelled, err := e.store.IsCancelled(ctx, p.ID)
	require.NoError(t, err)
	assert.False(t, cancelled)

	// A policy whose second month fell due two days ago and the sweep is
	// charging right now.
	held := e.seedPolicy(t, "VH-2026-000106", 365*24*time.Hour-32*24*time.Hour)
	now := e.clock.Now()
	i := domain.NewInstallment(held, 2, domain.DefaultInstallmentRules, now)
	_, err = e.store.CreateInstallment(ctx, i)
	require.NoError(t, err)
	claimed, err := e.store.ClaimDueAttempts(ctx, now, time.Minute, 10)
	require.NoError(t, err)
	require.Len(t, claimed, 1)
	q := e.quote(t, held.ID)
	assert.Equal(t, 0.0, q["refund_amount"], "a month in its grace has nothing paid to give back")
	assert.Equal(t, "payment_pending", q["payment_status"])

	code, body = e.portal.do(t, http.MethodPost, "/v1/policies/"+held.ID+"/cancellation", carolina, confirmation(0))

	assert.Equal(t, http.StatusConflict, code)
	assert.Equal(t, "payment_in_progress", body["error"])
	cancelled, err = e.store.IsCancelled(ctx, held.ID)
	require.NoError(t, err)
	assert.False(t, cancelled, "the sweep finishes first")
}
