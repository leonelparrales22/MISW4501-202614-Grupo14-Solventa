// This file wires HU-W1.3-03 end to end: accepting an offer (HU-W1.3-01) makes
// the activation fixture publish the coverage activation, and the contractual
// document is generated (real PDF), stored in a directory, tied to the policy,
// the insured is e-mailed a secure link (through the shared e-mail client,
// against a controllable provider) and the issuance is audited. It checks each
// acceptance criterion over HTTP.
package integration_test

import (
	"context"
	"encoding/json"
	"io"
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
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/filestore"
	httpadapter "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/http"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/links"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/memory"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/pdf"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/issuance"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/profiling"
)

// mailProvider is a controllable e-mail provider that drops repeated
// idempotency keys, like the real one and the stub.
type mailProvider struct {
	mu     sync.Mutex
	down   bool
	byKey  map[string]map[string]string
	hits   int
	server *httptest.Server
}

func newMailProvider(t *testing.T) *mailProvider {
	t.Helper()
	p := &mailProvider{byKey: map[string]map[string]string{}}
	p.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.mu.Lock()
		defer p.mu.Unlock()
		p.hits++
		if p.down {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		var m map[string]string
		_ = json.NewDecoder(r.Body).Decode(&m)
		key := r.Header.Get("Idempotency-Key")
		if _, dup := p.byKey[key]; dup {
			w.WriteHeader(http.StatusOK)
			return
		}
		p.byKey[key] = m
		w.WriteHeader(http.StatusCreated)
	}))
	t.Cleanup(p.server.Close)
	return p
}

func (p *mailProvider) setDown(down bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.down = down
}

func (p *mailProvider) messages() []map[string]string {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]map[string]string, 0, len(p.byKey))
	for _, m := range p.byKey {
		out = append(out, m)
	}
	return out
}

type contractEnvIT struct {
	srv  *httptest.Server
	mail *mailProvider
	acc  *acceptanceEnv
}

func newContractEnvIT(t *testing.T) *contractEnvIT {
	t.Helper()
	clock := wallClock{}
	mail := newMailProvider(t)

	store, err := filestore.New(t.TempDir())
	require.NoError(t, err)
	signer, err := links.NewSigner("0123456789abcdef0123456789abcdef")
	require.NoError(t, err)
	repo, audit := memory.NewContractStore(), memory.NewIssuanceAuditLog()
	notifier := emailadapter.NewNotifier(email.NewClient(email.Config{
		BaseURL: mail.server.URL, Timeout: time.Second,
		Breaker: resilience.Config{ConsecutiveFailures: 100, OpenTimeout: time.Second, HalfOpenSuccesses: 1},
	}))
	contracts := issuance.NewContractService(issuance.ContractDeps{
		Repo: repo, Generator: pdf.New(), Store: store, Signer: signer, Notifier: notifier, Audit: audit, Clock: clock,
	}, issuance.ContractConfig{
		PollInterval: 10 * time.Millisecond, RetryBase: 40 * time.Millisecond, RetryMax: 80 * time.Millisecond,
		PublicBaseURL: "https://api.solventa.example",
	})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go contracts.Run(ctx)

	offers, acceptances := memory.NewOfferStub(clock), memory.NewAcceptanceStore()
	processor := issuance.NewProcessor(offers, acceptances, memory.NewUnderwriterStub(clock, 0), memory.NewReviewQueueStub(),
		newActivationFixture(contracts, clock), clock,
		issuance.ProcessorConfig{PollInterval: 10 * time.Millisecond, RetryBase: 20 * time.Millisecond, RetryMax: 40 * time.Millisecond})
	go processor.Run(ctx)

	reader := issuance.NewContractReader(repo, store, audit, signer, clock)
	e := &contractEnvIT{mail: mail}
	e.srv = httptest.NewServer(httpadapter.NewRouter(profiling.NewResolver(nil, nil, clock),
		httpadapter.WithAcceptances(issuance.NewAcceptanceService(offers, acceptances, clock, processor.Notify), subjectVerifier{}),
		httpadapter.WithContractDocuments(reader, subjectVerifier{})))
	t.Cleanup(e.srv.Close)
	e.acc = &acceptanceEnv{srv: e.srv}
	return e
}

