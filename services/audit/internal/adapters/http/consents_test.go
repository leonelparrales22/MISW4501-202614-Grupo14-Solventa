package httpadapter_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/security"
	httpadapter "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/audit/internal/adapters/http"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/audit/internal/consents"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/audit/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/audit/internal/ports"
)

type memStore struct {
	mu   sync.Mutex
	rows []domain.Consent
	err  error
}

func (m *memStore) Create(_ context.Context, c domain.Consent) (domain.Consent, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return domain.Consent{}, false, m.err
	}
	for _, r := range m.rows {
		if r.CustomerID == c.CustomerID && r.ApplicationID == c.ApplicationID && r.TermsVersion == c.TermsVersion {
			return r, false, nil
		}
	}
	m.rows = append(m.rows, c)
	return c, true, nil
}

func (m *memStore) Get(_ context.Context, id string) (domain.Consent, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return domain.Consent{}, m.err
	}
	for _, r := range m.rows {
		if r.ID == id {
			return r, nil
		}
	}
	return domain.Consent{}, ports.ErrConsentNotFound
}

func (m *memStore) LatestByCustomer(_ context.Context, customerID string) (domain.Consent, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return domain.Consent{}, m.err
	}
	for i := len(m.rows) - 1; i >= 0; i-- {
		if m.rows[i].CustomerID == customerID {
			return m.rows[i], nil
		}
	}
	return domain.Consent{}, ports.ErrConsentNotFound
}

type fixedClock struct{ now time.Time }

func (c fixedClock) Now() time.Time { return c.now }

// tokenVerifier maps a bearer token to claims.
type tokenVerifier map[string]security.Claims

func (v tokenVerifier) Verify(_ context.Context, raw string) (security.Claims, error) {
	if c, ok := v[raw]; ok {
		return c, nil
	}
	return security.Claims{}, security.ErrInvalidToken
}

var granted = time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)

type env struct {
	handler http.Handler
	store   *memStore
	clock   *fixedClock
}

func newEnv(t *testing.T) *env {
	t.Helper()
	store := &memStore{}
	clock := &fixedClock{now: granted}
	svc := consents.NewService(store, clock)
	verifier := tokenVerifier{
		"operations": {Subject: "u-ops", Roles: []security.Role{security.RoleOperations}},
		"advisor":    {Subject: "u-adv", Roles: []security.Role{security.RoleAdvisor}},
		"insured":    {Subject: "u-ins", Roles: []security.Role{security.RoleInsured}},
		"partner":    {Subject: "u-par", Roles: []security.Role{security.RolePartner}},
	}
	return &env{
		handler: httpadapter.NewRouter(httpadapter.WithConsents(svc), httpadapter.WithConsentReads(svc, verifier)),
		store:   store,
		clock:   clock,
	}
}

const grantBody = `{"customer_id":"cust-1","document_id":"1020304050","full_name":"Ana María Pérez","terms_version":"v1",
"accepted_sources":["open_finance","open_data","kyc"],"accepted_data_policy":true,"channel":"embedded",
"partner_id":"partner-1","application_id":"app-1"}`

func (e *env) do(method, target, body, token string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 test")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	e.handler.ServeHTTP(rec, req)
	return rec
}

func decode(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var out map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out), rec.Body.String())
	return out
}

func TestHealth(t *testing.T) {
	rec := newEnv(t).do(http.MethodGet, "/health", "", "")

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "ok", rec.Body.String())
}

func TestHealthWithoutAnyOption(t *testing.T) {
	rec := httptest.NewRecorder()
	httpadapter.NewRouter().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))

	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestCurrentTermsShowTheSourcesDataAndPurpose(t *testing.T) {
	rec := newEnv(t).do(http.MethodGet, "/v1/audit/consent-terms/current", "", "")

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
	body := decode(t, rec)
	assert.Equal(t, "v1", body["version"])
	assert.Equal(t, "risk_profiling_mortgage_life_offer", body["purpose"])
	assert.Equal(t, "Solventa S.A.", body["controller"])
	assert.Equal(t, "personal-data-policy-v1", body["policy_ref"])
	sources := body["sources"].([]any)
	require.Len(t, sources, 3)
	first := sources[0].(map[string]any)
	assert.Equal(t, "open_finance", first["code"])
	assert.Equal(t, []any{"credit_products", "balances", "payment_behavior"}, first["data_categories"])
	assert.Equal(t, "12_months", first["validity"])
	assert.Equal(t, "single_use", sources[2].(map[string]any)["validity"])
}

