// This file wires the purchase formalization of HU-W1.1-03 end to end over HTTP
// (router, pkg/security middleware, the advisor quote, the formalization, the
// acceptance and its processor over the in-memory stores) and checks each
// acceptance criterion.
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

	httpadapter "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/http"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/memory"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/formalization"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/issuance"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/orchestrator"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/rating"
)

// codeInbox is the e-mail of the customer: it keeps the codes sent to them.
type codeInbox struct {
	mu    sync.Mutex
	codes []domain.ConsentCodeNotice
}

func (c *codeInbox) SendConsentCode(_ context.Context, n domain.ConsentCodeNotice) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.codes = append(c.codes, n)
	return nil
}

func (c *codeInbox) last(t *testing.T) domain.ConsentCodeNotice {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	require.NotEmpty(t, c.codes)
	return c.codes[len(c.codes)-1]
}

type formalizationEnv struct {
	*quoteEnv
	inbox  *codeInbox
	issued *memory.IssuanceStub
}

func newFormalizationEnv(t *testing.T) *formalizationEnv {
	t.Helper()
	pe, _ := newActuarialEnv(t, true)
	clock := &steppingClock{t: time.Now().UTC()}
	customers, quotes, offers := memory.NewCustomerStore(), memory.NewQuoteStore(), memory.NewPersonalizedOfferStore()
	acceptances := memory.NewAcceptanceStore()
	issued := memory.NewIssuanceStub()
	router := memory.NewOfferRouter(memory.NewOfferStub(clock), offers)

	processor := issuance.NewProcessor(router, acceptances, memory.NewUnderwriterStub(clock, 0, memory.DefaultReviewScoreThreshold),
		memory.NewReviewQueueStub(), issued, clock,
		issuance.ProcessorConfig{PollInterval: 20 * time.Millisecond, RetryBase: 30 * time.Millisecond, RetryMax: 60 * time.Millisecond})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go processor.Run(ctx)

	accepting := issuance.NewAcceptanceService(router, acceptances, clock, processor.Notify)
	inbox := &codeInbox{}
	quoting := orchestrator.NewAssistedQuoteService(customers, quotes, pe.resolver, rating.New(), clock)
	formalizing := formalization.New(formalization.Deps{
		Quotes: quotes, Customers: customers, Offers: offers, Formalized: memory.NewFormalizationStore(),
		Challenges: memory.NewConsentChallengeStore(), Sender: inbox, Acceptances: accepting, Clock: clock,
	})

	e := &formalizationEnv{quoteEnv: &quoteEnv{parallelEnv: pe, clock: clock}, inbox: inbox, issued: issued}
	e.srv = httptest.NewServer(httpadapter.NewRouter(pe.resolver, httpadapter.WithObservability("origination"),
		httpadapter.WithAssistedQuotes(quoting, subjectVerifier{}),
		httpadapter.WithFormalizations(formalizing, subjectVerifier{}),
		httpadapter.WithAcceptances(accepting, subjectVerifier{})))
	t.Cleanup(e.srv.Close)
	return e
}

func (e *formalizationEnv) quote(t *testing.T) string {
	t.Helper()
	status, q := e.do(t, http.MethodPost, "/v1/assisted-quotes", advisor, quoteBody("1032456789"))
	require.Equal(t, http.StatusCreated, status)
	return q["quote_id"].(string)
}

func (e *formalizationEnv) consent(t *testing.T, quoteID string) string {
	t.Helper()
	status, res := e.do(t, http.MethodPost, "/v1/assisted-quotes/"+quoteID+"/consent", advisor, "")
	require.Equal(t, http.StatusOK, status, "%v", res)
	assert.Equal(t, "an***@example.com", res["sent_to"])
	return e.inbox.last(t).Code
}

// codeJSON is the body of a formalization confirmed with the code the customer received.
func codeJSON(code string) string {
	b, _ := json.Marshal(map[string]any{
		"acceptance":        map[string]string{"method": "confirmation_code", "accepted_at": time.Now().UTC().Format(time.RFC3339)},
		"confirmation_code": code,
	})
	return string(b)
}

