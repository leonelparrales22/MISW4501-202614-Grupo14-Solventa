// This file wires the premium charge of HA-D02 end to end, in process: the
// offer acceptance (HU-W1.3-01) over HTTP, the billing worker over a real
// PostgreSQL (Testcontainers), a controllable payment gateway that charges a
// key once (like the stub) and can be down, answer ambiguously or decline, and
// the real contractual issuance of HU-W1.3-03 (PDF, document directory, signed
// links and e-mail). It checks each acceptance criterion of HA-D02 over HTTP.
package integration_test

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/adapters/email"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/adapters/payments"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/resilience"
	emailadapter "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/email"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/filestore"
	httpadapter "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/http"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/links"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/memory"
	paymentsadapter "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/payments"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/pdf"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/postgres"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/billing"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/issuance"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/profiling"
)

// --- PostgreSQL: one container for the package, one database per test --------

var (
	pgOnce      sync.Once
	pgContainer *tcpostgres.PostgresContainer
	pgAdminURL  string
	pgErr       error
)

func TestMain(m *testing.M) {
	code := m.Run()
	if pgContainer != nil {
		_ = testcontainers.TerminateContainer(pgContainer)
	}
	os.Exit(code)
}

func newChargeDB(t *testing.T) *sql.DB {
	t.Helper()
	testcontainers.SkipIfProviderIsNotHealthy(t)
	ctx := context.Background()
	pgOnce.Do(func() {
		pgContainer, pgErr = tcpostgres.Run(ctx, "postgres:16-alpine",
			tcpostgres.WithDatabase("origination"), tcpostgres.WithUsername("origination"), tcpostgres.WithPassword("origination"),
			testcontainers.WithWaitStrategy(wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).WithStartupTimeout(2*time.Minute)))
		if pgErr == nil {
			pgAdminURL, pgErr = pgContainer.ConnectionString(ctx, "sslmode=disable")
		}
	})
	require.NoError(t, pgErr, "start PostgreSQL")
	admin, err := postgres.Open(ctx, pgAdminURL)
	require.NoError(t, err)
	defer func() { _ = admin.Close() }()
	b := make([]byte, 8)
	_, err = rand.Read(b)
	require.NoError(t, err)
	name := "it_" + hex.EncodeToString(b)
	_, err = admin.ExecContext(ctx, "CREATE DATABASE "+name)
	require.NoError(t, err)
	db, err := postgres.Open(ctx, strings.Replace(pgAdminURL, "/origination?", "/"+name+"?", 1))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, postgres.Migrate(ctx, db))
	return db
}

// --- Payment gateway ---------------------------------------------------------

type gatewayMode int

const (
	gatewayUp gatewayMode = iota
	// gatewayDown cuts every connection before answering: nothing is charged.
	gatewayDown
	// gatewayAmbiguous charges and records, then never answers: the caller
	// times out without knowing the charge happened.
	gatewayAmbiguous
)

// payGateway charges a key once, like a provider that honors idempotency keys
// and like stubs/payment-gateway, and counts requests and real charges.
type payGateway struct {
	mu       sync.Mutex
	mode     gatewayMode
	requests map[string]int
	charged  map[string]int
	keys     []string
	srv      *httptest.Server
}

func newPayGateway(t *testing.T) *payGateway {
	t.Helper()
	g := &payGateway{requests: map[string]int{}, charged: map[string]int{}}
	g.srv = httptest.NewServer(http.HandlerFunc(g.serve))
	t.Cleanup(g.srv.Close)
	return g
}

func (g *payGateway) serve(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Reference string `json:"reference"`
	}
	_ = json.NewDecoder(r.Body).Decode(&in)
	key := r.Header.Get("Idempotency-Key")

	g.mu.Lock()
	mode := g.mode
	if mode == gatewayDown {
		g.mu.Unlock()
		if conn, _, err := w.(http.Hijacker).Hijack(); err == nil {
			_ = conn.Close()
		}
		return
	}
	g.requests[key]++
	g.keys = append(g.keys, key)
	declined := strings.Contains(in.Reference, "declined")
	if !declined && g.charged[key] == 0 {
		g.charged[key]++ // processed before answering
	}
	g.mu.Unlock()

	if mode == gatewayAmbiguous {
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if declined {
		w.WriteHeader(http.StatusPaymentRequired)
		_, _ = w.Write([]byte(`{"error":"payment_declined","decline_code":"51"}`))
		return
	}
	w.WriteHeader(http.StatusCreated)
	_, _ = fmt.Fprintf(w, `{"charge_id":"ch-%s","status":"succeeded"}`, key)
}

func (g *payGateway) set(m gatewayMode) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.mode = m
}

func (g *payGateway) chargesOf(key string) (requests, charged int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.requests[key], g.charged[key]
}

func (g *payGateway) totalCharged() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	n := 0
	for _, c := range g.charged {
		n += c
	}
	return n
}

// flakyConsumer fails the first deliveries before handing them to the real
// contractual issuance.
type flakyConsumer struct {
	mu    sync.Mutex
	fail  int
	calls int
	next  ports.CoverageActivationHandler
}

