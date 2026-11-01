// This file wires the online renewal of a policy (HU-W1.5-02) end to end, in
// process: the policy projection and the renewal over HTTP, the real billing
// worker over a real PostgreSQL (Testcontainers) and a controllable payment
// gateway. It checks each acceptance criterion of the story.
package integration_test

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
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
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/profiling"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/rating"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/renewal"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/wallet"
)

// shiftClock is the wall clock moved forward by the test, shared by the
// service and the charge worker so a deadline can be crossed without waiting.
type shiftClock struct{ offset atomic.Int64 }

func (c *shiftClock) Now() time.Time          { return time.Now().Add(time.Duration(c.offset.Load())) }
func (c *shiftClock) advance(d time.Duration) { c.offset.Add(int64(d)) }

type renewalCustomers struct{}

func (renewalCustomers) GetBySubject(_ context.Context, sub string) (domain.Customer, error) {
	if sub != "carolina" {
		return domain.Customer{}, ports.ErrCustomerNotFound
	}
	return domain.Customer{ID: "cust-carolina", CognitoSub: sub, BirthDate: time.Date(1985, 5, 1, 0, 0, 0, 0, time.UTC)}, nil
}

type renewalProfiles struct{}

func (renewalProfiles) Resolve(context.Context, string, bool) (domain.ProfileResolution, error) {
	return domain.ProfileResolution{Profile: domain.RiskProfile{Score: 0.4, Category: "medium"}, Mode: domain.ModeNormal}, nil
}

type noticeLog struct {
	mu      sync.Mutex
	notices []domain.RenewalNotice
}

func (l *noticeLog) Notify(_ context.Context, n domain.RenewalNotice) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.notices = append(l.notices, n)
	return nil
}

func (l *noticeLog) all() []domain.RenewalNotice {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]domain.RenewalNotice(nil), l.notices...)
}

type renewalEnv struct {
	srv      *httptest.Server
	acc      *acceptanceEnv
	gw       *payGateway
	clock    *shiftClock
	svc      *renewal.Service
	db       *sql.DB
	policies *postgres.PolicyStore
	charges  *postgres.ChargeStore
	notices  *noticeLog
}

