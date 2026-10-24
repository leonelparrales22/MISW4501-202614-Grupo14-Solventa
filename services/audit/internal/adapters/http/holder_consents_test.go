package httpadapter_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/security"
	httpadapter "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/audit/internal/adapters/http"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/audit/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/audit/internal/ports"
)

// holderFake is the holder use case: two holders, A (sub-a) and B (sub-b),
// and an account without a link (sub-none).
type holderFake struct {
	docs     map[string]string
	views    map[string][]domain.ConsentView
	err      error
	resolveE error
}

func (f *holderFake) Resolve(_ context.Context, subject string) (string, error) {
	if f.resolveE != nil {
		return "", f.resolveE
	}
	doc, ok := f.docs[subject]
	if !ok {
		return "", ports.ErrHolderNotLinked
	}
	return doc, nil
}

func (f *holderFake) List(_ context.Context, document string) ([]domain.ConsentView, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.views[document], nil
}

func (f *holderFake) Get(_ context.Context, document, id string) (domain.ConsentView, error) {
	if f.err != nil {
		return domain.ConsentView{}, f.err
	}
	for _, v := range f.views[document] {
		if v.Consent.ID == id {
			return v, nil
		}
	}
	return domain.ConsentView{}, ports.ErrConsentNotFound
}

var holderNow = time.Date(2026, 10, 24, 20, 0, 0, 0, time.UTC)

func holderView(id, document string, events []domain.ConsentEvent, granted time.Time) domain.ConsentView {
	terms := domain.TermsV1()
	c := domain.Consent{
		ID: id, CustomerID: "cust-" + id, Holder: domain.Holder{DocumentID: document, FullName: "Ana María Pérez"},
		TermsVersion: terms.Version, Scope: terms.Sources, Purpose: terms.Purpose, Channel: domain.ChannelEmbedded,
		PartnerID: "aliado-banco-1", ApplicationID: "app-" + id, UserAgent: "UA", GrantedAt: granted,
	}
	return domain.View(c, events, holderNow, domain.EntityNames{"aliado-banco-1": "Banco Aliado"})
}

func newHolderFake() *holderFake {
	revokedAt := holderNow.AddDate(0, 0, -3)
	return &holderFake{
		docs: map[string]string{"sub-a": "1020304050", "sub-b": "9988776655"},
		views: map[string][]domain.ConsentView{
			"1020304050": {
				holderView("cns-a-active", "1020304050", nil, holderNow.AddDate(0, -1, 0)),
				holderView("cns-a-revoked", "1020304050", []domain.ConsentEvent{{
					EventID: "e1", ConsentID: "cns-a-revoked", Status: domain.HistoryRevoked, OccurredAt: revokedAt,
					RecordedAt: revokedAt, ReceiptID: "REV-0001", Channel: "mobile",
				}}, holderNow.AddDate(0, -4, 0)),
			},
			"9988776655": {holderView("cns-b-active", "9988776655", nil, holderNow.AddDate(0, -2, 0))},
		},
	}
}

func holderRouter(svc httpadapter.HolderConsentService) http.Handler {
	verifier := tokenVerifier{
		"insured-a":    {Subject: "sub-a", Roles: []security.Role{security.RoleInsured}},
		"insured-b":    {Subject: "sub-b", Roles: []security.Role{security.RoleInsured}},
		"insured-none": {Subject: "sub-none", Roles: []security.Role{security.RoleInsured}},
		"operations":   {Subject: "u-ops", Roles: []security.Role{security.RoleOperations}},
		"advisor":      {Subject: "u-adv", Roles: []security.Role{security.RoleAdvisor}},
	}
	return httpadapter.NewRouter(httpadapter.WithHolderConsents(svc, verifier))
}