func (e *contractEnvIT) get(t *testing.T, path, token string) (int, []byte, http.Header) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, e.srv.URL+path, nil)
	require.NoError(t, err)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = res.Body.Close() }()
	body, err := io.ReadAll(res.Body)
	require.NoError(t, err)
	return res.StatusCode, body, res.Header
}

// accept accepts a demo offer as the insured and returns the policy id.
func (e *contractEnvIT) accept(t *testing.T, insured, offer string) string {
	t.Helper()
	status, body := e.acc.do(t, http.MethodPost, "/v1/offers/"+offer+"/acceptance", "insured:"+insured, `{"channel":"web"}`)
	require.Equal(t, http.StatusAccepted, status)
	return "pol-" + body["acceptance_id"].(string)
}

func (e *contractEnvIT) status(t *testing.T, policy, token string) map[string]any {
	t.Helper()
	code, body, _ := e.get(t, "/v1/policies/"+policy+"/issuance-status", token)
	if code != http.StatusOK {
		return nil
	}
	var m map[string]any
	require.NoError(t, json.Unmarshal(body, &m))
	return m
}

func (e *contractEnvIT) waitFor(t *testing.T, policy, token string, cond func(map[string]any) bool) map[string]any {
	t.Helper()
	var last map[string]any
	require.Eventually(t, func() bool {
		last = e.status(t, policy, token)
		return last != nil && cond(last)
	}, 5*time.Second, 10*time.Millisecond, "last status: %v", last)
	return last
}

func notification(m map[string]any) map[string]any { return m["notification"].(map[string]any) }

func TestAcceptedOfferEndsInADocumentAndANotificationWithoutManualSteps(t *testing.T) {
	e := newContractEnvIT(t)
	caller := "insured:cust-1"
	policy := e.accept(t, "cust-1", "demo-active-001")

	st := e.waitFor(t, policy, caller, func(m map[string]any) bool { return m["status"] == "completed" })

	// Criterion 1: generated without manual intervention, inside 60 s.
	assert.Equal(t, true, st["within_slo"])
	doc := st["document"].(map[string]any)
	assert.EqualValues(t, 1, doc["version"])
	assert.Equal(t, "application/pdf", doc["content_type"])

	// Criterion 2: stored and tied to the policy, readable by the owner and the team.
	code, pdfBytes, header := e.get(t, "/v1/policies/"+policy+"/contract-document", caller)
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, "application/pdf", header.Get("Content-Type"))
	assert.True(t, strings.HasPrefix(string(pdfBytes), "%PDF-"))
	assert.Contains(t, string(pdfBytes), policy, "the document names the policy")
	assert.Contains(t, string(pdfBytes), "COP 89.500", "premium")
	assert.Contains(t, string(pdfBytes), "Fallecimiento", "coverage")
	assert.Contains(t, string(pdfBytes), "Vigencia hasta", "validity")
	for _, role := range []string{"advisor:adv-1", "operations:ops-1"} {
		code, _, _ = e.get(t, "/v1/policies/"+policy+"/contract-document", role)
		assert.Equal(t, http.StatusOK, code, role)
	}
	code, _, _ = e.get(t, "/v1/policies/"+policy+"/contract-document", "insured:someone-else")
	assert.Equal(t, http.StatusForbidden, code, "another insured cannot read it")

	// Criterion 3: the insured is notified with a secure link that opens the document.
	msgs := e.mail.messages()
	require.Len(t, msgs, 1)
	assert.Contains(t, msgs[0]["to"], "@example.com")
	link := msgs[0]["body"][strings.Index(msgs[0]["body"], "https://api.solventa.example"):]
	link = strings.Fields(link)[0]
	require.Contains(t, link, "/v1/documents/")
	code, viaLink, _ := e.get(t, strings.TrimPrefix(link, "https://api.solventa.example"), "")
	require.Equal(t, http.StatusOK, code, "the link needs no session")
	assert.Equal(t, pdfBytes, viaLink)
	code, _, _ = e.get(t, strings.TrimPrefix(link, "https://api.solventa.example")+"x", "")
	assert.Equal(t, http.StatusForbidden, code, "a tampered link is refused")

	// Criterion 4: the audit trail rebuilds the issuance.
	audit := st["audit"].([]any)
	require.Len(t, audit, 2)
	generated, sent := audit[0].(map[string]any), audit[1].(map[string]any)
	assert.Equal(t, "document_generated", generated["event"])
	assert.Equal(t, "notification_sent", sent["event"])
	assert.EqualValues(t, 1, sent["document_version"])
	assert.Equal(t, "sent", sent["delivery_result"])
	assert.NotEmpty(t, sent["at"])
	assert.Equal(t, "sent", notification(st)["status"])
}

