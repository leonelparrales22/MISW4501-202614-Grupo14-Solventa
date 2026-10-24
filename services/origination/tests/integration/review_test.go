// This file wires the advisor inbox of HU-W1.1-02 end to end over HTTP
// (router with observability, pkg/security middleware, the acceptance of
// HU-W1.3-01 with its asynchronous processor, the review use case, the
// notification worker over the real e-mail client and the in-memory adapters)
// and checks each acceptance criterion. The e-mail provider is an
// httptest.Server that behaves like stubs/email (POST /messages, idempotent by
// key).
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
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/resilience"
	emailadapter "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/email"
	httpadapter "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/http"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/memory"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/issuance"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/profiling"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/underwriting"
)

// mailbox imitates the e-mail provider of stubs/email: it accepts a message
// once per idempotency key and can fail the first sends.
type mailbox struct {
	mu        sync.Mutex
	failFirst int
	messages  []mailMessage
	seen      map[string]bool
}

type mailMessage struct {
	Key     string
	To      string `json:"to"`
	Subject string `json:"subject"`
	Body    string `json:"body"`
}

func (m *mailbox) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if r.Method != http.MethodPost || r.URL.Path != "/messages" {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	if m.failFirst > 0 {
		m.failFirst--
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	var msg mailMessage
	if err := json.NewDecoder(r.Body).Decode(&msg); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	msg.Key = r.Header.Get("Idempotency-Key")
	if m.seen[msg.Key] {
		w.WriteHeader(http.StatusOK)
		return
	}
	m.seen[msg.Key] = true
	m.messages = append(m.messages, msg)
	w.WriteHeader(http.StatusCreated)
}

func (m *mailbox) received() []mailMessage {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]mailMessage(nil), m.messages...)
}

type reviewEnv struct {
	*acceptanceEnv
	mail *mailbox
}

const advisorCaller = "advisor:adv-1"

func newReviewEnv(t *testing.T, failFirst int) *reviewEnv {
	t.Helper()
	clock := wallClock{}
	offers := memory.NewOfferStub(clock)
	acceptances := memory.NewAcceptanceStore()
	reviews := memory.NewReviewQueueStub()
	issued := memory.NewIssuanceStub()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	underwriter := underwriting.NewHumanAdjustedUnderwriter(reviews,
		memory.NewUnderwriterStub(clock, 0, memory.DefaultReviewScoreThreshold), clock)
	processor := issuance.NewProcessor(offers, acceptances, underwriter, reviews, issued, clock,
		issuance.ProcessorConfig{PollInterval: 20 * time.Millisecond, RetryBase: 30 * time.Millisecond, RetryMax: 60 * time.Millisecond})
	go processor.Run(ctx)

	mail := &mailbox{failFirst: failFirst, seen: map[string]bool{}}
	mailSrv := httptest.NewServer(mail)
	t.Cleanup(mailSrv.Close)
	client := email.NewClient(email.Config{BaseURL: mailSrv.URL, Breaker: resilience.DefaultConfig()})
	worker := underwriting.NewReviewNotificationWorker(reviews, emailadapter.NewReviewNotifier(client), clock,
		underwriting.NotificationConfig{PollInterval: 20 * time.Millisecond, RetryBase: 20 * time.Millisecond, RetryMax: 40 * time.Millisecond})
	go worker.Run(ctx)

	svc := underwriting.NewReviewService(underwriting.ReviewDeps{
		Reviews: reviews, Acceptances: acceptances, OfferWriter: offers, Clock: clock,
		AcceptanceResumed: processor.Notify, NotificationScheduled: worker.Notify,
	})
	srv := httptest.NewServer(httpadapter.NewRouter(profiling.NewResolver(nil, nil, clock),
		httpadapter.WithObservability("origination"),
		httpadapter.WithAcceptances(issuance.NewAcceptanceService(offers, acceptances, clock, processor.Notify), subjectVerifier{}),
		httpadapter.WithReviews(svc, subjectVerifier{})))
	t.Cleanup(srv.Close)
	return &reviewEnv{acceptanceEnv: &acceptanceEnv{srv: srv, reviews: reviews, issued: issued}, mail: mail}
}

