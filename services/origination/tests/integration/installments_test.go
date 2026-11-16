// This file checks the acceptance criteria of the cancellation of a policy for
// non-payment (HU-W1.6-02) end to end, in process: the sweep of the monthly
// instalments over a real PostgreSQL (Testcontainers) and a payment gateway
// over HTTP, and what the insured (GET /v1/policies, GET /v1/policies/{id})
// and the advisor (GET /v1/policies/expiring) read afterwards.
package integration_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/adapters/email"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/adapters/payments"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/resilience"
	emailadapter "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/email"
	httpadapter "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/http"
	paymentsadapter "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/payments"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/postgres"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/installments"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/profiling"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/renewal"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/wallet"
)

// instalmentGateway declines, for insufficient funds, every instalment of the
// policies whose id contains "nopaga" and approves the rest.
type instalmentGateway struct {
	mu   sync.Mutex
	keys []string
	srv  *httptest.Server
}

func newInstalmentGateway(t *testing.T) *instalmentGateway {
	t.Helper()
	g := &instalmentGateway{}
	g.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Reference string `json:"reference"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		g.mu.Lock()
		g.keys = append(g.keys, r.Header.Get("Idempotency-Key"))
		g.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(in.Reference, "nopaga") {
			w.WriteHeader(http.StatusPaymentRequired)
			_, _ = w.Write([]byte(`{"error":"payment_declined","decline_code":"51","charge_id":"ch-declined"}`))
			return
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"charge_id":"ch-ok","status":"succeeded"}`))
	}))
	t.Cleanup(g.srv.Close)
	return g
}

// sentEmails records the e-mails of the cancellations.
type sentEmails struct {
	mu   sync.Mutex
	sent []email.Message
}

func (s *sentEmails) Send(_ context.Context, m email.Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sent = append(s.sent, m)
	return nil
}

type installmentEnv struct {
	*renewalEnv
	sweeper *installments.Sweeper
	gateway *instalmentGateway
	emails  *sentEmails
	portal  *acceptanceEnv
}

func newInstallmentEnv(t *testing.T) *installmentEnv {
	t.Helper()
	e := &installmentEnv{renewalEnv: newRenewalEnv(t), gateway: newInstalmentGateway(t), emails: &sentEmails{}}
	store := postgres.NewInstallmentStore(e.db)
	gw := paymentsadapter.NewGateway(payments.NewClient(payments.Config{BaseURL: e.gateway.srv.URL,
		Timeout: 500 * time.Millisecond, Breaker: resilience.Config{ConsecutiveFailures: 10_000, OpenTimeout: time.Second,
			HalfOpenSuccesses: 1}}))
	var err error
	e.sweeper, err = installments.New(installments.Config{Store: store, Methods: store, Gateway: gw,
		Notifier: emailadapter.NewCancellationNotifier(e.emails), Rules: domain.DefaultInstallmentRules, Clock: e.clock})
	require.NoError(t, err)

	policyWallet, err := wallet.NewService(e.svc, nil, e.clock)
	require.NoError(t, err)
	policyWallet = policyWallet.WithBilling(store, store, domain.DefaultInstallmentRules)
	expiring, err := renewal.NewExpiringService(postgres.NewExpiringPolicyStore(e.db), domain.DefaultRenewalPolicy, e.clock)
	require.NoError(t, err)
	srv := httptest.NewServer(httpadapter.NewRouter(profiling.NewResolver(nil, nil, e.clock),
		httpadapter.WithObservability("origination"),
		httpadapter.WithPolicyWallet(policyWallet, subjectVerifier{}),
		httpadapter.WithExpiringPolicies(expiring, subjectVerifier{})))
	t.Cleanup(srv.Close)
	e.portal = &acceptanceEnv{srv: srv}
	return e
}

// sweepAt moves the clock to at and sweeps once.
func (e *installmentEnv) sweepAt(t *testing.T, at time.Time) installments.Result {
	t.Helper()
	e.clock.advance(at.Sub(e.clock.Now()) + time.Second)
	r, err := e.sweeper.Sweep(context.Background())
	require.NoError(t, err)
	return r
}

func (e *installmentEnv) walletItem(t *testing.T, id string) map[string]any {
	t.Helper()
	code, body := e.portal.do(t, http.MethodGet, "/v1/policies", "insured:carolina", "")
	require.Equal(t, http.StatusOK, code)
	for _, it := range itemsOf(body) {
		if it["policy_id"] == id {
			return it
		}
	}
	t.Fatalf("policy %s is not in the wallet", id)
	return nil
}

