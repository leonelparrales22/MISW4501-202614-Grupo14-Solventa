// This file wires the advisor quote of HU-W1.1-01 end to end over HTTP (router,
// pkg/security middleware, use case, the real profiling over controllable
// external sources, the rating engine and the in-memory stores) and checks each
// acceptance criterion.
package integration_test

import (
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
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/orchestrator"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/rating"
)

type steppingClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *steppingClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *steppingClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

type quoteEnv struct {
	*parallelEnv
	srv   *httptest.Server
	clock *steppingClock
}

func newQuoteEnv(t *testing.T) *quoteEnv {
	t.Helper()
	pe, _ := newActuarialEnv(t, true)
	clock := &steppingClock{t: time.Now().UTC()}
	svc := orchestrator.NewAssistedQuoteService(memory.NewCustomerStore(), memory.NewQuoteStore(), pe.resolver, rating.New(), clock)
	e := &quoteEnv{parallelEnv: pe, clock: clock}
	e.srv = httptest.NewServer(httpadapter.NewRouter(pe.resolver,
		httpadapter.WithObservability("origination"), httpadapter.WithAssistedQuotes(svc, subjectVerifier{})))
	t.Cleanup(e.srv.Close)
	return e
}

func (e *quoteEnv) do(t *testing.T, method, path, token, body string) (int, map[string]any) {
	t.Helper()
	req, err := http.NewRequest(method, e.srv.URL+path, strings.NewReader(body))
	require.NoError(t, err)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = res.Body.Close() }()
	var m map[string]any
	_ = json.NewDecoder(res.Body).Decode(&m)
	return res.StatusCode, m
}

const advisor = "advisor:adv-1"

func quoteBody(document string) string {
	return `{"document_id":"` + document + `","full_name":"Ana María Pérez","email":"ana@example.com",` +
		`"birth_date":"1990-05-20","insured_amount":250000000,"term_months":180,"consent_active":true}`
}

func TestAdvisorQuotesWithTheSameEngineAndReadsItBack(t *testing.T) {
	e := newQuoteEnv(t)

	status, quote := e.do(t, http.MethodPost, "/v1/assisted-quotes", advisor, quoteBody("1032456789"))

	require.Equal(t, http.StatusCreated, status)
	assert.Equal(t, "normal", quote["mode"])
	assert.Equal(t, "external_sources", quote["source"])
	assert.Positive(t, quote["premium"])
	assert.Len(t, quote["coverages"], 2)
	assert.Equal(t, "valid", quote["status"])

	// The score is the one the embedded channel gets for the same customer:
	// the quote reuses the profile resolver (criterion 1).
	embedded := e.resolve(t, quote["customer_id"].(string))
	assert.InDelta(t, embedded.body["profile"].(map[string]any)["score"], quote["profile"].(map[string]any)["score"], 1e-9)

	id := quote["quote_id"].(string)
	status, read := e.do(t, http.MethodGet, "/v1/assisted-quotes/"+id, advisor, "")
	require.Equal(t, http.StatusOK, status)
	assert.Equal(t, quote["premium"], read["premium"])
	assert.Equal(t, quote["expires_at"], read["expires_at"], "criterion 4: the quote is persisted with its validity")
}

func TestExistingCustomerIsRecoveredByDocument(t *testing.T) {
	e := newQuoteEnv(t)

	status, _ := e.do(t, http.MethodGet, "/v1/customers?document_id=1032456789", advisor, "")
	assert.Equal(t, http.StatusNotFound, status)

	status, quote := e.do(t, http.MethodPost, "/v1/assisted-quotes", advisor, quoteBody("1032456789"))
	require.Equal(t, http.StatusCreated, status)

	status, customer := e.do(t, http.MethodGet, "/v1/customers?document_id=1032456789", advisor, "")
	require.Equal(t, http.StatusOK, status)
	assert.Equal(t, quote["customer_id"], customer["customer_id"], "criterion 2: no need to type the data again")
	assert.Equal(t, "Ana María Pérez", customer["full_name"])
	assert.Equal(t, "1990-05-20", customer["birth_date"])
}