// callJSON is the body of a formalization backed by a recorded call.
func callJSON(reference string) string {
	b, _ := json.Marshal(map[string]any{"acceptance": map[string]string{
		"method": "recorded_call", "accepted_at": time.Now().UTC().Format(time.RFC3339), "reference": reference,
	}})
	return string(b)
}

func TestCA1ToCA5TheAdvisorFormalizesWithTheCustomersCodeAndTheIssuanceStarts(t *testing.T) {
	e := newFormalizationEnv(t)
	quoteID := e.quote(t)
	code := e.consent(t, quoteID)

	status, res := e.do(t, http.MethodPost, "/v1/assisted-quotes/"+quoteID+"/formalization", advisor, codeJSON(code))

	require.Equal(t, http.StatusCreated, status, "CA-1: %v", res)
	assert.Equal(t, quoteID, res["quote_id"], "CA-4: the quote it comes from")
	assert.Equal(t, "adv-1", res["advisor_id"], "CA-4: the advisor")
	assert.NotEmpty(t, res["formalized_at"], "CA-4: the date")
	assert.Equal(t, "confirmation_code", res["consent_method"], "CA-5")

	acceptanceID := res["acceptance_id"].(string)
	assert.Eventually(t, func() bool { return e.issued.Started(acceptanceID) }, 3*time.Second, 10*time.Millisecond,
		"CA-3: the issuance starts with no manual step")
	status, acc := e.do(t, http.MethodGet, "/v1/acceptances/"+acceptanceID, advisor, "")
	require.Equal(t, http.StatusOK, status)
	assert.Equal(t, "advisor", acc["channel"])

	status, read := e.do(t, http.MethodGet, "/v1/assisted-quotes/"+quoteID+"/formalization", advisor, "")
	require.Equal(t, http.StatusOK, status)
	assert.Equal(t, res["formalization_id"], read["formalization_id"])
}

func TestCA5ARecordedCallBacksTheConsentWithoutACode(t *testing.T) {
	e := newFormalizationEnv(t)
	quoteID := e.quote(t)

	status, res := e.do(t, http.MethodPost, "/v1/assisted-quotes/"+quoteID+"/formalization", advisor, callJSON("GRAB-2026-88910"))

	require.Equal(t, http.StatusCreated, status, "%v", res)
	assert.Equal(t, "recorded_call", res["consent_method"])
	assert.Equal(t, "GRAB-2026-88910", res["consent_reference"])
	assert.NotEmpty(t, res["accepted_at"])
	assert.Equal(t, "adv-1@example.com", res["advisor_email"])
	assert.Empty(t, e.inbox.codes, "no code was sent")
	acceptanceID := res["acceptance_id"].(string)
	assert.Eventually(t, func() bool { return e.issued.Started(acceptanceID) }, 3*time.Second, 10*time.Millisecond)
}

func TestCA5ACallWithoutItsReferenceIsRefused(t *testing.T) {
	e := newFormalizationEnv(t)
	quoteID := e.quote(t)
	body := `{"acceptance":{"method":"recorded_call","accepted_at":"` + time.Now().UTC().Format(time.RFC3339) + `"}}`

	status, res := e.do(t, http.MethodPost, "/v1/assisted-quotes/"+quoteID+"/formalization", advisor, body)

	require.Equal(t, http.StatusUnprocessableEntity, status)
	assert.Equal(t, "validation_failed", res["error"])
	status, _ = e.do(t, http.MethodGet, "/v1/assisted-quotes/"+quoteID+"/formalization", advisor, "")
	assert.Equal(t, http.StatusNotFound, status)
}

func TestCA2AnExpiredQuoteIsNotFormalizedAndOffersANewOne(t *testing.T) {
	e := newFormalizationEnv(t)
	quoteID := e.quote(t)
	code := e.consent(t, quoteID)
	e.clock.advance(25 * time.Hour)

	status, res := e.do(t, http.MethodPost, "/v1/assisted-quotes/"+quoteID+"/formalization", advisor, codeJSON(code))

	require.Equal(t, http.StatusUnprocessableEntity, status)
	assert.Equal(t, "quote_expired", res["error"])
	assert.Equal(t, true, res["suggest_new_quote"])
	status, _ = e.do(t, http.MethodGet, "/v1/assisted-quotes/"+quoteID+"/formalization", advisor, "")
	assert.Equal(t, http.StatusNotFound, status, "nothing was formalized")
}