func (f *flakyConsumer) Receive(ctx context.Context, ev domain.CoverageActivated) error {
	f.mu.Lock()
	f.calls++
	failing := f.fail > 0
	if failing {
		f.fail--
	}
	f.mu.Unlock()
	if failing {
		return fmt.Errorf("contractual document unavailable")
	}
	return f.next.Receive(ctx, ev)
}

func (f *flakyConsumer) deliveries() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// --- Environment ---------------------------------------------------------------

type chargeEnvOptions struct {
	window        time.Duration
	consumerFails int
	workers       int
	// contractAttempts bounds the notification attempts before the contractual
	// issuance fails (the service default is 12); 0 keeps the default.
	contractAttempts int
	// personalized are personalized offers (HU-W1.2-03) stored and claimed by
	// their owner before the test, next to the demo offers.
	personalized []ownedOffer
}

// ownedOffer is a personalized offer and the insured who claimed it.
type ownedOffer struct {
	offer domain.PersonalizedOffer
	owner string
}

type chargeEnv struct {
	srv       *httptest.Server
	gw        *payGateway
	mail      *mailProvider
	acc       *acceptanceEnv
	contract  *contractEnvIT
	consumer  *flakyConsumer
	contracts *postgres.ContractStore
	// service is the contractual issuance, to wake its worker.
	service *issuance.ContractService
}

func newChargeEnv(t *testing.T, o chargeEnvOptions) *chargeEnv {
	t.Helper()
	db := newChargeDB(t)
	clock := wallClock{}
	e := &chargeEnv{gw: newPayGateway(t), mail: newMailProvider(t)}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	// The real contractual issuance of HU-W1.3-03.
	store, err := filestore.New(t.TempDir())
	require.NoError(t, err)
	signer, err := links.NewSigner("0123456789abcdef0123456789abcdef")
	require.NoError(t, err)
	repo, audit := postgres.NewContractStore(db), memory.NewIssuanceAuditLog()
	e.contracts = repo
	contracts := issuance.NewContractService(issuance.ContractDeps{
		Repo: repo, Generator: pdf.New(), Store: store, Signer: signer, Audit: audit, Clock: clock,
		Notifier: emailadapter.NewNotifier(email.NewClient(email.Config{BaseURL: e.mail.server.URL, Timeout: time.Second,
			Breaker: resilience.Config{ConsecutiveFailures: 100, OpenTimeout: time.Second, HalfOpenSuccesses: 1}})),
	}, issuance.ContractConfig{PollInterval: 10 * time.Millisecond, RetryBase: 20 * time.Millisecond, RetryMax: 50 * time.Millisecond,
		MaxAttempts: o.contractAttempts, PublicBaseURL: "https://api.solventa.example"})
	e.service = contracts
	go contracts.Run(ctx)
	e.consumer = &flakyConsumer{fail: o.consumerFails, next: contracts}

	// The premium charge of HA-D02.
	window := o.window
	if window == 0 {
		window = 24 * time.Hour
	}
	policy := resilience.RetryPolicy{Base: 20 * time.Millisecond, Max: 100 * time.Millisecond, Window: window}
	charges := postgres.NewChargeStore(db)
	gateway := paymentsadapter.NewGateway(payments.NewClient(payments.Config{BaseURL: e.gw.srv.URL, Timeout: 200 * time.Millisecond,
		Breaker: resilience.Config{ConsecutiveFailures: 10_000, OpenTimeout: time.Second, HalfOpenSuccesses: 1}}))
	var workers []*billing.Worker
	for i := 0; i < max(o.workers, 1); i++ {
		w, err := billing.NewWorker(charges, gateway, clock, billing.WorkerConfig{
			PollInterval: 10 * time.Millisecond, ClaimLease: 2 * time.Second, Retry: policy,
		}, e.consumer)
		require.NoError(t, err)
		go w.Run(ctx)
		workers = append(workers, w)
	}
	notify := func() {
		for _, w := range workers {
			w.Notify()
		}
	}

	// The demo offers come from the stub and the personalized ones from their
	// store, through the router the service uses (HU-W1.2-03).
	personal := memory.NewPersonalizedOfferStore()
	for _, p := range o.personalized {
		_, _, err := personal.Create(ctx, p.offer)
		require.NoError(t, err)
		_, err = personal.Claim(ctx, p.offer.ID, p.owner)
		require.NoError(t, err)
	}
	offers, acceptances := memory.NewOfferRouter(memory.NewOfferStub(clock), personal), memory.NewAcceptanceStore()
	processor := issuance.NewProcessor(offers, acceptances, memory.NewUnderwriterStub(clock, 0, memory.DefaultReviewScoreThreshold), memory.NewReviewQueueStub(),
		billing.NewStarter(charges, billing.OfferTerms(fixtureCoverageTerms()), clock, policy, notify), clock,
		issuance.ProcessorConfig{PollInterval: 10 * time.Millisecond, RetryBase: 20 * time.Millisecond, RetryMax: 40 * time.Millisecond})
	go processor.Run(ctx)

	e.srv = httptest.NewServer(httpadapter.NewRouter(profiling.NewResolver(nil, nil, clock),
		httpadapter.WithObservability("origination"),
		httpadapter.WithAcceptances(issuance.NewAcceptanceService(offers, acceptances, clock, processor.Notify), subjectVerifier{}),
		httpadapter.WithContractDocuments(issuance.NewContractReader(repo, store, audit, signer, clock), subjectVerifier{}),
		httpadapter.WithCharges(billing.NewService(charges, clock, policy, notify).WithContractRetry(repo, contracts.Notify), subjectVerifier{})))
	t.Cleanup(e.srv.Close)
	e.acc = &acceptanceEnv{srv: e.srv}
	e.contract = &contractEnvIT{srv: e.srv, mail: e.mail, acc: e.acc}
	return e
}