func TestGrantCreatesTheConsentThenReturnsTheSameOnRepeat(t *testing.T) {
	e := newEnv(t)

	first := e.do(http.MethodPost, "/v1/audit/consents", grantBody, "")
	require.Equal(t, http.StatusCreated, first.Code, first.Body.String())
	assert.Equal(t, "no-store", first.Header().Get("Cache-Control"))
	body := decode(t, first)
	assert.Regexp(t, `^cns-[0-9a-f]{32}$`, body["consent_id"])
	assert.Equal(t, "cust-1", body["customer_id"])
	assert.Equal(t, "v1", body["terms_version"])
	assert.Equal(t, "embedded", body["channel"])
	assert.Equal(t, "2026-10-17T12:00:00Z", body["granted_at"])
	assert.Equal(t, "2027-10-17T12:00:00Z", body["expires_at"])
	assert.Len(t, body["scope"], 3)
	assert.NotContains(t, first.Body.String(), "1020304050", "the receipt does not echo the document")
	assert.Equal(t, "Mozilla/5.0 test", e.store.rows[0].UserAgent, "the user agent comes from the header")

	e.clock.now = granted.Add(time.Hour)
	again := e.do(http.MethodPost, "/v1/audit/consents", grantBody, "")
	require.Equal(t, http.StatusOK, again.Code)
	assert.Equal(t, body, decode(t, again))
	assert.Len(t, e.store.rows, 1)
}

func TestGrantReportsEveryInvalidField(t *testing.T) {
	rec := newEnv(t).do(http.MethodPost, "/v1/audit/consents", `{"customer_id":"bad id","accepted_sources":["open_finance"]}`, "")

	require.Equal(t, http.StatusBadRequest, rec.Code)
	body := decode(t, rec)
	assert.Equal(t, "validation_failed", body["error"])
	fields := map[string]string{}
	for _, f := range body["fields"].([]any) {
		m := f.(map[string]any)
		fields[m["field"].(string)] = m["code"].(string)
	}
	assert.Equal(t, "invalid_format", fields["customer_id"])
	assert.Equal(t, "incomplete", fields["accepted_sources"])
	assert.Equal(t, "must_be_true", fields["accepted_data_policy"])
	assert.Equal(t, "required", fields["document_id"])
}

func TestGrantWithOutdatedTermsIsAConflict(t *testing.T) {
	e := newEnv(t)

	rec := e.do(http.MethodPost, "/v1/audit/consents", strings.Replace(grantBody, `"v1"`, `"v0"`, 1), "")

	require.Equal(t, http.StatusConflict, rec.Code)
	assert.Equal(t, "terms_outdated", decode(t, rec)["error"])
	assert.Empty(t, e.store.rows)
}

func TestGrantRejectsMalformedBodies(t *testing.T) {
	e := newEnv(t)
	tests := map[string]string{
		"not JSON":      `{"customer_id":`,
		"empty":         ``,
		"wrong type":    `{"customer_id":1}`,
		"oversize body": `{"customer_id":"` + strings.Repeat("a", 1<<15) + `"}`,
	}
	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			rec := e.do(http.MethodPost, "/v1/audit/consents", body, "")

			require.Equal(t, http.StatusBadRequest, rec.Code)
			assert.Equal(t, "invalid_request", decode(t, rec)["error"])
		})
	}
	assert.Empty(t, e.store.rows)
}

func TestGrantFailsWith503WhenTheStoreIsDown(t *testing.T) {
	e := newEnv(t)
	e.store.err = errors.New("db down")

	rec := e.do(http.MethodPost, "/v1/audit/consents", grantBody, "")

	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	assert.Equal(t, "service_unavailable", decode(t, rec)["error"])
	assert.NotContains(t, rec.Body.String(), "db down", "the cause stays in the log")
}

func TestVerificationWithoutConsentIsInactive(t *testing.T) {
	rec := newEnv(t).do(http.MethodGet, "/v1/audit/consent-verifications?customer_id=nobody", "", "")

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
	assert.JSONEq(t, `{"active":false,"consent_id":null,"granted_at":null,"expires_at":null,"sources":[]}`, rec.Body.String())
}

func TestVerificationOfAnActiveConsent(t *testing.T) {
	e := newEnv(t)
	id := decode(t, e.do(http.MethodPost, "/v1/audit/consents", grantBody, ""))["consent_id"]

	rec := e.do(http.MethodGet, "/v1/audit/consent-verifications?customer_id=cust-1", "", "")

	require.Equal(t, http.StatusOK, rec.Code)
	assert.JSONEq(t, `{"active":true,"consent_id":"`+id.(string)+`","granted_at":"2026-10-17T12:00:00Z",
		"expires_at":"2027-10-17T12:00:00Z","sources":["open_finance","open_data","kyc"]}`, rec.Body.String())
}

