package integration_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/adapters/email"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/adapters/payments"
	corepolicies "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/adapters/policies"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/resilience"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/security"
	httpadapter "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/adapters/http"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/adapters/mail"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/adapters/payouts"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/adapters/pdf"
	policiesadapter "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/adapters/policies"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/adapters/postgres"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/decision"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/evaluation"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/filing"
)

// Every acceptance criterion of HU-W2.2-03 (evaluation, approval and payout
// of the claim) over HTTP: the real router, use cases, worker and clients of
// the policy core, the payment gateway and the e-mail provider, PostgreSQL in
// a container and the claims filed through the real web filing. The three
// providers are servers of the test that answer like their stubs (stubs/):
// the policy core with the terms and the holder, the gateway paying a key
// once and rejecting an account that ends in 89, and the e-mail provider.

func init() {
	verifier["analista"] = security.Claims{Subject: "sub-analista", Email: "analista@solventa.example",
		Roles: []security.Role{security.RoleOperations}}
}

// termsCore is the policy core of the decision: the home policy of each
// holder with its terms, and their profiles with the account to pay.
type termsCore struct {
	srv *httptest.Server
}

func newTermsCore(t *testing.T) *termsCore {
	t.Helper()
	policy := map[string]any{"policy_id": "HG-2025-880231", "line": "home", "product": "Hogar",
		"insured_object": "Carrera 15 # 93-40", "status": "active", "valid_from": validFrom, "valid_to": validTo,
		"covered_incidents": []string{"water_damage"},
		"coverages": []map[string]any{{"incident": "water_damage", "insured_value_minor": cents(320_000_000),
			"currency": "COP", "deductible_basis_points": 500}}}
	holder := func(id, name, mailbox, account string) map[string]any {
		return map[string]any{"holder_id": id, "full_name": name, "document_type": "CC", "document_id": "1020345678",
			"email": mailbox, "phone": "+57 301 234 5678", "payout_account": map[string]string{"bank_code": "1007",
				"bank_name": "Banco Aliado", "account_type": "savings", "account_number": account}}
	}
	holders := map[string]map[string]any{
		"sub-carolina": holder("sub-carolina", "Carolina Pérez Ruiz", "carolina@correo.example", "0123-4567-12"),
		// The account of Andrés ends in 89: the gateway rejects it (O6b).
		"sub-andres": holder("sub-andres", "Andrés Gómez", "andres@correo.example", "0123-4567-89"),
	}
	tc := &termsCore{}
	tc.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		if len(parts) < 2 || parts[0] != "holders" || holders[parts[1]] == nil {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		switch len(parts) {
		case 2:
			_ = json.NewEncoder(w).Encode(holders[parts[1]])
		case 3:
			_ = json.NewEncoder(w).Encode(map[string]any{"policies": []map[string]any{policy}})
		default:
			_ = json.NewEncoder(w).Encode(policy)
		}
	}))
	t.Cleanup(tc.srv.Close)
	return tc
}

// gateway answers /payouts like the payment gateway stub: a key pays once,
// an account that ends in 89 is rejected without binding the key, and it can
// be made to fail.
type gateway struct {
	srv  *httptest.Server
	down atomic.Bool

	mu    sync.Mutex
	paid  map[string]map[string]any
	calls int
}

func newGateway(t *testing.T) *gateway {
	t.Helper()
	g := &gateway{paid: map[string]map[string]any{}}
	g.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		defer g.mu.Unlock()
		g.calls++
		if g.down.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		key := r.Header.Get("Idempotency-Key")
		var in map[string]any
		_ = json.NewDecoder(r.Body).Decode(&in)
		if done, ok := g.paid[key]; ok {
			_ = json.NewEncoder(w).Encode(done)
			return
		}
		if strings.HasSuffix(in["destination"].(map[string]any)["account_number"].(string), "89") {
			w.WriteHeader(http.StatusUnprocessableEntity)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid_destination_account"})
			return
		}
		g.paid[key] = map[string]any{"payout_id": "po-" + key, "status": "paid", "idempotency_key": key,
			"amount_minor": in["amount_minor"], "currency": in["currency"]}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(g.paid[key])
	}))
	t.Cleanup(g.srv.Close)
	return g
}