// fixtureCoverageTerms are the demonstration terms the demo offers activate,
// as in the service (billing.OfferTerms, decision D-16).
func fixtureCoverageTerms() domain.CoverageTerms {
	return domain.CoverageTerms{InsuredAmount: fixtureInsuredAmount, TermMonths: fixtureTermMonths, Coverages: []domain.Coverage{
		{Code: domain.CoverageDeath, InsuredAmount: fixtureInsuredAmount},
		{Code: domain.CoverageDisability, InsuredAmount: fixtureInsuredAmount},
	}}
}

// accept accepts a demo offer and returns the acceptance id.
// accept registers the acceptance and hands over a payment method, which is
// what makes the charge due (HU-W1.3-02). The tests below are about the
// charge mechanism of HA-D02, so they start once there is something to
// charge with; the waiting state has its own test.
func (e *chargeEnv) accept(t *testing.T, customer, offer string) string {
	t.Helper()
	id := e.acceptWithoutPaying(t, customer, offer)
	e.pay(t, id, customer)
	return id
}

// acceptWithoutPaying leaves the charge where the acceptance leaves it:
// pending, with no payment method and no due date.
func (e *chargeEnv) acceptWithoutPaying(t *testing.T, customer, offer string) string {
	t.Helper()
	status, body := e.acc.do(t, http.MethodPost, "/v1/offers/"+offer+"/acceptance", "insured:"+customer, `{"channel":"web"}`)
	require.Equal(t, http.StatusAccepted, status, "the acceptance is registered whatever the gateway does")
	return body["acceptance_id"].(string)
}

// pay hands over the card of screen E4. The number never travels: only the
// token the gateway gave the browser.
func (e *chargeEnv) pay(t *testing.T, id, customer string) map[string]any {
	t.Helper()
	// Underwriting approves and stores the charge asynchronously, so the
	// payment method can arrive before there is a charge to attach it to.
	e.waitCharge(t, id, "insured:"+customer, func(map[string]any) bool { return true })
	status, body := e.acc.do(t, http.MethodPost, "/v1/acceptances/"+id+"/charge/payment-method",
		"insured:"+customer,
		`{"method":"card","payment_token":"tok_7b3c1f9a","brand":"visa","last4":"1111","installments":1}`)
	require.Equal(t, http.StatusAccepted, status, "the payment method is accepted")
	return body
}

func (e *chargeEnv) charge(t *testing.T, id, token string) (int, map[string]any) {
	t.Helper()
	code, body, _ := e.contract.get(t, "/v1/acceptances/"+id+"/charge", token)
	var m map[string]any
	if len(body) > 0 {
		require.NoError(t, json.Unmarshal(body, &m))
	}
	return code, m
}

func (e *chargeEnv) waitCharge(t *testing.T, id, token string, cond func(map[string]any) bool) map[string]any {
	t.Helper()
	var last map[string]any
	require.Eventually(t, func() bool {
		code, m := e.charge(t, id, token)
		last = m
		return code == http.StatusOK && cond(m)
	}, 15*time.Second, 10*time.Millisecond, "last charge: %v", last)
	return last
}

func (e *chargeEnv) issuanceStatus(t *testing.T, id, token string) int {
	t.Helper()
	code, _, _ := e.contract.get(t, "/v1/policies/pol-"+id+"/issuance-status", token)
	return code
}

func (e *chargeEnv) mailsFor(id string) int {
	n := 0
	for _, m := range e.mail.messages() {
		if strings.Contains(m["body"], "pol-"+id) {
			n++
		}
	}
	return n
}

func status(want string) func(map[string]any) bool {
	return func(m map[string]any) bool { return m["status"] == want }
}

func attemptsAtLeast(n float64) func(map[string]any) bool {
	return func(m map[string]any) bool { return m["status"] == "pending" && m["attempts"].(float64) >= n }
}

// --- Acceptance criteria ---------------------------------------------------------

func TestChargeCA1TheAcceptanceIsPersistedBeforeAnyCharge(t *testing.T) {
	e := newChargeEnv(t, chargeEnvOptions{})
	e.gw.set(gatewayDown)

	id := e.accept(t, "cust-1", "demo-active-ca1")

	_, acc := e.acc.do(t, http.MethodGet, "/v1/acceptances/"+id, "insured:cust-1", "")
	assert.Equal(t, id, acc["acceptance_id"], "readable while the gateway is down")
	e.acc.waitStatus(t, id, "insured:cust-1", "issuance_pending")
	c := e.waitCharge(t, id, "insured:cust-1", attemptsAtLeast(1))
	assert.Equal(t, "cobro-"+id, c["idempotency_key"])
	_, charged := e.gw.chargesOf("cobro-" + id)
	assert.Zero(t, charged, "nothing charged while the gateway is down")
}