func TestInvalidFieldsAreReportedAndNoQuoteIsGenerated(t *testing.T) {
	e := newQuoteEnv(t)
	body := `{"document_id":"12","full_name":"","email":"nope","birth_date":"1990-05-20","insured_amount":50,"term_months":180,"consent_active":true}`

	status, res := e.do(t, http.MethodPost, "/v1/assisted-quotes", advisor, body)

	require.Equal(t, http.StatusUnprocessableEntity, status)
	assert.Equal(t, "validation_failed", res["error"])
	fields := map[string]string{}
	for _, f := range res["fields"].([]any) {
		m := f.(map[string]any)
		fields[m["field"].(string)] = m["code"].(string)
	}
	assert.Equal(t, map[string]string{
		"document_id": "invalid_format", "full_name": "required", "email": "invalid_format", "insured_amount": "out_of_range",
	}, fields, "criterion 3: each field carries its specific error")
	assert.Zero(t, e.hits(), "nothing was profiled")
}

func TestQuoteIsDegradedWhenTheExternalSourcesAreDown(t *testing.T) {
	e := newQuoteEnv(t)
	e.of.down.Store(true)
	e.od.down.Store(true)
	e.kyc.down.Store(true)

	status, quote := e.do(t, http.MethodPost, "/v1/assisted-quotes", advisor, quoteBody("1032456789"))

	require.Equal(t, http.StatusCreated, status, "criterion 5: the quote is generated anyway")
	assert.Equal(t, "provisional", quote["mode"])
	assert.Equal(t, "base_actuarial", quote["source"])
	assert.Equal(t, "personalization_unavailable", quote["notice"])
	assert.Equal(t, "open_finance", quote["unavailable_source"])
	assert.Positive(t, quote["premium"])
}

func TestDegradedQuoteUsesTheLastCachedProfileWhenThereIsOne(t *testing.T) {
	e := newQuoteEnv(t)
	_, first := e.do(t, http.MethodPost, "/v1/assisted-quotes", advisor, quoteBody("1032456789"))
	e.of.down.Store(true)
	e.od.down.Store(true)
	e.kyc.down.Store(true)

	// The complete profile is reused without querying the sources (HU-W1.2-02),
	// so the second quote is normal and cached.
	status, second := e.do(t, http.MethodPost, "/v1/assisted-quotes", advisor, quoteBody("1032456789"))

	require.Equal(t, http.StatusCreated, status)
	assert.Equal(t, "cache", second["source"])
	assert.Equal(t, first["customer_id"], second["customer_id"])
	assert.Equal(t, first["premium"], second["premium"], "the same profile gives the same price")
}

func TestExpiredQuoteCannotBeFormalized(t *testing.T) {
	e := newQuoteEnv(t)
	_, quote := e.do(t, http.MethodPost, "/v1/assisted-quotes", advisor, quoteBody("1032456789"))
	id := quote["quote_id"].(string)

	e.clock.advance(23*time.Hour + 59*time.Minute)
	_, read := e.do(t, http.MethodGet, "/v1/assisted-quotes/"+id, advisor, "")
	assert.Equal(t, true, read["can_formalize"])

	e.clock.advance(2 * time.Minute)
	status, read := e.do(t, http.MethodGet, "/v1/assisted-quotes/"+id, advisor, "")
	require.Equal(t, http.StatusOK, status)
	assert.Equal(t, "expired", read["status"], "criterion 6")
	assert.Equal(t, false, read["can_formalize"])
	assert.Equal(t, "quote_expired", read["blocked_reason"])
}

func TestConsentIsRequiredToQuote(t *testing.T) {
	e := newQuoteEnv(t)
	body := strings.Replace(quoteBody("1032456789"), `"consent_active":true`, `"consent_active":false`, 1)

	status, res := e.do(t, http.MethodPost, "/v1/assisted-quotes", advisor, body)

	assert.Equal(t, http.StatusUnprocessableEntity, status)
	assert.Equal(t, "consent_required", res["error"])
	assert.Zero(t, e.hits())
}

func TestOnlyAdvisorsUseTheQuoteRoutes(t *testing.T) {
	e := newQuoteEnv(t)
	for _, tc := range []struct {
		token string
		want  int
	}{
		{"", http.StatusUnauthorized},
		{"garbage", http.StatusUnauthorized},
		{"insured:ins-1", http.StatusForbidden},
		{"operations:ops-1", http.StatusForbidden},
		{"partner:p-1", http.StatusForbidden},
	} {
		status, _ := e.do(t, http.MethodPost, "/v1/assisted-quotes", tc.token, quoteBody("1032456789"))
		assert.Equal(t, tc.want, status, "token %q", tc.token)
		status, _ = e.do(t, http.MethodGet, "/v1/customers?document_id=1032456789", tc.token, "")
		assert.Equal(t, tc.want, status, "token %q", tc.token)
	}
}