func (g *gateway) payouts() map[string]map[string]any {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := map[string]map[string]any{}
	for k, v := range g.paid {
		out[k] = v
	}
	return out
}

// mailbox answers /messages like the e-mail stub.
type mailbox struct {
	srv *httptest.Server

	mu       sync.Mutex
	messages map[string]email.Message
}

func newMailbox(t *testing.T) *mailbox {
	t.Helper()
	m := &mailbox{messages: map[string]email.Message{}}
	m.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in email.Message
		_ = json.NewDecoder(r.Body).Decode(&in)
		m.mu.Lock()
		defer m.mu.Unlock()
		m.messages[r.Header.Get("Idempotency-Key")] = in
		w.WriteHeader(http.StatusCreated)
	}))
	t.Cleanup(m.srv.Close)
	return m
}

func (m *mailbox) message(key string) (email.Message, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	msg, ok := m.messages[key]
	return msg, ok
}

// startWithDecisions is the claims service as cmd/claims composes it with
// DATABASE_URL, AUTH_*, POLICY_CORE_URL, PAYMENT_GATEWAY_URL and EMAIL_URL,
// with the worker that pays and notifies running.
func startWithDecisions(t *testing.T, url string, core *termsCore, g *gateway, m *mailbox) service {
	t.Helper()
	ctx := context.Background()
	db, err := postgres.Open(ctx, url)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, postgres.Migrate(ctx, db))

	breaker := resilience.Config{ConsecutiveFailures: 100, OpenTimeout: time.Second, HalfOpenSuccesses: 1}
	coreClient := corepolicies.NewClient(corepolicies.Config{BaseURL: core.srv.URL, Timeout: 500 * time.Millisecond,
		Breaker: breaker})
	records, supports, objects := postgres.NewClaimRecordStore(db), postgres.NewSupportStore(db), newBucket()
	terms := policiesadapter.NewTerms(coreClient)
	decisions := postgres.DecisionStoreOf(records)
	worker, err := decision.NewWorker(decisions,
		payouts.NewGateway(payments.NewClient(payments.Config{BaseURL: g.srv.URL, Breaker: breaker})),
		mail.NewMailer(email.NewClient(email.Config{BaseURL: m.srv.URL, Breaker: breaker})), clock{},
		decision.WorkerConfig{PollInterval: 50 * time.Millisecond,
			Retry: resilience.RetryPolicy{Base: 50 * time.Millisecond, Max: 100 * time.Millisecond}})
	require.NoError(t, err)
	wctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		worker.Run(wctx)
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})

	priorities := startSweep(t, db)
	signer := filing.NewQueryService(records, pdf.NewReceiptRenderer()).WithObjects(objects)
	return service{db: db, bucket: objects, Handler: httpadapter.NewRouter(
		httpadapter.WithClaimReads(filing.NewQueryService(records, pdf.NewReceiptRenderer()).WithObjects(objects), verifier),
		httpadapter.WithWebFilings(filing.NewWebService(records, supports, policiesadapter.NewDirectory(coreClient), clock{}),
			filing.NewSupportService(supports, objects, clock{}), verifier),
		httpadapter.WithEvaluation(evaluation.NewService(records, records, priorities, signer, terms, objects).
			WithDecisions(decisions), verifier),
		httpadapter.WithDecisions(decision.NewService(records, decisions, terms, clock{}, worker.Notify), verifier),
	)}
}

func postJSON(t *testing.T, svc service, path, token string, body any) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	raw, err := json.Marshal(body)
	require.NoError(t, err)
	return call(t, svc, http.MethodPost, path, token, bytes.NewReader(raw), map[string]string{"Content-Type": "application/json"})
}