func TestAnUnavailableMailProviderDelaysOnlyTheNotification(t *testing.T) {
	e := newContractEnvIT(t)
	e.mail.setDown(true)
	caller := "insured:cust-2"
	policy := e.accept(t, "cust-2", "demo-active-002")

	st := e.waitFor(t, policy, caller, func(m map[string]any) bool {
		return m["document"] != nil && notification(m)["attempts"].(float64) >= 2
	})
	assert.Equal(t, "document_ready", st["status"], "the document is available while the notification retries")
	assert.Equal(t, "pending", notification(st)["status"])
	code, _, _ := e.get(t, "/v1/policies/"+policy+"/contract-document", caller)
	assert.Equal(t, http.StatusOK, code)

	e.mail.setDown(false)
	st = e.waitFor(t, policy, caller, func(m map[string]any) bool { return m["status"] == "completed" })

	assert.Len(t, e.mail.messages(), 1, "the insured gets one message")
	var events []string
	for _, a := range st["audit"].([]any) {
		events = append(events, a.(map[string]any)["event"].(string))
	}
	assert.Equal(t, "document_generated", events[0])
	assert.Contains(t, events, "notification_failed")
	assert.Equal(t, "notification_sent", events[len(events)-1])
}

func TestEveryIssuedPolicyGetsItsOwnDocument(t *testing.T) {
	e := newContractEnvIT(t)
	first := e.accept(t, "cust-3", "demo-active-003")
	second := e.accept(t, "cust-4", "demo-active-004")
	require.NotEqual(t, first, second)

	e.waitFor(t, first, "insured:cust-3", func(m map[string]any) bool { return m["status"] == "completed" })
	e.waitFor(t, second, "insured:cust-4", func(m map[string]any) bool { return m["status"] == "completed" })

	assert.Len(t, e.mail.messages(), 2)
	code, _, _ := e.get(t, "/v1/policies/"+first+"/contract-document", "insured:cust-4")
	assert.Equal(t, http.StatusForbidden, code)
	code, _, _ = e.get(t, "/v1/policies/pol-unknown/issuance-status", "advisor:adv-1")
	assert.Equal(t, http.StatusNotFound, code)
}

func TestARequestRoutedToHumanReviewIssuesNothingYet(t *testing.T) {
	e := newContractEnvIT(t)
	status, body := e.acc.do(t, http.MethodPost, "/v1/offers/demo-review-001/acceptance", "insured:cust-5", `{"channel":"web"}`)
	require.Equal(t, http.StatusAccepted, status)
	policy := "pol-" + body["acceptance_id"].(string)

	require.Eventually(t, func() bool {
		_, b := e.acc.do(t, http.MethodGet, "/v1/acceptances/"+body["acceptance_id"].(string), "insured:cust-5", "")
		return b["status"] == "in_review"
	}, 5*time.Second, 10*time.Millisecond)

	code, _, _ := e.get(t, "/v1/policies/"+policy+"/issuance-status", "insured:cust-5")
	assert.Equal(t, http.StatusNotFound, code, "no coverage was activated, so no document")
	assert.Empty(t, e.mail.messages())
}