// routed accepts the offer as the insured and waits until it is in review.
func (e *reviewEnv) routed(t *testing.T, offerID string) string {
	t.Helper()
	status, body := e.accept(t, offerID, customer)
	require.Equal(t, http.StatusAccepted, status)
	id := body["acceptance_id"].(string)
	e.waitStatus(t, id, customer, "in_review")
	return id
}

func (e *reviewEnv) decide(t *testing.T, id, body string) (int, map[string]any) {
	t.Helper()
	return e.do(t, http.MethodPost, "/v1/review-requests/"+id+"/decision", advisorCaller, body)
}

func (e *reviewEnv) list(t *testing.T, query string) map[string]any {
	t.Helper()
	status, body := e.do(t, http.MethodGet, "/v1/review-requests"+query, advisorCaller, "")
	require.Equal(t, http.StatusOK, status)
	return body
}

func items(body map[string]any) []map[string]any {
	out := []map[string]any{}
	for _, it := range body["items"].([]any) {
		out = append(out, it.(map[string]any))
	}
	return out
}

func TestHUW1102CA1RoutedRequestAppearsPendingInTheInbox(t *testing.T) {
	e := newReviewEnv(t, 0)
	id := e.routed(t, "demo-review-ca1")

	got := items(e.list(t, ""))

	require.Len(t, got, 1)
	it := got[0]
	assert.Equal(t, id, it["acceptance_id"])
	assert.Equal(t, "demo-review-ca1", it["offer_id"])
	assert.Equal(t, "cust-1", it["customer_id"])
	assert.Equal(t, map[string]any{"full_name": "cust-1@example.com", "document_id": "demo-document", "email": "cust-1@example.com"}, it["applicant"])
	assert.Equal(t, map[string]any{"premium": 89500.0, "currency": "COP"}, it["offer"])
	assert.Equal(t, "provisional_offer", it["reason"])
	assert.NotEmpty(t, it["received_at"])
	assert.Equal(t, "pending", it["status"])
	assert.Nil(t, it["decision"])

	status, detail := e.do(t, http.MethodGet, "/v1/review-requests/"+id, advisorCaller, "")
	require.Equal(t, http.StatusOK, status)
	assert.Equal(t, "provisional", detail["offer"].(map[string]any)["mode"])
	assert.NotNil(t, detail["offer"].(map[string]any)["expires_at"])
}

func TestHUW1102CA2ObservationsAreRequiredToAdjustOrReject(t *testing.T) {
	e := newReviewEnv(t, 0)
	id := e.routed(t, "demo-review-ca2")

	status, body := e.decide(t, id, `{"type":"reject","observations":"   "}`)
	assert.Equal(t, http.StatusBadRequest, status)
	assert.Equal(t, "observations_required", body["error"])
	status, body = e.decide(t, id, `{"type":"adjust","premium_adjustment_pct":10}`)
	assert.Equal(t, http.StatusBadRequest, status)
	assert.Equal(t, "observations_required", body["error"])
	status, body = e.decide(t, id, `{"type":"adjust","observations":"Recargo"}`)
	assert.Equal(t, http.StatusBadRequest, status)
	assert.Equal(t, "premium_adjustment_invalid", body["error"])

	assert.Len(t, items(e.list(t, "")), 1, "the case is still pending")
	e.waitStatus(t, id, customer, "in_review")
}

func TestHUW1102CA3AcceptContinuesToIssuance(t *testing.T) {
	e := newReviewEnv(t, 0)
	id := e.routed(t, "demo-review-ca3a")

	status, body := e.decide(t, id, `{"type":"accept"}`)
	require.Equal(t, http.StatusCreated, status)
	assert.Equal(t, "decided", body["status"])

	got := e.waitStatus(t, id, customer, "issuance_pending")
	assert.Equal(t, "approved", got["underwriting"].(map[string]any)["outcome"])
	assert.Equal(t, "accept", got["review"].(map[string]any)["outcome"])
	assert.True(t, e.issued.Started(id))
	assert.Empty(t, items(e.list(t, "")), "the case left the pending list")
	assert.Empty(t, e.mail.received(), "the issuance e-mail of HU-W1.3-03 tells the customer")
}