func TestChargeCA2PendingWithGrowingRetriesTheSameKeyAndA24HourWindow(t *testing.T) {
	e := newChargeEnv(t, chargeEnvOptions{})
	e.gw.set(gatewayDown)
	id := e.accept(t, "cust-1", "demo-active-ca2")

	c := e.waitCharge(t, id, "insured:cust-1", attemptsAtLeast(4))

	assert.Equal(t, "pending", c["status"], "payment pending, the acceptance is saved")
	assert.Equal(t, "cobro-"+id, c["idempotency_key"])
	assert.NotNil(t, c["next_attempt_at"])
	assert.NotNil(t, c["last_failure_at"])
	assert.Nil(t, c["policy_id"])
	window := c["retry_window"].(map[string]any)
	started, err := time.Parse(time.RFC3339Nano, window["started_at"].(string))
	require.NoError(t, err)
	ends, err := time.Parse(time.RFC3339Nano, window["ends_at"].(string))
	require.NoError(t, err)
	assert.Equal(t, 24*time.Hour, ends.Sub(started))
	for _, role := range []string{"advisor:adv-1", "operations:ops-1"} {
		code, _ := e.charge(t, id, role)
		assert.Equal(t, http.StatusOK, code, role)
	}
	code, _ := e.charge(t, id, "insured:someone-else")
	assert.Equal(t, http.StatusForbidden, code)

	e.gw.set(gatewayUp)
	e.waitCharge(t, id, "insured:cust-1", status("confirmed"))
	requests, charged := e.gw.chargesOf("cobro-" + id)
	assert.Equal(t, 1, charged)
	assert.Equal(t, 1, requests, "a down gateway never received the earlier attempts")
	e.gw.mu.Lock()
	for _, k := range e.gw.keys {
		assert.Equal(t, "cobro-"+id, k)
	}
	e.gw.mu.Unlock()
}

func TestChargeCA3AnExpiredWindowIsAVisibleExceptionThatCanBeResumed(t *testing.T) {
	e := newChargeEnv(t, chargeEnvOptions{window: 300 * time.Millisecond})
	e.gw.set(gatewayDown)
	id := e.accept(t, "cust-1", "demo-active-ca3")

	c := e.waitCharge(t, id, "insured:cust-1", status("failed"))
	assert.Equal(t, "retry_window_expired", c["failure_reason"])
	assert.Nil(t, c["next_attempt_at"])

	code, body, _ := e.contract.get(t, "/v1/issuance-exceptions", "operations:ops-1")
	require.Equal(t, http.StatusOK, code)
	var list struct {
		Items []map[string]any `json:"items"`
	}
	require.NoError(t, json.Unmarshal(body, &list))
	require.Len(t, list.Items, 1)
	assert.Equal(t, id, list.Items[0]["acceptance_id"])
	assert.Equal(t, "premium_charge", list.Items[0]["step"])
	assert.Equal(t, "retry_window_expired", list.Items[0]["reason"])
	code, _, _ = e.contract.get(t, "/v1/issuance-exceptions", "advisor:adv-1")
	assert.Equal(t, http.StatusForbidden, code)
	assert.Equal(t, http.StatusNotFound, e.issuanceStatus(t, id, "insured:cust-1"), "no payment, no activation")

	// The monitor (HU-W1.3-04) shows the same failure as an exception, and
	// no action has been taken on it yet.
	issuances := func(query string) []map[string]any {
		code, body, _ := e.contract.get(t, "/v1/issuances"+query, "operations:ops-1")
		require.Equal(t, http.StatusOK, code, query)
		var out struct {
			Items []map[string]any `json:"items"`
		}
		require.NoError(t, json.Unmarshal(body, &out))
		return out.Items
	}
	exceptions := issuances("?status=exception")
	require.Len(t, exceptions, 1)
	assert.Equal(t, id, exceptions[0]["acceptance_id"])
	assert.Equal(t, "exception", exceptions[0]["status"])
	assert.Equal(t, "premium_charge", exceptions[0]["step"])
	assert.Equal(t, "retry_window_expired", exceptions[0]["reason"])
	assert.Empty(t, issuances("?status=active"), "an empty filter is not an error")
	code, _, _ = e.contract.get(t, "/v1/issuances", "advisor:adv-1")
	assert.Equal(t, http.StatusForbidden, code)
	code, body, _ = e.contract.get(t, "/v1/issuances/"+id+"/actions", "operations:ops-1")
	require.Equal(t, http.StatusOK, code)
	assert.JSONEq(t, `{"items":[]}`, string(body))

	status400, _ := e.acc.do(t, http.MethodPost, "/v1/acceptances/"+id+"/charge/resume", "operations:ops-1", `{"note":""}`)
	assert.Equal(t, http.StatusBadRequest, status400, "the note is required")
	e.gw.set(gatewayUp)
	code202, resumed := e.acc.do(t, http.MethodPost, "/v1/acceptances/"+id+"/charge/resume", "operations:ops-1",
		`{"note":"Pasarela restablecida, se reanuda desde la aceptacion"}`)
	require.Equal(t, http.StatusAccepted, code202)
	assert.Equal(t, "cobro-"+id, resumed["idempotency_key"], "the same key")
	assert.EqualValues(t, 1, resumed["resume_count"])

	// The resumption left its action, with author and note (CA-3).
	_, body, _ = e.contract.get(t, "/v1/issuances/"+id+"/actions", "operations:ops-1")
	var actions struct {
		Items []map[string]any `json:"items"`
	}
	require.NoError(t, json.Unmarshal(body, &actions))
	require.Len(t, actions.Items, 1)
	assert.Equal(t, "resume", actions.Items[0]["action"])
	assert.Equal(t, "ops-1", actions.Items[0]["actor"])
	assert.Equal(t, "Pasarela restablecida, se reanuda desde la aceptacion", actions.Items[0]["note"])

	e.waitCharge(t, id, "insured:cust-1", status("confirmed"))
	_, charged := e.gw.chargesOf("cobro-" + id)
	assert.Equal(t, 1, charged)
	e.contract.waitFor(t, "pol-"+id, "insured:cust-1", func(m map[string]any) bool { return m["status"] == "completed" })
	e.waitIssuance(t, id, "notified") // charged, its document ready and the insured notified
	assert.Empty(t, issuances("?status=exception"))
	code409, _ := e.acc.do(t, http.MethodPost, "/v1/acceptances/"+id+"/charge/resume", "operations:ops-1", `{"note":"otra"}`)
	assert.Equal(t, http.StatusConflict, code409, "a confirmed charge is not resumable")
}