func call(h http.Handler, method, target, token string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestTheHolderListsOnlyTheirConsents(t *testing.T) {
	h := holderRouter(newHolderFake())

	rec := call(h, http.MethodGet, "/v1/consents", "insured-a")

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
	var out struct {
		Items []map[string]any `json:"items"`
		Gen   string           `json:"generated_at"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	require.Len(t, out.Items, 2)
	assert.NotEmpty(t, out.Gen)
	active := out.Items[0]
	assert.Equal(t, "cns-a-active", active["consent_id"])
	assert.Equal(t, "active", active["status"])
	assert.Equal(t, true, active["current"])
	assert.Equal(t, []any{map[string]any{"code": "aliado-banco-1", "name": "Banco Aliado"}}, active["entities"])
	assert.Equal(t, "risk_profiling_mortgage_life_offer", active["purpose"])
	assert.Equal(t, "embedded", active["channel"])
	assert.Equal(t, "v1", active["terms_version"])
	assert.Len(t, active["scope"], 3)
	assert.NotEmpty(t, active["granted_at"])
	assert.NotEmpty(t, active["expires_at"])
	assert.Nil(t, active["ended_at"])
	assert.Nil(t, active["revocation_receipt_id"])
	history := active["history"].([]any)
	require.Len(t, history, 1)
	assert.Equal(t, map[string]any{"status": "granted", "occurred_at": history[0].(map[string]any)["occurred_at"],
		"receipt_id": nil, "channel": "embedded", "entity_code": nil}, history[0])

	revoked := out.Items[1]
	assert.Equal(t, "revoked", revoked["status"])
	assert.Equal(t, false, revoked["current"])
	assert.Equal(t, "REV-0001", revoked["revocation_receipt_id"])
	assert.Equal(t, "2026-10-21T20:00:00Z", revoked["ended_at"])

	body := rec.Body.String()
	assert.NotContains(t, body, "1020304050", "the holder's document is never in the response")
	assert.NotContains(t, body, "Ana María", "nor the holder's name")
	assert.NotContains(t, body, "cns-b-active")
}

func TestTheHolderReadsOneOfTheirConsentsWithItsHistory(t *testing.T) {
	h := holderRouter(newHolderFake())

	rec := call(h, http.MethodGet, "/v1/consents/cns-a-revoked", "insured-a")

	require.Equal(t, http.StatusOK, rec.Code)
	var out map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	assert.Equal(t, "cns-a-revoked", out["consent_id"])
	history := out["history"].([]any)
	require.Len(t, history, 2)
	last := history[1].(map[string]any)
	assert.Equal(t, "revoked", last["status"])
	assert.Equal(t, "REV-0001", last["receipt_id"])
	assert.Equal(t, "mobile", last["channel"])
	assert.NotContains(t, rec.Body.String(), "1020304050")
}

func TestAConsentOfAnotherHolderIsNotFound(t *testing.T) {
	h := holderRouter(newHolderFake())

	rec := call(h, http.MethodGet, "/v1/consents/cns-a-active", "insured-b")

	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.JSONEq(t, `{"error":"not_found","message":"there is no consent with that id"}`, rec.Body.String())

	rec = call(h, http.MethodGet, "/v1/consents/"+strings.Repeat("x", 65), "insured-b")
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestTheHolderRoutesNeedATokenAndTheInsuredRole(t *testing.T) {
	h := holderRouter(newHolderFake())
	for _, target := range []string{"/v1/consents", "/v1/consents/cns-a-active"} {
		assert.Equal(t, http.StatusUnauthorized, call(h, http.MethodGet, target, "").Code, target)
		assert.Equal(t, http.StatusUnauthorized, call(h, http.MethodGet, target, "forged").Code, target)
		assert.Equal(t, http.StatusForbidden, call(h, http.MethodGet, target, "operations").Code, target)
		assert.Equal(t, http.StatusForbidden, call(h, http.MethodGet, target, "advisor").Code, target)
	}
}

func TestAnAccountWithoutALinkGetsHolderNotLinked(t *testing.T) {
	h := holderRouter(newHolderFake())
	for _, target := range []string{"/v1/consents", "/v1/consents/cns-a-active"} {
		rec := call(h, http.MethodGet, target, "insured-none")
		assert.Equal(t, http.StatusForbidden, rec.Code)
		var out map[string]string
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
		assert.Equal(t, "holder_not_linked", out["error"])
	}
}

func TestStoreFailuresAre503(t *testing.T) {
	down := newHolderFake()
	down.err = errors.New("db down")
	h := holderRouter(down)
	assert.Equal(t, http.StatusServiceUnavailable, call(h, http.MethodGet, "/v1/consents", "insured-a").Code)
	assert.Equal(t, http.StatusServiceUnavailable, call(h, http.MethodGet, "/v1/consents/cns-a-active", "insured-a").Code)

	unresolved := newHolderFake()
	unresolved.resolveE = errors.New("db down")
	rec := call(holderRouter(unresolved), http.MethodGet, "/v1/consents", "insured-a")
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	assert.Contains(t, rec.Body.String(), "service_unavailable")
}

// Criterion 4: the routes are read-only; no method changes a consent.
func TestTheHolderRoutesAreReadOnly(t *testing.T) {
	h := holderRouter(newHolderFake())
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		for _, target := range []string{"/v1/consents", "/v1/consents/cns-a-active"} {
			rec := call(h, method, target, "insured-a")
			assert.Equal(t, http.StatusMethodNotAllowed, rec.Code, "%s %s", method, target)
		}
	}
}

func TestAnEmptyListIsAnEmptyArray(t *testing.T) {
	fake := newHolderFake()
	fake.docs["sub-none"] = "0000000000"
	h := holderRouter(fake)

	rec := call(h, http.MethodGet, "/v1/consents", "insured-none")

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `"items":[]`)
}