func newRenewalEnv(t *testing.T) *renewalEnv {
	t.Helper()
	db := newChargeDB(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	e := &renewalEnv{gw: newPayGateway(t), clock: &shiftClock{}, notices: &noticeLog{}}
	e.db = db
	e.policies, e.charges = postgres.NewPolicyStore(db), postgres.NewChargeStore(db)

	policy := resilience.RetryPolicy{Base: 20 * time.Millisecond, Max: 100 * time.Millisecond, Window: 24 * time.Hour}
	gateway := paymentsadapter.NewGateway(payments.NewClient(payments.Config{BaseURL: e.gw.srv.URL, Timeout: 200 * time.Millisecond,
		Breaker: resilience.Config{ConsecutiveFailures: 10_000, OpenTimeout: time.Second, HalfOpenSuccesses: 1}}))
	worker, err := billing.NewWorker(e.charges, gateway, e.clock, billing.WorkerConfig{
		PollInterval: 10 * time.Millisecond, ClaimLease: 2 * time.Second, Retry: policy,
	}, renewal.NewProjector(e.policies))
	require.NoError(t, err)
	go worker.Run(ctx)

	e.svc, err = renewal.NewService(renewal.Config{
		Policies: e.policies, Renewals: postgres.NewRenewalStore(db), Charges: e.charges,
		Customers: renewalCustomers{}, Profiles: renewalProfiles{}, Rater: rating.New(),
		Notifier: e.notices, Rules: domain.DefaultRenewalPolicy, Clock: e.clock, Wake: worker.Notify,
	})
	require.NoError(t, err)
	// HU-M1.2-01: the list of the policies is the wallet's GET /v1/policies,
	// here with origination's policies as its only source.
	policyWallet, err := wallet.NewService(e.svc, nil, e.clock)
	require.NoError(t, err)

	e.srv = httptest.NewServer(httpadapter.NewRouter(profiling.NewResolver(nil, nil, e.clock),
		httpadapter.WithObservability("origination"),
		httpadapter.WithPolicies(e.svc, subjectVerifier{}),
		httpadapter.WithPolicyWallet(policyWallet, subjectVerifier{}),
		httpadapter.WithCharges(billing.NewService(e.charges, e.clock, policy, worker.Notify), subjectVerifier{})))
	t.Cleanup(e.srv.Close)
	e.acc = &acceptanceEnv{srv: e.srv}
	return e
}

// seedPolicy stores an active policy of carolina that ends in endsIn, with the
// card she paid it with, as the activation of its charge leaves it.
func (e *renewalEnv) seedPolicy(t *testing.T, id string, endsIn time.Duration) domain.Policy {
	t.Helper()
	ctx := context.Background()
	now := e.clock.Now().UTC().Truncate(time.Microsecond)
	p := domain.Policy{
		ID: id, AcceptanceID: "acc-" + id, CustomerID: "carolina",
		Holder:  domain.Applicant{FullName: "Carolina Pérez", DocumentID: "1032456789", Email: "carolina@example.com"},
		Premium: 142_300, Currency: "COP", InsuredAmount: 68_000_000,
		Coverages:     []domain.Coverage{{Code: domain.CoverageDeath, InsuredAmount: 68_000_000}},
		CoverageStart: now.Add(endsIn).AddDate(-1, 0, 0), CoverageEnd: now.Add(endsIn),
	}
	require.NoError(t, e.policies.Create(ctx, p))
	// The charge the policy was paid with, to keep its registered card.
	c := domain.NewCharge(domain.Acceptance{ID: p.AcceptanceID, Channel: domain.ChannelWeb, AcceptedAt: now},
		domain.Offer{ID: "off-" + id, CustomerID: "carolina", Applicant: p.Holder, Premium: p.Premium, Currency: p.Currency},
		domain.CoverageTerms{InsuredAmount: p.InsuredAmount, TermMonths: 12, Coverages: p.Coverages}, now, now.Add(time.Hour))
	_, _, err := e.charges.CreatePending(ctx, c)
	require.NoError(t, err)
	_, err = e.charges.AttachPaymentMethod(ctx, p.AcceptanceID,
		domain.PaymentMethod{Kind: domain.PaymentCard, Token: "tok_7b3c1f9a", Brand: "visa", Last4: "1111", Installments: 1}, now)
	require.NoError(t, err)
	// That charge is already paid: the worker must not charge it again.
	_, err = e.db.ExecContext(ctx, `UPDATE charge_attempt SET status = 'confirmed', next_attempt_at = NULL, confirmed_at = $2,
		policy_id = $3, coverage_start = $4, coverage_end = $5, activation_published_at = $2 WHERE acceptance_id = $1`,
		p.AcceptanceID, now, p.ID, p.CoverageStart, p.CoverageEnd)
	require.NoError(t, err)
	return p
}

func (e *renewalEnv) do(t *testing.T, method, path, token, body string) (int, map[string]any) {
	t.Helper()
	return e.acc.do(t, method, path, token, body)
}

const carolina = "insured:carolina"

func (e *renewalEnv) waitRenewal(t *testing.T, policyID, want string) map[string]any {
	t.Helper()
	var last map[string]any
	require.Eventually(t, func() bool {
		code, body := e.do(t, http.MethodGet, "/v1/policies/"+policyID+"/renewal/status", carolina, "")
		last = body
		return code == http.StatusOK && body["renewal"].(map[string]any)["status"] == want
	}, 15*time.Second, 20*time.Millisecond, "last renewal: %v", last)
	return last
}

// CA 1: inside the window the insured sees the current and the renewed
// conditions, re-priced, before accepting.
func TestRenewalCA1TheInsuredSeesTheRenewedConditionsBeforeAccepting(t *testing.T) {
	e := newRenewalEnv(t)
	p := e.seedPolicy(t, "pol-r1", 10*24*time.Hour)

	code, body := e.do(t, http.MethodGet, "/v1/policies/"+p.ID+"/renewal", carolina, "")
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, true, body["requoted"], "the engine re-priced the policy with the current profile")
	assert.InDelta(t, domain.DiscountRate, body["discount_rate"], 1e-9)
	current, renewed := body["current"].(map[string]any), body["renewal"].(map[string]any)
	assert.InDelta(t, 142_300, current["premium"], 0.001)
	assert.Greater(t, renewed["premium"].(float64), 0.0)
	assert.NotEqual(t, current["premium"], renewed["premium"])
	assert.Equal(t, current["coverage_end"], renewed["coverage_start"], "the renewed coverage starts when the current one ends")
	method := body["payment_method"].(map[string]any)
	assert.Equal(t, "1111", method["last4"])
	assert.NotContains(t, method, "payment_token", "the token never leaves the service")

	_, list := e.do(t, http.MethodGet, "/v1/policies", carolina, "")
	items := list["items"].([]any)
	require.Len(t, items, 1)
	assert.Equal(t, true, items[0].(map[string]any)["can_renew"])
}