func TestChargeCA4TheCoverageIsActivatedOnlyWithAConfirmedPayment(t *testing.T) {
	e := newChargeEnv(t, chargeEnvOptions{consumerFails: 2})
	e.gw.set(gatewayAmbiguous)
	id := e.accept(t, "cust-1", "demo-active-ca4")

	e.waitCharge(t, id, "insured:cust-1", attemptsAtLeast(2))
	assert.Equal(t, http.StatusNotFound, e.issuanceStatus(t, id, "insured:cust-1"),
		"charged by the gateway but not confirmed to origination: no activation yet")
	_, charged := e.gw.chargesOf("cobro-" + id)
	assert.Equal(t, 1, charged, "the ambiguous answers charged once")

	e.gw.set(gatewayUp)
	c := e.waitCharge(t, id, "insured:cust-1", func(m map[string]any) bool { return m["activation_published_at"] != nil })
	assert.Equal(t, "confirmed", c["status"])
	assert.Equal(t, "pol-"+id, c["policy_id"])
	assert.NotNil(t, c["coverage_activated_at"])
	st := e.contract.waitFor(t, "pol-"+id, "insured:cust-1", func(m map[string]any) bool { return m["status"] == "completed" })
	assert.Equal(t, c["coverage_activated_at"], st["activated_at"], "the document starts from the confirmed payment")

	time.Sleep(200 * time.Millisecond)
	assert.Equal(t, 1, e.mailsFor(id), "one message per policy although the consumer failed twice")
	assert.GreaterOrEqual(t, e.consumer.deliveries(), 3)
	_, charged = e.gw.chargesOf("cobro-" + id)
	assert.Equal(t, 1, charged)
}

// personalizedOffer is an offer priced by the rating engine of HU-W1.2-03,
// with its own insured amount, term and coverages.
func personalizedOffer(id string, terms domain.CoverageTerms) domain.PersonalizedOffer {
	now := time.Now().UTC()
	return domain.PersonalizedOffer{
		ID: id, CustomerID: "partner-cust-1", Channel: domain.OfferChannelEmbedded, ConsentID: "cns-1",
		PartnerID: "partner-1", ApplicationID: "app-1",
		Applicant: domain.Applicant{FullName: "Ana Perez", DocumentID: "1020304050", Email: "ana@example.com"},
		Terms:     terms, Premium: 98_400, Currency: "COP", Mode: domain.ModeNormal,
		Profile: domain.ProfileSnapshot{Score: 0.5}, CreatedAt: now, ExpiresAt: now.Add(domain.OfferValidity),
	}
}

