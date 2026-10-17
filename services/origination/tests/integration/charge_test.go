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
}

type chargeEnv struct {
	srv      *httptest.Server
	gw       *payGateway
	mail     *mailProvider
	acc      *acceptanceEnv
	contract *contractEnvIT
	consumer *flakyConsumer
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
	repo, audit := memory.NewContractStore(), memory.NewIssuanceAuditLog()
	contracts := issuance.NewContractService(issuance.ContractDeps{
		Repo: repo, Generator: pdf.New(), Store: store, Signer: signer, Audit: audit, Clock: clock,
		Notifier: emailadapter.NewNotifier(email.NewClient(email.Config{BaseURL: e.mail.server.URL, Timeout: time.Second,
			Breaker: resilience.Config{ConsecutiveFailures: 100, OpenTimeout: time.Second, HalfOpenSuccesses: 1}})),
	}, issuance.ContractConfig{PollInterval: 10 * time.Millisecond, RetryBase: 20 * time.Millisecond, RetryMax: 50 * time.Millisecond,
		PublicBaseURL: "https://api.solventa.example"})
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

	offers, acceptances := memory.NewOfferStub(clock), memory.NewAcceptanceStore()
	processor := issuance.NewProcessor(offers, acceptances, memory.NewUnderwriterStub(clock, 0, memory.DefaultReviewScoreThreshold), memory.NewReviewQueueStub(),
		billing.NewStarter(charges, chargeTerms, clock, policy, notify), clock,
		issuance.ProcessorConfig{PollInterval: 10 * time.Millisecond, RetryBase: 20 * time.Millisecond, RetryMax: 40 * time.Millisecond})
	go processor.Run(ctx)

	e.srv = httptest.NewServer(httpadapter.NewRouter(profiling.NewResolver(nil, nil, clock),
		httpadapter.WithObservability("origination"),
		httpadapter.WithAcceptances(issuance.NewAcceptanceService(offers, acceptances, clock, processor.Notify), subjectVerifier{}),
		httpadapter.WithContractDocuments(issuance.NewContractReader(repo, store, audit, signer, clock), subjectVerifier{}),
		httpadapter.WithCharges(billing.NewService(charges, clock, policy, notify), subjectVerifier{})))
	t.Cleanup(e.srv.Close)
	e.acc = &acceptanceEnv{srv: e.srv}
	e.contract = &contractEnvIT{srv: e.srv, mail: e.mail, acc: e.acc}
	return e
}

func chargeTerms(domain.Offer) (domain.CoverageTerms, error) {
	return domain.CoverageTerms{InsuredAmount: fixtureInsuredAmount, TermMonths: fixtureTermMonths, Coverages: []domain.Coverage{
		{Code: domain.CoverageDeath, InsuredAmount: fixtureInsuredAmount},
		{Code: domain.CoverageDisability, InsuredAmount: fixtureInsuredAmount},
	}}, nil
}

// accept accepts a demo offer and returns the acceptance id.
func (e *chargeEnv) accept(t *testing.T, customer, offer string) string {
	t.Helper()
	status, body := e.acc.do(t, http.MethodPost, "/v1/offers/"+offer+"/acceptance", "insured:"+customer, `{"channel":"web"}`)
	require.Equal(t, http.StatusAccepted, status, "the acceptance is registered whatever the gateway does")
	return body["acceptance_id"].(string)
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

	status400, _ := e.acc.do(t, http.MethodPost, "/v1/acceptances/"+id+"/charge/resume", "operations:ops-1", `{"note":""}`)
	assert.Equal(t, http.StatusBadRequest, status400, "the note is required")
	e.gw.set(gatewayUp)
	code202, resumed := e.acc.do(t, http.MethodPost, "/v1/acceptances/"+id+"/charge/resume", "operations:ops-1",
		`{"note":"Pasarela restablecida, se reanuda desde la aceptacion"}`)
	require.Equal(t, http.StatusAccepted, code202)
	assert.Equal(t, "cobro-"+id, resumed["idempotency_key"], "the same key")
	assert.EqualValues(t, 1, resumed["resume_count"])

	e.waitCharge(t, id, "insured:cust-1", status("confirmed"))
	_, charged := e.gw.chargesOf("cobro-" + id)
	assert.Equal(t, 1, charged)
	e.contract.waitFor(t, "pol-"+id, "insured:cust-1", func(m map[string]any) bool { return m["status"] == "completed" })
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