// CA 2: accepting charges and issues the renewed policy with no gap.
func TestRenewalCA2AcceptingChargesAndExtendsTheCoverageWithoutAGap(t *testing.T) {
	e := newRenewalEnv(t)
	p := e.seedPolicy(t, "pol-r2", 10*24*time.Hour)

	code, body := e.do(t, http.MethodPost, "/v1/policies/"+p.ID+"/renewal", carolina, `{}`)
	require.Equal(t, http.StatusCreated, code)
	chargeID := body["renewal"].(map[string]any)["charge_id"].(string)

	done := e.waitRenewal(t, p.ID, "renewed")
	newID := done["renewal"].(map[string]any)["new_policy_id"].(string)
	renewed, err := e.policies.Get(context.Background(), newID)
	require.NoError(t, err)
	assert.Equal(t, p.ID, renewed.RenewalOf)
	assert.True(t, renewed.CoverageStart.Equal(p.CoverageEnd), "no gap and no overlap: %s vs %s", renewed.CoverageStart, p.CoverageEnd)
	assert.True(t, renewed.CoverageEnd.Equal(p.CoverageEnd.AddDate(0, 12, 0)))
	assert.Equal(t, domain.ChannelRenewal, mustCharge(t, e, chargeID).Channel)

	// The same acceptance again is the same renewal: one charge in total.
	code, again := e.do(t, http.MethodPost, "/v1/policies/"+p.ID+"/renewal", carolina, `{}`)
	require.Equal(t, http.StatusOK, code, "accepting again answers the renewal that exists")
	assert.Equal(t, chargeID, again["renewal"].(map[string]any)["charge_id"])
	assert.Equal(t, 1, e.gw.totalCharged(), "the premium was charged once")
}

func mustCharge(t *testing.T, e *renewalEnv, id string) domain.Charge {
	t.Helper()
	c, err := e.charges.Get(context.Background(), id)
	require.NoError(t, err)
	return c
}

// CA 3: outside the window renewing is not available.
func TestRenewalCA3OutsideTheWindowTheRenewalIsNotAvailable(t *testing.T) {
	e := newRenewalEnv(t)
	p := e.seedPolicy(t, "pol-r3", 90*24*time.Hour)

	code, body := e.do(t, http.MethodGet, "/v1/policies/"+p.ID+"/renewal", carolina, "")
	assert.Equal(t, http.StatusConflict, code)
	assert.Equal(t, "renewal_not_available", body["error"])
	code, body = e.do(t, http.MethodPost, "/v1/policies/"+p.ID+"/renewal", carolina, `{}`)
	assert.Equal(t, http.StatusConflict, code)
	assert.Equal(t, "renewal_not_available", body["error"])

	_, list := e.do(t, http.MethodGet, "/v1/policies", carolina, "")
	assert.Equal(t, false, list["items"].([]any)[0].(map[string]any)["can_renew"])
}

// CA 4 and 6: a declined payment is a visible exception that keeps the
// request; the insured retries with another method inside the deadline, with
// no new request, and past the deadline the policy is not renewed.
func TestRenewalCA4AFailedPaymentKeepsTheRequestAndCanBeRetriedUntilTheDeadline(t *testing.T) {
	e := newRenewalEnv(t)
	e.gw.setDeclineFirst(true)
	p := e.seedPolicy(t, "pol-r4", 10*24*time.Hour)

	_, accepted := e.do(t, http.MethodPost, "/v1/policies/"+p.ID+"/renewal", carolina, `{}`)
	chargeID := accepted["renewal"].(map[string]any)["charge_id"].(string)
	e.acc.do(t, http.MethodGet, "/v1/acceptances/"+chargeID+"/charge", carolina, "")
	require.Eventually(t, func() bool {
		return mustCharge(t, e, chargeID).Status == domain.ChargeFailed
	}, 15*time.Second, 20*time.Millisecond)

	// Operations see it in the monitor, as a renewal.
	code, mon := e.do(t, http.MethodGet, "/v1/issuances?status=exception&channel=renewal", "operations:ops", "")
	require.Equal(t, http.StatusOK, code)
	require.Len(t, mon["items"].([]any), 1, "the declined renewal is a visible exception")
	_, status := e.do(t, http.MethodGet, "/v1/policies/"+p.ID+"/renewal/status", carolina, "")
	assert.Equal(t, "accepted", status["renewal"].(map[string]any)["status"], "the request is not lost")

	// Another card, same request.
	code, _ = e.do(t, http.MethodPost, "/v1/acceptances/"+chargeID+"/charge/payment-method", carolina,
		`{"method":"card","payment_token":"tok_other","brand":"mastercard","last4":"4444","installments":1}`)
	require.Equal(t, http.StatusAccepted, code)
	done := e.waitRenewal(t, p.ID, "renewed")
	assert.Equal(t, chargeID, done["renewal"].(map[string]any)["charge_id"], "no new request was created")

	_, list := e.do(t, http.MethodGet, "/v1/policies", carolina, "")
	assert.Len(t, list["items"].([]any), 2, "the old and the renewed policy, with no duplicate")
}