// TestChargeOfAPersonalizedOfferActivatesItsOwnTerms covers decision D-16 of
// HA-D02: the coverage of a personalized offer is activated, and its
// contractual document issued, with the insured amount, the term and the
// coverages it was priced with, not with the demonstration terms.
func TestChargeOfAPersonalizedOfferActivatesItsOwnTerms(t *testing.T) {
	terms := domain.CoverageTerms{InsuredAmount: 180_000_000, TermMonths: 60, Coverages: []domain.Coverage{
		{Code: domain.CoverageDeath, InsuredAmount: 180_000_000},
		{Code: domain.CoverageDisability, InsuredAmount: 90_000_000},
	}}
	e := newChargeEnv(t, chargeEnvOptions{personalized: []ownedOffer{
		{offer: personalizedOffer("off-d16-own-terms", terms), owner: "cust-1"},
	}})

	id := e.accept(t, "cust-1", "off-d16-own-terms")
	c := e.waitCharge(t, id, "insured:cust-1", func(m map[string]any) bool { return m["activation_published_at"] != nil })
	assert.Equal(t, "confirmed", c["status"])
	assert.InDelta(t, 98_400, c["amount"].(float64), 0.001, "the premium of the offer")
	e.contract.waitFor(t, "pol-"+id, "insured:cust-1", func(m map[string]any) bool { return m["status"] == "completed" })

	issued, err := e.contracts.Get(context.Background(), "pol-"+id)
	require.NoError(t, err)
	ev := issued.Event
	assert.InDelta(t, 180_000_000, ev.InsuredAmount, 0.001)
	assert.Equal(t, terms.Coverages, ev.Coverages)
	assert.Equal(t, ev.CoverageStart.AddDate(0, 60, 0), ev.CoverageEnd, "the 60 months of the offer, not the 120 demonstration months")

	code, pdf, _ := e.contract.get(t, "/v1/policies/pol-"+id+"/contract-document", "insured:cust-1")
	require.Equal(t, http.StatusOK, code)
	text := string(pdf)
	assert.Contains(t, text, "COP 180.000.000", "insured amount of the offer")
	assert.Contains(t, text, "COP 90.000.000", "coverage of the offer")
	assert.Contains(t, text, ev.CoverageEnd.UTC().Format("02/01/2006"), "validity from the term of the offer")
	assert.NotContains(t, text, "COP 250.000.000", "no demonstration amount")
}

// TestChargeOfAPersonalizedOfferWithoutTermsIsNeverCharged: decision D-16
// never invents the terms of a personalized offer; its acceptance stays
// registered and nothing is charged or issued.
func TestChargeOfAPersonalizedOfferWithoutTermsIsNeverCharged(t *testing.T) {
	e := newChargeEnv(t, chargeEnvOptions{personalized: []ownedOffer{
		{offer: personalizedOffer("off-d16-no-terms", domain.CoverageTerms{}), owner: "cust-1"},
	}})

	id := e.acceptWithoutPaying(t, "cust-1", "off-d16-no-terms")

	assert.Never(t, func() bool {
		code, _ := e.charge(t, id, "insured:cust-1")
		return code != http.StatusNotFound
	}, 600*time.Millisecond, 50*time.Millisecond, "no charge is stored without the real terms")
	assert.Zero(t, e.gw.totalCharged())
	assert.Equal(t, http.StatusNotFound, e.issuanceStatus(t, id, "insured:cust-1"))
	_, acc := e.acc.do(t, http.MethodGet, "/v1/acceptances/"+id, "insured:cust-1", "")
	assert.Equal(t, id, acc["acceptance_id"], "the acceptance stays registered")
}

func TestChargeCA5EveryAcceptanceDuringTheOutageEndsInExactlyOneCharge(t *testing.T) {
	e := newChargeEnv(t, chargeEnvOptions{workers: 3, consumerFails: 3})
	const down, ambiguous = 12, 8
	var ids []string
	e.gw.set(gatewayDown)
	for i := 0; i < down; i++ {
		ids = append(ids, e.accept(t, fmt.Sprintf("cust-d%02d", i), fmt.Sprintf("demo-active-ca5d%02d", i)))
	}
	e.gw.set(gatewayAmbiguous)
	for i := 0; i < ambiguous; i++ {
		ids = append(ids, e.accept(t, fmt.Sprintf("cust-a%02d", i), fmt.Sprintf("demo-active-ca5a%02d", i)))
	}
	for i, id := range ids {
		customer := fmt.Sprintf("insured:cust-d%02d", i)
		if i >= down {
			customer = fmt.Sprintf("insured:cust-a%02d", i-down)
		}
		e.waitCharge(t, id, customer, attemptsAtLeast(1))
	}

	e.gw.set(gatewayUp)

	for i, id := range ids {
		customer := fmt.Sprintf("cust-d%02d", i)
		if i >= down {
			customer = fmt.Sprintf("cust-a%02d", i-down)
		}
		e.waitCharge(t, id, "insured:"+customer, func(m map[string]any) bool { return m["activation_published_at"] != nil })
		e.contract.waitFor(t, "pol-"+id, "insured:"+customer, func(m map[string]any) bool { return m["status"] == "completed" })
	}
	time.Sleep(200 * time.Millisecond)
	for _, id := range ids {
		_, charged := e.gw.chargesOf("cobro-" + id)
		assert.Equal(t, 1, charged, "exactly one real charge for %s", id)
		assert.Equal(t, 1, e.mailsFor(id), "one contractual notification for %s", id)
	}
	assert.Equal(t, down+ambiguous, e.gw.totalCharged(), "none lost, none duplicated")
}

