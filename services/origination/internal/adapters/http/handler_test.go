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