func TestRenewalCA4PastTheDeadlineTheRetryIsRefusedAndThePolicyIsNotRenewed(t *testing.T) {
	e := newRenewalEnv(t)
	e.gw.setDeclineFirst(true)
	p := e.seedPolicy(t, "pol-r5", 10*24*time.Hour)

	_, accepted := e.do(t, http.MethodPost, "/v1/policies/"+p.ID+"/renewal", carolina, `{}`)
	chargeID := accepted["renewal"].(map[string]any)["charge_id"].(string)
	require.Eventually(t, func() bool { return mustCharge(t, e, chargeID).Status == domain.ChargeFailed }, 15*time.Second, 20*time.Millisecond)

	e.clock.advance(domain.DefaultRenewalPolicy.RetryWithin + time.Minute)
	code, body := e.do(t, http.MethodPost, "/v1/acceptances/"+chargeID+"/charge/payment-method", carolina,
		`{"method":"card","payment_token":"tok_other","brand":"mastercard","last4":"4444","installments":1}`)
	assert.Equal(t, http.StatusConflict, code)
	assert.Equal(t, "charge_not_payable", body["error"])

	_, err := e.svc.Sweep(context.Background())
	require.NoError(t, err)
	_, status := e.do(t, http.MethodGet, "/v1/policies/"+p.ID+"/renewal/status", carolina, "")
	renewalBody := status["renewal"].(map[string]any)
	assert.Equal(t, "not_renewed", renewalBody["status"])
	assert.Equal(t, "payment_failed", renewalBody["reason"])
}

// CA 5: no answer before the window closes: not renewed, and the insured is told once.
func TestRenewalCA5NoAnswerBeforeTheWindowClosesIsNotRenewedAndNotified(t *testing.T) {
	e := newRenewalEnv(t)
	p := e.seedPolicy(t, "pol-r6", 2*time.Hour)

	e.clock.advance(3 * time.Hour)
	recorded, err := e.svc.Sweep(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, recorded)

	_, list := e.do(t, http.MethodGet, "/v1/policies", carolina, "")
	item := list["items"].([]any)[0].(map[string]any)
	assert.Equal(t, false, item["can_renew"])
	assert.Equal(t, "not_renewed", item["renewal"].(map[string]any)["status"])
	assert.Equal(t, "no_response", item["renewal"].(map[string]any)["reason"])

	notices := e.notices.all()
	require.Len(t, notices, 1)
	assert.Equal(t, "carolina@example.com", notices[0].To)
	assert.Equal(t, p.ID, notices[0].PolicyID)

	_, err = e.svc.Sweep(context.Background())
	require.NoError(t, err)
	assert.Len(t, e.notices.all(), 1, "the notice is sent once")

	code, body := e.do(t, http.MethodPost, "/v1/policies/"+p.ID+"/renewal", carolina, `{}`)
	assert.Equal(t, http.StatusConflict, code)
	assert.Equal(t, "renewal_not_available", body["error"])
}

func TestRenewalOnlyTheOwnerInsuredSeesAndRenewsAPolicy(t *testing.T) {
	e := newRenewalEnv(t)
	p := e.seedPolicy(t, "pol-r7", 10*24*time.Hour)

	for _, token := range []string{"insured:mallory", "advisor:ana", "operations:ops"} {
		code, _ := e.do(t, http.MethodPost, "/v1/policies/"+p.ID+"/renewal", token, `{}`)
		assert.Contains(t, []int{http.StatusForbidden, http.StatusUnauthorized}, code, token)
	}
	_, list := e.do(t, http.MethodGet, "/v1/policies", "insured:mallory", "")
	assert.Empty(t, list["items"])
	code, _ := e.do(t, http.MethodGet, "/v1/policies/pol-unknown/renewal", carolina, "")
	assert.Equal(t, http.StatusNotFound, code)
}

// A renewal accepted at the same moment from two tabs is one renewal.
func TestRenewalTwoConcurrentAcceptancesAreOneRenewalAndOneCharge(t *testing.T) {
	e := newRenewalEnv(t)
	p := e.seedPolicy(t, "pol-r8", 10*24*time.Hour)

	var wg sync.WaitGroup
	codes := make([]int, 2)
	for i := range codes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			codes[i], _ = e.do(t, http.MethodPost, "/v1/policies/"+p.ID+"/renewal", carolina, `{}`)
		}()
	}
	wg.Wait()
	assert.Contains(t, codes, http.StatusCreated)
	e.waitRenewal(t, p.ID, "renewed")
	assert.Equal(t, 1, e.gw.totalCharged())
}