func TestChargeADeclinedPaymentFailsWithoutRetriesOrIssuance(t *testing.T) {
	e := newChargeEnv(t, chargeEnvOptions{})
	id := e.accept(t, "cust-1", "demo-active-declined01")

	c := e.waitCharge(t, id, "insured:cust-1", status("failed"))

	assert.Equal(t, "payment_rejected", c["failure_reason"])
	time.Sleep(150 * time.Millisecond)
	requests, charged := e.gw.chargesOf("cobro-" + id)
	assert.Equal(t, 1, requests, "a decline is not retried")
	assert.Zero(t, charged)
	assert.Equal(t, http.StatusNotFound, e.issuanceStatus(t, id, "insured:cust-1"))
	code, _, _ := e.contract.get(t, "/v1/issuance-exceptions", "operations:ops-1")
	assert.Equal(t, http.StatusOK, code)
}

func TestChargeOfAnAcceptanceInReviewDoesNotExist(t *testing.T) {
	e := newChargeEnv(t, chargeEnvOptions{})
	_, body := e.acc.do(t, http.MethodPost, "/v1/offers/demo-review-ca/acceptance", "insured:cust-1", `{"channel":"web"}`)
	id := body["acceptance_id"].(string)
	e.acc.waitStatus(t, id, "insured:cust-1", "in_review")

	code, m := e.charge(t, id, "insured:cust-1")

	assert.Equal(t, http.StatusNotFound, code)
	assert.Equal(t, "charge_not_found", m["error"])
	assert.Zero(t, e.gw.totalCharged())
}

// --- Payment method (HU-W1.3-02) -------------------------------------------

// Nothing is charged until the customer hands over a payment method in screen
// E4. The acceptance is still the no-loss point: the charge exists from the
// moment underwriting approves, it just has no due date yet.
func TestChargeWaitsForThePaymentMethodOfScreenE4(t *testing.T) {
	e := newChargeEnv(t, chargeEnvOptions{})

	id := e.acceptWithoutPaying(t, "cust-1", "demo-active-wait")

	c := e.waitCharge(t, id, "insured:cust-1", func(map[string]any) bool { return true })
	assert.Equal(t, "pending", c["status"], "the charge exists from the acceptance")
	assert.Nil(t, c["payment_method"], "there is no payment method yet: screen E4")
	assert.Nil(t, c["next_attempt_at"], "and no due date, so the worker leaves it alone")
	assert.EqualValues(t, 0, c["payment_attempt"])
	_, charged := e.gw.chargesOf("cobro-" + id)
	assert.Zero(t, charged, "the gateway is never called without a payment method")
}

func TestThePaymentMethodMakesTheChargeDueAndItIsCharged(t *testing.T) {
	e := newChargeEnv(t, chargeEnvOptions{})
	id := e.acceptWithoutPaying(t, "cust-1", "demo-active-pay")

	body := e.pay(t, id, "cust-1")

	method, ok := body["payment_method"].(map[string]any)
	require.True(t, ok, "the answer carries the payment method: %v", body)
	assert.Equal(t, "card", method["method"])
	assert.Equal(t, "1111", method["last4"], "enough to show Visa ...1111, never the number")
	assert.EqualValues(t, 1, body["payment_attempt"])
	e.waitCharge(t, id, "insured:cust-1", func(m map[string]any) bool { return m["status"] == "confirmed" })
}

// Only the owner pays: an advisor or an analyst does not pay for someone else.
func TestOnlyTheOwnerHandsOverThePaymentMethod(t *testing.T) {
	e := newChargeEnv(t, chargeEnvOptions{})
	id := e.acceptWithoutPaying(t, "cust-1", "demo-active-owner")
	e.waitCharge(t, id, "insured:cust-1", func(map[string]any) bool { return true })

	const card = `{"method":"card","payment_token":"tok_7b3c1f9a","brand":"visa","last4":"1111","installments":1}`
	for _, caller := range []string{"insured:someone-else", "advisor:adv-1", "operations:ops-1"} {
		status, _ := e.acc.do(t, http.MethodPost, "/v1/acceptances/"+id+"/charge/payment-method", caller, card)
		assert.Equal(t, http.StatusForbidden, status, "caller %s", caller)
	}
}

// A card number where the token goes means the client did not tokenise: it
// must fail loudly instead of dragging a PAN into the service.
func TestACardNumberInsteadOfATokenIsRefused(t *testing.T) {
	e := newChargeEnv(t, chargeEnvOptions{})
	id := e.acceptWithoutPaying(t, "cust-1", "demo-active-pan")
	e.waitCharge(t, id, "insured:cust-1", func(map[string]any) bool { return true })

	status, body := e.acc.do(t, http.MethodPost, "/v1/acceptances/"+id+"/charge/payment-method", "insured:cust-1",
		`{"method":"card","payment_token":"4111111111111111","installments":1}`)

	assert.Equal(t, http.StatusBadRequest, status)
	assert.Equal(t, "invalid_request", body["error"])
}

// A confirmed charge takes no other card.
func TestAConfirmedChargeTakesNoPaymentMethod(t *testing.T) {
	e := newChargeEnv(t, chargeEnvOptions{})
	id := e.accept(t, "cust-1", "demo-active-done")
	e.waitCharge(t, id, "insured:cust-1", func(m map[string]any) bool { return m["status"] == "confirmed" })

	status, body := e.acc.do(t, http.MethodPost, "/v1/acceptances/"+id+"/charge/payment-method", "insured:cust-1",
		`{"method":"card","payment_token":"tok_other","brand":"visa","last4":"4242","installments":1}`)

	assert.Equal(t, http.StatusConflict, status)
	assert.Equal(t, "charge_not_payable", body["error"])
}