func TestCA5AWrongOrMissingCodeFormalizesNothing(t *testing.T) {
	e := newFormalizationEnv(t)
	quoteID := e.quote(t)

	status, res := e.do(t, http.MethodPost, "/v1/assisted-quotes/"+quoteID+"/formalization", advisor, codeJSON("123456"))
	require.Equal(t, http.StatusUnprocessableEntity, status)
	assert.Equal(t, "consent_code_required", res["error"], "the advisor cannot formalize on their own word")

	code := e.consent(t, quoteID)
	wrong := "000000"
	if code == wrong {
		wrong = "111111"
	}
	status, res = e.do(t, http.MethodPost, "/v1/assisted-quotes/"+quoteID+"/formalization", advisor, codeJSON(wrong))
	require.Equal(t, http.StatusUnprocessableEntity, status)
	assert.Equal(t, "invalid_confirmation_code", res["error"])
	status, _ = e.do(t, http.MethodGet, "/v1/assisted-quotes/"+quoteID+"/formalization", advisor, "")
	assert.Equal(t, http.StatusNotFound, status)
}

func TestCA6TwoAdvisorsAtOnceOnlyOneFormalizes(t *testing.T) {
	e := newFormalizationEnv(t)
	quoteID := e.quote(t)
	code := e.consent(t, quoteID)

	const advisors = 6
	var wg sync.WaitGroup
	statuses := make([]int, advisors)
	acceptances := make([]string, advisors)
	start := make(chan struct{})
	for i := 0; i < advisors; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			token := "advisor:adv-" + string(rune('a'+i))
			req, err := http.NewRequest(http.MethodPost, e.srv.URL+"/v1/assisted-quotes/"+quoteID+"/formalization", strings.NewReader(codeJSON(code)))
			assert.NoError(t, err)
			req.Header.Set("Authorization", "Bearer "+token)
			res, err := http.DefaultClient.Do(req)
			if !assert.NoError(t, err) {
				return
			}
			defer func() { _ = res.Body.Close() }()
			var body map[string]any
			_ = json.NewDecoder(res.Body).Decode(&body)
			statuses[i] = res.StatusCode
			if id, ok := body["acceptance_id"].(string); ok {
				acceptances[i] = id
			}
		}()
	}
	close(start)
	wg.Wait()

	created, conflicts := 0, 0
	for i, s := range statuses {
		switch s {
		case http.StatusCreated:
			created++
			assert.NotEmpty(t, acceptances[i])
		case http.StatusConflict:
			conflicts++
		default:
			t.Fatalf("advisor %d: unexpected status %d", i, s)
		}
	}
	assert.Equal(t, 1, created, "a single formalization completes")
	assert.Equal(t, advisors-1, conflicts, "the others are rejected: no double issuance")
}

func TestRepeatingTheFormalizationDoesNotIssueTwice(t *testing.T) {
	e := newFormalizationEnv(t)
	quoteID := e.quote(t)
	code := e.consent(t, quoteID)
	status, first := e.do(t, http.MethodPost, "/v1/assisted-quotes/"+quoteID+"/formalization", advisor, codeJSON(code))
	require.Equal(t, http.StatusCreated, status)

	status, again := e.do(t, http.MethodPost, "/v1/assisted-quotes/"+quoteID+"/formalization", advisor, codeJSON(code))

	require.Equal(t, http.StatusOK, status)
	assert.Equal(t, first["acceptance_id"], again["acceptance_id"])
}

func TestOnlyAnAdvisorFormalizes(t *testing.T) {
	e := newFormalizationEnv(t)
	quoteID := e.quote(t)

	status, _ := e.do(t, http.MethodPost, "/v1/assisted-quotes/"+quoteID+"/consent", "insured:cust-1", "")
	assert.Equal(t, http.StatusForbidden, status)
	status, _ = e.do(t, http.MethodPost, "/v1/assisted-quotes/"+quoteID+"/formalization", "", codeJSON("123456"))
	assert.Equal(t, http.StatusUnauthorized, status)
}
