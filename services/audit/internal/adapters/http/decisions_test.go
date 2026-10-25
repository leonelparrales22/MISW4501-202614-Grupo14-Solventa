package httpadapter_test

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

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/security"
	httpadapter "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/audit/internal/adapters/http"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/audit/internal/decisions"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/audit/internal/domain"
)

type memDecisions struct {
	mu   sync.Mutex
	rows map[string]domain.DecisionRecord
	err  error
}

func (m *memDecisions) Create(_ context.Context, r domain.DecisionRecord) (domain.DecisionRecord, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return domain.DecisionRecord{}, false, m.err
	}
	if existing, ok := m.rows[r.OfferID]; ok {
		return existing, false, nil
	}
	if m.rows == nil {
		m.rows = map[string]domain.DecisionRecord{}
	}
	m.rows[r.OfferID] = r
	return r, true, nil
}

func (m *memDecisions) Get(_ context.Context, id string) (domain.DecisionRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if r, ok := m.rows[id]; ok {
		return r, nil
	}
	return domain.DecisionRecord{}, domain.ErrDecisionNotFound
}

const decisionJSON = `{"offer_id":"off-1","customer_id":"cus-1","partner_id":"partner-1","application_id":"app-1",
"channel":"embedded","requested_at":"2026-10-20T10:04:05-05:00","rater_version":"v1",
"sources":[{"source":"open_finance","status":"ok","latency_ms":80},{"source":"kyc","status":"timeout","latency_ms":700}],
"profile":{"score":0.4,"category":"medium","partial":true},
"rules":[{"order":1,"code":"base_monthly_rate","value":0.001},{"order":2,"code":"age_factor","value":1.2}],
"premium":120000,"currency":"COP","decision":"deferred","decision_reason":"kyc_unavailable"}`

func newDecisionHandler(store *memDecisions) http.Handler {
	svc := decisions.NewService(store, fixedClock{now: granted})
	verifier := tokenVerifier{
		"operations": {Subject: "u-ops", Roles: []security.Role{security.RoleOperations}},
		"advisor":    {Subject: "u-adv", Roles: []security.Role{security.RoleAdvisor}},
	}
	return httpadapter.NewRouter(httpadapter.WithDecisionRecords(svc), httpadapter.WithDecisionReads(svc, verifier))
}

func doDecision(h http.Handler, method, target, body, token string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestRecordThenReadDecision(t *testing.T) {
	h := newDecisionHandler(&memDecisions{})

	rec := doDecision(h, http.MethodPost, "/v1/audit/decisions", decisionJSON, "")
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))

	again := doDecision(h, http.MethodPost, "/v1/audit/decisions", decisionJSON, "")
	assert.Equal(t, http.StatusOK, again.Code, "recording the same offer twice is idempotent")

	read := doDecision(h, http.MethodGet, "/v1/audit/decisions/off-1", "", "operations")
	require.Equal(t, http.StatusOK, read.Code, read.Body.String())
	var body map[string]any
	require.NoError(t, json.Unmarshal(read.Body.Bytes(), &body))
	assert.Equal(t, "deferred", body["decision"])
	assert.Equal(t, "kyc_unavailable", body["decision_reason"])
	assert.Equal(t, "v1", body["rater_version"])
	assert.Equal(t, "2026-10-20T15:04:05Z", body["requested_at"], "UTC instant of the request")
	assert.Equal(t, granted.Format(time.RFC3339), body["recorded_at"])
	assert.Len(t, body["sources"], 2)
	rules := body["rules"].([]any)
	require.Len(t, rules, 2)
	assert.Equal(t, "base_monthly_rate", rules[0].(map[string]any)["code"], "rules keep their execution order")
}

func TestReadDecisionNotFoundHidesInternals(t *testing.T) {
	h := newDecisionHandler(&memDecisions{})
	rec := doDecision(h, http.MethodGet, "/v1/audit/decisions/nope", "", "operations")
	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.JSONEq(t, `{"error":"decision_not_found","message":"no audit record was found for that identifier"}`, rec.Body.String())
}

func TestReadDecisionRequiresOperationsRole(t *testing.T) {
	h := newDecisionHandler(&memDecisions{})
	assert.Equal(t, http.StatusUnauthorized, doDecision(h, http.MethodGet, "/v1/audit/decisions/off-1", "", "").Code)
	assert.Equal(t, http.StatusForbidden, doDecision(h, http.MethodGet, "/v1/audit/decisions/off-1", "", "advisor").Code)
}

func TestRecordDecisionRejectsInvalidAndReadOnly(t *testing.T) {
	h := newDecisionHandler(&memDecisions{})
	assert.Equal(t, http.StatusBadRequest, doDecision(h, http.MethodPost, "/v1/audit/decisions", `not json`, "").Code)

	rec := doDecision(h, http.MethodPost, "/v1/audit/decisions", `{"offer_id":"x"}`, "")
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "validation_failed")

	// The record is read-only: there is no route to change or delete it.
	for _, method := range []string{http.MethodPut, http.MethodPatch, http.MethodDelete} {
		assert.Equal(t, http.StatusMethodNotAllowed, doDecision(h, method, "/v1/audit/decisions/off-1", "", "operations").Code, method)
	}
}

func TestRecordDecisionStoreDown(t *testing.T) {
	h := newDecisionHandler(&memDecisions{err: assert.AnError})
	assert.Equal(t, http.StatusServiceUnavailable, doDecision(h, http.MethodPost, "/v1/audit/decisions", decisionJSON, "").Code)
}