func TestVerificationOfAnExpiredConsentIsInactiveButKeepsTheEvidence(t *testing.T) {
	e := newEnv(t)
	id := decode(t, e.do(http.MethodPost, "/v1/audit/consents", grantBody, ""))["consent_id"]
	e.clock.now = time.Date(2027, 10, 17, 12, 0, 0, 0, time.UTC)

	rec := e.do(http.MethodGet, "/v1/audit/consent-verifications?customer_id=cust-1", "", "")

	require.Equal(t, http.StatusOK, rec.Code)
	assert.JSONEq(t, `{"active":false,"consent_id":"`+id.(string)+`","granted_at":"2026-10-17T12:00:00Z",
		"expires_at":"2027-10-17T12:00:00Z","sources":[]}`, rec.Body.String())
}

func TestVerificationRequiresAValidCustomerID(t *testing.T) {
	e := newEnv(t)
	for _, target := range []string{
		"/v1/audit/consent-verifications",
		"/v1/audit/consent-verifications?customer_id=",
		"/v1/audit/consent-verifications?customer_id=" + strings.Repeat("a", 129),
	} {
		rec := e.do(http.MethodGet, target, "", "")

		require.Equal(t, http.StatusBadRequest, rec.Code, target)
		assert.Equal(t, "invalid_request", decode(t, rec)["error"])
	}
}

func TestVerificationFailsWith503WhenTheStoreIsDown(t *testing.T) {
	e := newEnv(t)
	e.store.err = errors.New("db down")

	rec := e.do(http.MethodGet, "/v1/audit/consent-verifications?customer_id=cust-1", "", "")

	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	assert.Equal(t, "service_unavailable", decode(t, rec)["error"])
}

func TestEvidenceIsReadableByOperationsAndAdvisors(t *testing.T) {
	e := newEnv(t)
	id := decode(t, e.do(http.MethodPost, "/v1/audit/consents", grantBody, ""))["consent_id"].(string)

	for _, token := range []string{"operations", "advisor"} {
		rec := e.do(http.MethodGet, "/v1/audit/consents/"+id, "", token)

		require.Equal(t, http.StatusOK, rec.Code, token)
		assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
		body := decode(t, rec)
		assert.Equal(t, id, body["consent_id"])
		assert.Equal(t, map[string]any{"document_id": "1020304050", "full_name": "Ana María Pérez"}, body["holder"])
		assert.Equal(t, "v1", body["terms_version"])
		assert.Equal(t, "risk_profiling_mortgage_life_offer", body["purpose"])
		assert.Equal(t, "embedded", body["channel"])
		assert.Equal(t, "partner-1", body["partner_id"])
		assert.Equal(t, "app-1", body["application_id"])
		assert.Equal(t, "Mozilla/5.0 test", body["user_agent"])
		assert.Equal(t, "2026-10-17T12:00:00Z", body["granted_at"])
		assert.Len(t, body["scope"], 3)
	}
}

func TestEvidenceRequiresAToken(t *testing.T) {
	rec := newEnv(t).do(http.MethodGet, "/v1/audit/consents/cns-x", "", "")

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestEvidenceRejectsAnInvalidToken(t *testing.T) {
	rec := newEnv(t).do(http.MethodGet, "/v1/audit/consents/cns-x", "", "forged")

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestEvidenceIsForbiddenToOtherRoles(t *testing.T) {
	e := newEnv(t)
	id := decode(t, e.do(http.MethodPost, "/v1/audit/consents", grantBody, ""))["consent_id"].(string)

	for _, token := range []string{"insured", "partner"} {
		rec := e.do(http.MethodGet, "/v1/audit/consents/"+id, "", token)

		assert.Equal(t, http.StatusForbidden, rec.Code, token)
		assert.NotContains(t, rec.Body.String(), "1020304050")
	}
}

func TestEvidenceOfAnUnknownConsentIsNotFound(t *testing.T) {
	e := newEnv(t)
	for _, id := range []string{"cns-unknown", strings.Repeat("a", 65)} {
		rec := e.do(http.MethodGet, "/v1/audit/consents/"+id, "", "operations")

		require.Equal(t, http.StatusNotFound, rec.Code, id)
		assert.Equal(t, "consent_not_found", decode(t, rec)["error"])
	}
}

func TestEvidenceFailsWith503WhenTheStoreIsDown(t *testing.T) {
	e := newEnv(t)
	e.store.err = errors.New("db down")

	rec := e.do(http.MethodGet, "/v1/audit/consents/cns-1", "", "operations")

	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
}

func TestTheEvidenceRouteIsNotMountedWithoutReads(t *testing.T) {
	svc := consents.NewService(&memStore{}, fixedClock{now: granted})
	handler := httpadapter.NewRouter(httpadapter.WithConsents(svc))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/audit/consents/cns-1", nil))

	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestObservabilityMiddlewareAddsTheCorrelationID(t *testing.T) {
	svc := consents.NewService(&memStore{}, fixedClock{now: granted})
	handler := httpadapter.NewRouter(httpadapter.WithObservability("audit"), httpadapter.WithConsents(svc))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/audit/consent-terms/current", nil))

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.NotEmpty(t, rec.Header().Get("X-Correlation-Id"))
}
