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

	httpadapter "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/http"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/profiling"
)

type fakeResolver struct {
	res         domain.ProfileResolution
	err         error
	gotCustomer string
	gotConsent  bool
}

func (f *fakeResolver) Resolve(_ context.Context, id string, consent bool) (domain.ProfileResolution, error) {
	f.gotCustomer, f.gotConsent = id, consent
	return f.res, f.err
}

func post(t *testing.T, r *fakeResolver, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/risk-profiles/resolve", strings.NewReader(body))
	rec := httptest.NewRecorder()
	httpadapter.NewRouter(r).ServeHTTP(rec, req)
	return rec
}

func decode(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &m))
	return m
}

func TestHealth(t *testing.T) {
	rec := httptest.NewRecorder()
	httpadapter.NewRouter(&fakeResolver{}).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestResolveNormal(t *testing.T) {
	at := time.Date(2026, 10, 9, 10, 0, 0, 0, time.UTC)
	f := &fakeResolver{res: domain.ProfileResolution{
		Profile: domain.RiskProfile{Score: 0.3, Category: "standard", CalculatedAt: at},
		Mode:    domain.ModeNormal, Source: domain.SourceOpenFinance,
	}}

	rec := post(t, f, `{"customer_id":"c1","consent_active":true}`)

	require.Equal(t, http.StatusOK, rec.Code)
	m := decode(t, rec)
	assert.Equal(t, "normal", m["mode"])
	assert.Equal(t, "open_finance", m["source"])
	assert.Nil(t, m["unavailable_source"])
	assert.Nil(t, m["notice"])
	assert.Equal(t, "2026-10-09T10:00:00Z", m["profile_date"])
	assert.Equal(t, "c1", f.gotCustomer)
	assert.True(t, f.gotConsent)
}

func TestResolveProvisionalFromCache(t *testing.T) {
	at := time.Date(2026, 9, 20, 10, 0, 0, 0, time.UTC)
	f := &fakeResolver{res: domain.ProfileResolution{
		Profile: domain.RiskProfile{Score: 0.3, Category: "standard", CalculatedAt: at},
		Mode:    domain.ModeProvisional, Source: domain.SourceCache, UnavailableSource: domain.ExternalSourceOpenFinance,
	}}

	m := decode(t, post(t, f, `{"customer_id":"c1","consent_active":true}`))

	assert.Equal(t, "provisional", m["mode"])
	assert.Equal(t, "cache", m["source"])
	assert.Equal(t, "open_finance", m["unavailable_source"])
	assert.Equal(t, httpadapter.NoticeLastProfileInUse, m["notice"])
	assert.Equal(t, "2026-09-20T10:00:00Z", m["profile_date"])
}

func TestResolveProvisionalFromBaseProfile(t *testing.T) {
	f := &fakeResolver{res: domain.ProfileResolution{
		Profile: domain.BaseActuarialProfile("new"),
		Mode:    domain.ModeProvisional, Source: domain.SourceBaseActuarial, UnavailableSource: domain.ExternalSourceOpenFinance,
	}}

	m := decode(t, post(t, f, `{"customer_id":"new","consent_active":true}`))

	assert.Equal(t, "base_actuarial", m["source"])
	assert.Equal(t, httpadapter.NoticePersonalizationUnavailable, m["notice"])
	assert.Nil(t, m["profile_date"], "the base profile has no calculation date")
	assert.Equal(t, "base_actuarial", m["profile"].(map[string]any)["category"])
}

func TestResolveConsentRequired(t *testing.T) {
	rec := post(t, &fakeResolver{err: profiling.ErrConsentRequired}, `{"customer_id":"c1","consent_active":false}`)

	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	assert.Equal(t, "consent_required", decode(t, rec)["error"])
}

func TestResolveInvalidRequests(t *testing.T) {
	bodies := map[string]string{
		"not json":           `nope`,
		"empty body":         ``,
		"missing customer":   `{"consent_active":true}`,
		"missing consent":    `{"customer_id":"c1"}`,
		"customer too long":  `{"customer_id":"` + strings.Repeat("x", 129) + `","consent_active":true}`,
		"wrong consent type": `{"customer_id":"c1","consent_active":"yes"}`,
	}
	for name, body := range bodies {
		t.Run(name, func(t *testing.T) {
			f := &fakeResolver{}
			rec := post(t, f, body)
			assert.Equal(t, http.StatusBadRequest, rec.Code)
			assert.Equal(t, "invalid_request", decode(t, rec)["error"])
			assert.Empty(t, f.gotCustomer, "an invalid request must not reach the use case")
		})
	}
}

func TestResolveAbortedRequest(t *testing.T) {
	rec := post(t, &fakeResolver{err: errors.New("context canceled")}, `{"customer_id":"c1","consent_active":true}`)

	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	assert.Equal(t, "request_aborted", decode(t, rec)["error"])
}

// --- HA-L01: partial profile, per-source state and cacheable read -------------

func get(t *testing.T, r *fakeResolver, target string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	httpadapter.NewRouter(r).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
	return rec
}

func partialResolution() domain.ProfileResolution {
	return domain.ProfileResolution{
		Profile: domain.RiskProfile{Score: 0.4, Category: "standard", CalculatedAt: time.Date(2026, 10, 10, 9, 0, 0, 0, time.UTC), Partial: true},
		Mode:    domain.ModeNormal,
		Source:  domain.SourceExternal,
		Sources: []domain.SourceOutcome{
			{Source: domain.ExternalSourceOpenFinance, Status: domain.SourceStatusOK, Latency: 63 * time.Millisecond},
			{Source: domain.ExternalSourceOpenData, Status: domain.SourceStatusTimeout, Latency: 120*time.Millisecond + 400*time.Microsecond},
			{Source: domain.ExternalSourceKYC, Status: domain.SourceStatusCircuitOpen},
		},
	}
}

func TestResolveResponseHasPartialAndSources(t *testing.T) {
	rec := post(t, &fakeResolver{res: partialResolution()}, `{"customer_id":"c1","consent_active":true}`)

	require.Equal(t, http.StatusOK, rec.Code)
	m := decode(t, rec)
	assert.Equal(t, "normal", m["mode"])
	assert.Equal(t, "external_sources", m["source"])
	assert.Equal(t, true, m["partial"])
	assert.Equal(t, []any{
		map[string]any{"name": "open_finance", "status": "ok", "latency_ms": 63.0},
		map[string]any{"name": "open_data", "status": "timeout", "latency_ms": 120.0},
		map[string]any{"name": "kyc", "status": "circuit_open", "latency_ms": 0.0},
	}, m["sources"])
}

func TestSingleSourceResponseHasEmptySourcesAndIsNotPartial(t *testing.T) {
	f := &fakeResolver{res: domain.ProfileResolution{
		Profile: domain.RiskProfile{Score: 0.3, Category: "standard"},
		Mode:    domain.ModeNormal, Source: domain.SourceOpenFinance,
	}}

	m := decode(t, post(t, f, `{"customer_id":"c1","consent_active":true}`))

	assert.Equal(t, false, m["partial"])
	assert.Equal(t, []any{}, m["sources"], "sources is always an array, never null")
}

func TestProvisionalResponseIsNotPartialButKeepsTheSources(t *testing.T) {
	res := partialResolution()
	res.Mode = domain.ModeProvisional
	res.Source = domain.SourceCache
	res.UnavailableSource = domain.ExternalSourceOpenFinance

	m := decode(t, post(t, &fakeResolver{res: res}, `{"customer_id":"c1","consent_active":true}`))

	assert.Equal(t, false, m["partial"], "a provisional answer is flagged by mode and source")
	assert.Len(t, m["sources"], 3)
	assert.Equal(t, httpadapter.NoticeLastProfileInUse, m["notice"])
}

func TestGetRiskProfileMatchesPost(t *testing.T) {
	f := &fakeResolver{res: partialResolution()}
	posted := post(t, f, `{"customer_id":"c 1","consent_active":true}`)

	g := &fakeResolver{res: partialResolution()}
	got := get(t, g, "/v1/risk-profiles/c%201?consent_active=true")

	require.Equal(t, http.StatusOK, got.Code)
	assert.JSONEq(t, posted.Body.String(), got.Body.String())
	assert.Equal(t, "c 1", g.gotCustomer, "the path parameter is decoded")
	assert.True(t, g.gotConsent)
}

func TestGetRiskProfileDecodesEscapedSlashes(t *testing.T) {
	g := &fakeResolver{res: partialResolution()}

	rec := get(t, g, "/v1/risk-profiles/a%2Fb?consent_active=true")

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "a/b", g.gotCustomer)
}