func TestHUW1102CA3AdjustedOfferIsAcceptedThroughTheSameFlow(t *testing.T) {
	e := newReviewEnv(t, 0)
	id := e.routed(t, "demo-review-ca3b")

	status, body := e.decide(t, id, `{"type":"adjust","observations":"Ingreso no verificado","premium_adjustment_pct":10}`)
	require.Equal(t, http.StatusCreated, status)
	offer := body["decision"].(map[string]any)["adjusted_offer"].(map[string]any)
	assert.Equal(t, "adj-"+id, offer["offer_id"])
	assert.InDelta(t, 98450.0, offer["premium"], 1e-9)

	got := e.waitStatus(t, id, customer, "adjusted")
	assert.Equal(t, offer, got["review"].(map[string]any)["adjusted_offer"])

	// The customer accepts the new offer with the same POST of HU-W1.3-01.
	status, accepted := e.accept(t, "adj-"+id, customer)
	require.Equal(t, http.StatusAccepted, status)
	newID := accepted["acceptance_id"].(string)
	final := e.waitStatus(t, newID, customer, "issuance_pending")
	assert.Equal(t, "approved", final["underwriting"].(map[string]any)["outcome"], "it does not go back to the inbox")
	assert.True(t, e.issued.Started(newID))
	assert.Empty(t, items(e.list(t, "")))
}

func TestHUW1102CA3RejectClosesTheCase(t *testing.T) {
	e := newReviewEnv(t, 0)
	id := e.routed(t, "demo-highrisk-ca3c")

	status, _ := e.decide(t, id, `{"type":"reject","observations":"Riesgo fuera de política"}`)
	require.Equal(t, http.StatusCreated, status)

	got := e.waitStatus(t, id, customer, "rejected")
	assert.Equal(t, "reject", got["review"].(map[string]any)["outcome"])
	assert.False(t, e.issued.Started(id))
	decided := items(e.list(t, "?status=decided"))
	require.Len(t, decided, 1)
	assert.Equal(t, "high_risk_score", decided[0]["reason"])
}

// D-3 c: one e-mail per decision, with its own idempotency key.
func TestHUW1102CA3RejectAndAdjustNotifyTheCustomerByEmail(t *testing.T) {
	e := newReviewEnv(t, 0)
	rejected := e.routed(t, "demo-highrisk-mail")
	adjusted := e.routed(t, "demo-review-mail")

	status, _ := e.decide(t, rejected, `{"type":"reject","observations":"Observación interna del asesor"}`)
	require.Equal(t, http.StatusCreated, status)
	status, _ = e.decide(t, adjusted, `{"type":"adjust","observations":"Observación interna del asesor","premium_adjustment_pct":10}`)
	require.Equal(t, http.StatusCreated, status)

	require.Eventually(t, func() bool { return len(e.mail.received()) == 2 }, 3*time.Second, 10*time.Millisecond)
	byKey := map[string]mailMessage{}
	for _, m := range e.mail.received() {
		byKey[m.Key] = m
		assert.Equal(t, "cust-1@example.com", m.To)
		assert.NotContains(t, m.Body, "Observación interna", "the advisor observations stay internal")
	}
	assert.Equal(t, "Tu solicitud de seguro no fue aprobada", byKey["review-outcome:"+rejected].Subject)
	assert.Contains(t, byKey["review-outcome:"+adjusted].Body, "$ 98.450 COP")

	require.Eventually(t, func() bool {
		d := items(e.list(t, "?status=decided"))
		return len(d) == 2 && notificationStatus(d[0]) == "sent" && notificationStatus(d[1]) == "sent"
	}, 3*time.Second, 10*time.Millisecond)
}

func TestHUW1102CA3NoticeArrivesAfterTwoFailedSends(t *testing.T) {
	e := newReviewEnv(t, 2)
	id := e.routed(t, "demo-highrisk-retry")

	status, _ := e.decide(t, id, `{"type":"reject","observations":"Fuera de política"}`)
	require.Equal(t, http.StatusCreated, status)

	var n map[string]any
	require.Eventually(t, func() bool {
		d := items(e.list(t, "?status=decided"))
		if len(d) != 1 {
			return false
		}
		n, _ = d[0]["decision"].(map[string]any)["notification"].(map[string]any)
		return n != nil && n["status"] == "sent"
	}, 3*time.Second, 10*time.Millisecond)
	assert.Equal(t, 2.0, n["attempts"])
	assert.Len(t, e.mail.received(), 1, "a single message")
}