// CA-1: when the grace period ends without the payment, the policy is
// cancelled automatically. CA-2: with no refund, and the insured is told why.
func TestInstallmentsCAAPolicyThatDoesNotPayIsCancelledWithNoRefundAndTheInsuredIsTold(t *testing.T) {
	e := newInstallmentEnv(t)
	rules := domain.DefaultInstallmentRules
	pays := e.seedPolicy(t, "pol-paga", 365*24*time.Hour)
	doesNot := e.seedPolicy(t, "pol-nopaga", 365*24*time.Hour)
	due := rules.DueAt(doesNot, 2)

	assert.Equal(t, 2, e.sweepAt(t, due).Billed, "the second month of each policy falls due")
	first, _ := rules.AttemptAt(due, 1)
	r := e.sweepAt(t, first)
	assert.Equal(t, 1, r.Paid)
	assert.Equal(t, 1, r.Declined)

	pending := e.walletItem(t, doesNot.ID)
	assert.Equal(t, "active", pending["status"], "the grace period keeps the coverage")
	payment := pending["payment"].(map[string]any)
	assert.Equal(t, "payment_pending", payment["status"])
	assert.EqualValues(t, 1, payment["pending"].(map[string]any)["attempts"])
	upToDate := e.walletItem(t, pays.ID)["payment"].(map[string]any)
	assert.Equal(t, "up_to_date", upToDate["status"])
	assert.NotNil(t, upToDate["next_due_at"])

	for n := 2; n <= 3; n++ {
		at, _ := rules.AttemptAt(due, n)
		assert.Equal(t, 1, e.sweepAt(t, at).Declined, "attempt %d", n)
	}
	assert.Zero(t, e.sweepAt(t, rules.GraceEndsAt(due)).Cancelled, "not before the grace period ends")
	r = e.sweepAt(t, rules.CancelsAt(due))
	assert.Equal(t, 1, r.Cancelled)
	assert.Equal(t, 1, r.Emailed)

	cancelled := e.walletItem(t, doesNot.ID)
	assert.Equal(t, "cancelled", cancelled["status"])
	assert.Equal(t, false, cancelled["can_renew"])
	c := cancelled["cancellation"].(map[string]any)
	assert.Equal(t, "non_payment", c["reason"])
	assert.Equal(t, "not_applicable", c["refund"])
	assert.EqualValues(t, 3, c["attempts"])
	assert.NotNil(t, c["email_sent_at"])
	assert.Equal(t, "active", e.walletItem(t, pays.ID)["status"], "the policy that pays is untouched")

	code, detail := e.portal.do(t, http.MethodGet, "/v1/policies/"+doesNot.ID, "insured:carolina", "")
	require.Equal(t, http.StatusOK, code)
	payments := detail["payments"].([]any)
	require.Len(t, payments, 2)
	month2 := payments[0].(map[string]any)
	assert.Equal(t, "unpaid", month2["status"])
	require.Len(t, month2["attempts"], 3)
	assert.Equal(t, "51", month2["attempts"].([]any)[2].(map[string]any)["decline_code"])
	assert.Equal(t, "issuance", payments[1].(map[string]any)["kind"])
	assert.Equal(t, "1111", detail["payment_method"].(map[string]any)["last4"])
	code, _ = e.portal.do(t, http.MethodGet, "/v1/policies/"+doesNot.ID, "insured:someone-else", "")
	assert.Equal(t, http.StatusNotFound, code, "another insured does not see it")

	var events int
	require.NoError(t, e.db.QueryRowContext(context.Background(), `SELECT count(*) FROM outbox_event
		WHERE event_type = 'PolicyCancelledForNonPayment' AND event_id = $1`,
		"policy-cancelled-non-payment-"+doesNot.ID).Scan(&events))
	assert.Equal(t, 1, events, "the push goes out from the event")
	require.Len(t, e.emails.sent, 1)
	mail := e.emails.sent[0]
	assert.Equal(t, "carolina@example.com", mail.To)
	assert.Contains(t, mail.Body, "no recibimos el pago de la cuota")
	assert.Contains(t, mail.Body, "no genera reembolso")

	code, list := e.portal.do(t, http.MethodGet, "/v1/policies/expiring?status=cancelled_non_payment", laura, "")
	require.Equal(t, http.StatusOK, code)
	assert.EqualValues(t, 1, list["cancelled_non_payment_last_30_days"])
	require.Equal(t, []string{doesNot.ID}, policyIDs(list))
	assert.NotNil(t, itemsOf(list)[0]["cancelled_at"])

	months := map[string]int{}
	rows, err := e.db.QueryContext(context.Background(), `SELECT policy_id, max(seq) FROM premium_installment
		GROUP BY policy_id`)
	require.NoError(t, err)
	for rows.Next() {
		var (
			id  string
			max int
		)
		require.NoError(t, rows.Scan(&id, &max))
		months[id] = max
	}
	require.NoError(t, rows.Err())
	_ = rows.Close()
	assert.Equal(t, 3, months[pays.ID], "the policy that pays goes on to month 3")
	assert.Equal(t, 2, months[doesNot.ID], "the grace was about month 2: month 3 was never billed")
	e.gateway.mu.Lock()
	defer e.gateway.mu.Unlock()
	for _, k := range e.gateway.keys {
		assert.NotContains(t, k, doesNot.ID+"-3-", "a cancelled policy is never charged again")
	}
}