// statusFor reads the status of the claim as the insured sees it.
func statusFor(t *testing.T, svc service, token, id string) string {
	t.Helper()
	rec, body := call(t, svc, http.MethodGet, "/v1/claims/"+id, token, nil, nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	return body["status"].(string)
}

func waitForStatus(t *testing.T, svc service, token, id, want string) {
	t.Helper()
	require.Eventually(t, func() bool { return statusFor(t, svc, token, id) == want }, 10*time.Second,
		50*time.Millisecond, "the claim never became %s", want)
}

func TestDecisionAcceptanceCriteria(t *testing.T) {
	core, g, m := newTermsCore(t), newGateway(t), newMailbox(t)
	svc := startWithDecisions(t, newDatabase(t), core, g, m)
	paid := fileClaim(t, svc, "form-paga", waterDamageBody(t, svc, "carolina", cents(4_800_000)))
	rejected := fileClaim(t, svc, "form-rechaza", waterDamageBody(t, svc, "carolina", nil))
	rec, filed := file(t, svc, "andres", "form-excepcion", waterDamageBody(t, svc, "andres", cents(2_000_000)))
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	exception := filed["claim_id"].(string)

	t.Run("1 the analyst opens the claim from the list with all it is decided with", func(t *testing.T) {
		rec, queue := call(t, svc, http.MethodGet, "/v1/claims/evaluations", "analista", nil, nil)
		require.Equal(t, http.StatusOK, rec.Code)
		var ids []string
		for _, it := range queue["items"].([]any) {
			ids = append(ids, it.(map[string]any)["claim_id"].(string))
		}
		assert.ElementsMatch(t, []string{paid, rejected, exception}, ids)

		rec, detail := call(t, svc, http.MethodGet, "/v1/claims/"+paid+"/evaluation", "analista", nil, nil)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		assert.EqualValues(t, cents(4_800_000), detail["claim"].(map[string]any)["estimated_amount_minor"])
		assert.EqualValues(t, cents(320_000_000), detail["coverage"].(map[string]any)["insured_value_minor"])
		assert.EqualValues(t, 500, detail["coverage"].(map[string]any)["deductible_basis_points"])
		account := detail["holder"].(map[string]any)["payout_account"].(map[string]any)
		assert.Equal(t, "•••• 6712", account["masked_number"])
		assert.Len(t, detail["support_files"], 3)
		assert.NotNil(t, detail["triage"])
		assert.Nil(t, detail["decision"])
	})

	t.Run("6 only operations approves or rejects", func(t *testing.T) {
		for _, token := range []string{"carolina", "advisor"} {
			rec, _ := postJSON(t, svc, "/v1/claims/"+paid+"/approval", token, map[string]any{"approved_amount_minor": 1})
			assert.Equal(t, http.StatusForbidden, rec.Code, token)
			rec, _ = postJSON(t, svc, "/v1/claims/"+paid+"/rejection", token, map[string]any{"reason": "x"})
			assert.Equal(t, http.StatusForbidden, rec.Code, token)
		}
		assert.Equal(t, "active", statusFor(t, svc, "carolina", paid))
	})

	t.Run("2 the approval is bounded by the insured value, discounts the deductible and is paid", func(t *testing.T) {
		rec, body := postJSON(t, svc, "/v1/claims/"+paid+"/approval", "analista",
			map[string]any{"approved_amount_minor": cents(320_000_000) + 1})
		assert.Equal(t, http.StatusUnprocessableEntity, rec.Code)
		assert.Equal(t, "amount_exceeds_insured_value", body["error"])

		rec, body = postJSON(t, svc, "/v1/claims/"+paid+"/approval", "analista",
			map[string]any{"approved_amount_minor": cents(4_350_000)})
		require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
		approval := body["approval"].(map[string]any)
		assert.EqualValues(t, cents(217_500), approval["deductible_minor"])
		assert.EqualValues(t, cents(4_132_500), approval["payout_amount_minor"])

		waitForStatus(t, svc, "carolina", paid, "paid")
		payout := g.payouts()["indemnizacion-"+paid]
		require.NotNil(t, payout)
		assert.EqualValues(t, cents(4_132_500), payout["amount_minor"])

		rec, _ = postJSON(t, svc, "/v1/claims/"+paid+"/approval", "analista", map[string]any{"approved_amount_minor": 1})
		assert.Equal(t, http.StatusConflict, rec.Code, "a decided claim is not decided again")
	})

	t.Run("3 the rejection needs its reason and is notified to the insured", func(t *testing.T) {
		rec, body := postJSON(t, svc, "/v1/claims/"+rejected+"/rejection", "analista", map[string]any{"reason": "   "})
		assert.Equal(t, http.StatusUnprocessableEntity, rec.Code)
		assert.Equal(t, "reason_required", body["error"])

		reason := "La póliza excluye los daños por falta de mantenimiento."
		rec, _ = postJSON(t, svc, "/v1/claims/"+rejected+"/rejection", "analista", map[string]any{"reason": reason})
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		assert.Equal(t, "rejected", statusFor(t, svc, "carolina", rejected))

		var msg email.Message
		require.Eventually(t, func() bool {
			var ok bool
			msg, ok = m.message("rechazo-" + rejected)
			return ok
		}, 10*time.Second, 50*time.Millisecond)
		assert.Equal(t, "carolina@correo.example", msg.To)
		assert.Contains(t, msg.Body, reason)
	})

	t.Run("4 a failed payout keeps the approval as a payment exception that is retried once paid", func(t *testing.T) {
		rec, _ := postJSON(t, svc, "/v1/claims/"+exception+"/approval", "analista",
			map[string]any{"approved_amount_minor": cents(1_000_000)})
		require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
		waitForStatus(t, svc, "andres", exception, "payment_exception")

		rec, list := call(t, svc, http.MethodGet, "/v1/claims/payment-exceptions", "analista", nil, nil)
		require.Equal(t, http.StatusOK, rec.Code)
		items := list["items"].([]any)
		require.Len(t, items, 1)
		assert.Equal(t, exception, items[0].(map[string]any)["claim_id"])
		_, detail := call(t, svc, http.MethodGet, "/v1/claims/"+exception+"/evaluation", "analista", nil, nil)
		payout := detail["decision"].(map[string]any)["payout"].(map[string]any)
		assert.Equal(t, "rejected", payout["failure"])
		assert.Equal(t, "invalid_destination_account", payout["gateway_code"])
		assert.Equal(t, "indemnizacion-"+exception, payout["payment_reference"])

		// The gateway is down when the analyst retries: the automatic attempts
		// run out and the claim is an exception again, with the same key.
		g.down.Store(true)
		rec, _ = postJSON(t, svc, "/v1/claims/"+exception+"/payout-retry", "analista", map[string]any{
			"destination": map[string]string{"bank_code": "1007", "bank_name": "Banco Aliado", "account_type": "savings",
				"account_number": "0123-4567-21"}})
		require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
		require.Eventually(t, func() bool {
			_, d := call(t, svc, http.MethodGet, "/v1/claims/"+exception+"/evaluation", "analista", nil, nil)
			p := d["decision"].(map[string]any)["payout"].(map[string]any)
			return p["failure"] == "retries_exhausted"
		}, 10*time.Second, 50*time.Millisecond)
		assert.Equal(t, "payment_exception", statusFor(t, svc, "andres", exception))

		g.down.Store(false)
		rec, _ = postJSON(t, svc, "/v1/claims/"+exception+"/payout-retry", "analista", map[string]any{})
		require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
		waitForStatus(t, svc, "andres", exception, "paid")
		assert.Len(t, g.payouts(), 2, "one disbursement per approval: Carolina's and Andrés's")
	})

	t.Run("5 every decision is recorded with its analyst, its time and its amount or reason", func(t *testing.T) {
		_, approved := call(t, svc, http.MethodGet, "/v1/claims/"+paid+"/evaluation", "analista", nil, nil)
		d := approved["decision"].(map[string]any)
		assert.Equal(t, "analista@solventa.example", d["decided_by"])
		assert.NotEmpty(t, d["decided_at"])
		assert.EqualValues(t, cents(4_350_000), d["approval"].(map[string]any)["approved_amount_minor"])
		assert.Equal(t, "paid", d["payout"].(map[string]any)["status"])

		_, refused := call(t, svc, http.MethodGet, "/v1/claims/"+rejected+"/evaluation", "analista", nil, nil)
		r := refused["decision"].(map[string]any)
		assert.Equal(t, "analista@solventa.example", r["decided_by"])
		assert.Equal(t, "La póliza excluye los daños por falta de mantenimiento.", r["rejection"].(map[string]any)["reason"])
		assert.NotNil(t, r["rejection"].(map[string]any)["notified_at"])

		rec, mine := call(t, svc, http.MethodGet, "/v1/claims?status=paid", "carolina", nil, nil)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		require.Len(t, mine["items"], 1, "the insured filters their claims by the new status")
	})
}