func TestGetRiskProfileConsentRequired(t *testing.T) {
	g := &fakeResolver{err: profiling.ErrConsentRequired}

	rec := get(t, g, "/v1/risk-profiles/c1?consent_active=false")

	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	assert.Equal(t, "consent_required", decode(t, rec)["error"])
	assert.False(t, g.gotConsent)
}

func TestGetRiskProfileInvalidRequests(t *testing.T) {
	badEscaping := httptest.NewRequest(http.MethodGet, "/v1/risk-profiles/x?consent_active=true", nil)
	badEscaping.URL.RawPath = "/v1/risk-profiles/%zz" // chi routes on the raw path
	requests := map[string]*http.Request{
		"missing consent":   httptest.NewRequest(http.MethodGet, "/v1/risk-profiles/c1", nil),
		"invalid consent":   httptest.NewRequest(http.MethodGet, "/v1/risk-profiles/c1?consent_active=yes", nil),
		"customer too long": httptest.NewRequest(http.MethodGet, "/v1/risk-profiles/"+strings.Repeat("x", 129)+"?consent_active=true", nil),
		"bad escaping":      badEscaping,
	}
	for name, req := range requests {
		t.Run(name, func(t *testing.T) {
			f := &fakeResolver{}
			rec := httptest.NewRecorder()
			httpadapter.NewRouter(f).ServeHTTP(rec, req)
			assert.Equal(t, http.StatusBadRequest, rec.Code)
			assert.Equal(t, "invalid_request", decode(t, rec)["error"])
			assert.Empty(t, f.gotCustomer, "an invalid request must not reach the use case")
		})
	}
}

func TestGetRiskProfileAbortedRequest(t *testing.T) {
	rec := get(t, &fakeResolver{err: errors.New("context canceled")}, "/v1/risk-profiles/c1?consent_active=true")

	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	assert.Equal(t, "request_aborted", decode(t, rec)["error"])
}