func notificationStatus(it map[string]any) any {
	n, ok := it["decision"].(map[string]any)["notification"].(map[string]any)
	if !ok {
		return nil
	}
	return n["status"]
}

func TestHUW1102CA4DecisionIsRecordedForAudit(t *testing.T) {
	e := newReviewEnv(t, 0)
	id := e.routed(t, "demo-review-ca4")
	before := time.Now().UTC()

	status, _ := e.decide(t, id, `{"type":"adjust","observations":"Ingreso no verificado","premium_adjustment_pct":12.5}`)
	require.Equal(t, http.StatusCreated, status)

	decided := items(e.list(t, "?status=decided"))
	require.Len(t, decided, 1)
	d := decided[0]["decision"].(map[string]any)
	assert.NotEmpty(t, d["decision_id"])
	assert.Equal(t, "adjust", d["type"])
	assert.Equal(t, map[string]any{"id": "adv-1", "email": "adv-1@example.com"}, d["advisor"])
	assert.Equal(t, "Ingreso no verificado", d["observations"])
	assert.Equal(t, 12.5, d["premium_adjustment_pct"])
	decidedAt, err := time.Parse(time.RFC3339Nano, d["decided_at"].(string))
	require.NoError(t, err)
	assert.WithinDuration(t, before, decidedAt, 5*time.Second)

	status, body := e.do(t, http.MethodPost, "/v1/review-requests/"+id+"/decision", "advisor:adv-2", `{"type":"accept"}`)
	assert.Equal(t, http.StatusConflict, status)
	assert.Equal(t, "review_request_already_decided", body["error"])
	_, detail := e.do(t, http.MethodGet, "/v1/review-requests/"+id, advisorCaller, "")
	assert.Equal(t, "adv-1", detail["decision"].(map[string]any)["advisor"].(map[string]any)["id"], "the first decision stays")
}

func TestHUW1102CA5FilterAndOrderByArrival(t *testing.T) {
	e := newReviewEnv(t, 0)
	first := e.routed(t, "demo-review-ca5a")
	second := e.routed(t, "demo-highrisk-ca5b")
	third := e.routed(t, "demo-review-ca5c")

	asc := e.list(t, "?status=pending&order=asc")
	assert.Equal(t, []any{first, second, third}, ids(asc))
	assert.Equal(t, map[string]any{"pending": 3.0, "decided": 0.0}, asc["counts"])
	assert.Equal(t, []any{third, second, first}, ids(e.list(t, "?order=desc")))
	assert.Equal(t, []any{second}, ids(e.list(t, "?reason=high_risk_score")))

	status, _ := e.decide(t, first, `{"type":"accept"}`)
	require.Equal(t, http.StatusCreated, status)
	assert.Equal(t, []any{second, third}, ids(e.list(t, "")))
	decided := e.list(t, "?status=decided")
	assert.Equal(t, []any{first}, ids(decided))
	assert.Equal(t, map[string]any{"pending": 2.0, "decided": 1.0}, decided["counts"])

	status, body := e.do(t, http.MethodGet, "/v1/review-requests?status=open", advisorCaller, "")
	assert.Equal(t, http.StatusBadRequest, status)
	assert.Equal(t, "invalid_request", body["error"])
}

func ids(body map[string]any) []any {
	out := []any{}
	for _, it := range items(body) {
		out = append(out, it["acceptance_id"])
	}
	return out
}

func TestHUW1102OnlyAnAdvisorUsesTheInbox(t *testing.T) {
	e := newReviewEnv(t, 0)
	id := e.routed(t, "demo-review-access")

	for _, token := range []string{customer, "operations:ops-1", "partner:par-1"} {
		status, _ := e.do(t, http.MethodGet, "/v1/review-requests", token, "")
		assert.Equal(t, http.StatusForbidden, status, token)
		status, _ = e.do(t, http.MethodPost, "/v1/review-requests/"+id+"/decision", token, `{"type":"accept"}`)
		assert.Equal(t, http.StatusForbidden, status, token)
	}
	req, err := http.NewRequest(http.MethodGet, e.srv.URL+"/v1/review-requests", strings.NewReader(""))
	require.NoError(t, err)
	res, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	_ = res.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, res.StatusCode)
	e.waitStatus(t, id, customer, "in_review")
}