// --- Monitoring of the issuances (HU-W1.3-04) -----------------------------------

// monitor reads /v1/issuances as operations: the items and the counts.
func (e *chargeEnv) monitor(t *testing.T, query string) (items []map[string]any, counts map[string]float64) {
	t.Helper()
	code, body, _ := e.contract.get(t, "/v1/issuances"+query, "operations:ops-1")
	require.Equal(t, http.StatusOK, code, query)
	var out struct {
		Items  []map[string]any   `json:"items"`
		Counts map[string]float64 `json:"counts"`
	}
	require.NoError(t, json.Unmarshal(body, &out))
	return out.Items, out.Counts
}

func (e *chargeEnv) waitIssuance(t *testing.T, id string, status string) map[string]any {
	t.Helper()
	var found map[string]any
	require.Eventually(t, func() bool {
		items, _ := e.monitor(t, "?status="+status)
		for _, i := range items {
			if i["acceptance_id"] == id {
				found = i
				return true
			}
		}
		return false
	}, 15*time.Second, 20*time.Millisecond, "issuance %s never became %s", id, status)
	return found
}

func TestMonitorAChargedPolicyEndsNotifiedAndTheCountsSayToday(t *testing.T) {
	e := newChargeEnv(t, chargeEnvOptions{})
	id := e.accept(t, "cust-1", "demo-active-monitor1")

	notified := e.waitIssuance(t, id, "notified")

	assert.Equal(t, "completed", notified["step"])
	assert.Equal(t, "pol-"+id, notified["policy_id"])
	assert.NotNil(t, notified["notified_at"])
	assert.Equal(t, "web", notified["channel"])
	assert.False(t, notified["unattended"].(bool))
	_, counts := e.monitor(t, "?status=exception")
	assert.EqualValues(t, 1, counts["active_today"], "the counts do not depend on the filter")
	assert.EqualValues(t, 1, counts["notified_today"])
	assert.Zero(t, counts["exceptions"])
}

func TestMonitorAMailThatKeepsFailingIsAnExceptionAnalystsCanRetry(t *testing.T) {
	e := newChargeEnv(t, chargeEnvOptions{contractAttempts: 3})
	e.mail.setDown(true)
	id := e.accept(t, "cust-1", "demo-active-monitor2")

	exception := e.waitIssuance(t, id, "exception")

	assert.Equal(t, "notification", exception["step"], "the document exists; the notification failed")
	assert.Equal(t, "notification_failed", exception["reason"])
	assert.EqualValues(t, 3, exception["attempts"], "the attempts of the contractual issuance")
	assert.NotNil(t, exception["failed_at"])
	_, counts := e.monitor(t, "")
	assert.EqualValues(t, 1, counts["exceptions"])
	assert.EqualValues(t, 1, counts["active_today"], "the coverage is active even though the notice failed")

	// The retry is one route for any failed step, with a note (CA-3).
	status400, _ := e.acc.do(t, http.MethodPost, "/v1/issuances/"+id+"/retry", "operations:ops-1", `{"note":""}`)
	assert.Equal(t, http.StatusBadRequest, status400)
	status403, _ := e.acc.do(t, http.MethodPost, "/v1/issuances/"+id+"/retry", "advisor:adv-1", `{"note":"x"}`)
	assert.Equal(t, http.StatusForbidden, status403)
	e.mail.setDown(false)
	status202, retried := e.acc.do(t, http.MethodPost, "/v1/issuances/"+id+"/retry", "operations:ops-1", `{"note":"El correo volvió"}`)
	require.Equal(t, http.StatusAccepted, status202)
	assert.Equal(t, "retry", retried["action"], "what failed was the contractual issuance, not the charge")

	notified := e.waitIssuance(t, id, "notified")
	assert.Equal(t, "completed", notified["step"])
	assert.Len(t, e.mail.messages(), 1, "one notice reached the insured, not one per attempt")

	_, body, _ := e.contract.get(t, "/v1/issuances/"+id+"/actions", "operations:ops-1")
	var actions struct {
		Items []map[string]any `json:"items"`
	}
	require.NoError(t, json.Unmarshal(body, &actions))
	require.Len(t, actions.Items, 1)
	assert.Equal(t, "retry", actions.Items[0]["action"])
	assert.Equal(t, "ops-1", actions.Items[0]["actor"])
	assert.Equal(t, "El correo volvió", actions.Items[0]["note"])

	status409, _ := e.acc.do(t, http.MethodPost, "/v1/issuances/"+id+"/retry", "operations:ops-1", `{"note":"otra vez"}`)
	assert.Equal(t, http.StatusConflict, status409, "nothing is failed any more (CA-7: no double notice)")
	status404, _ := e.acc.do(t, http.MethodPost, "/v1/issuances/acc-none/retry", "operations:ops-1", `{"note":"x"}`)
	assert.Equal(t, http.StatusNotFound, status404)
}
