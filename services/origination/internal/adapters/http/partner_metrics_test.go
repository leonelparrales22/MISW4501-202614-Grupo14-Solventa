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
	httpadapter "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/http"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/partnermetrics"
)

// HU-W3.2-01: the summary of the dashboard of a commercial partner.

// partnerVerifier reads "<role>:<subject>@<partner_id>" (the partner is
// optional), so a test can give a token the custom:partner_id claim.
type partnerVerifier struct{}

func (partnerVerifier) Verify(_ context.Context, raw string) (security.Claims, error) {
	role, rest, ok := strings.Cut(raw, ":")
	if !ok {
		return security.Claims{}, errors.New("bad token")
	}
	subject, partner, _ := strings.Cut(rest, "@")
	return security.Claims{Subject: subject, Roles: []security.Role{security.Role(role)}, PartnerID: partner}, nil
}

type fakeActivity struct {
	activity   domain.PartnerActivity
	err        error
	gotPartner string
	gotStart   time.Time
	gotEnd     time.Time
	calls      int
}

func (f *fakeActivity) CountPartnerActivity(_ context.Context, partnerID string, start, end time.Time) (domain.PartnerActivity, error) {
	f.calls++
	f.gotPartner, f.gotStart, f.gotEnd = partnerID, start, end
	return f.activity, f.err
}

func callSummary(t *testing.T, store *fakeActivity, path, token string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	httpadapter.NewRouter(&fakeResolver{}, httpadapter.WithObservability("origination"),
		httpadapter.WithPartnerMetrics(partnermetrics.NewService(store), partnerVerifier{})).ServeHTTP(rec, req)
	return rec
}

const summaryPath = "/v1/partner-metrics/summary?from=2026-08-18&to=2026-09-16"

func TestTheSummaryIsOnlyForThePartnerPortal(t *testing.T) {
	store := &fakeActivity{}

	assert.Equal(t, http.StatusUnauthorized, callSummary(t, store, summaryPath, "").Code)
	assert.Equal(t, http.StatusForbidden, callSummary(t, store, summaryPath, "operations:ops@socio-a").Code)
	assert.Equal(t, http.StatusForbidden, callSummary(t, store, summaryPath, "advisor:laura").Code)
	assert.Equal(t, http.StatusForbidden, callSummary(t, store, summaryPath, "partner:ana").Code,
		"a partner user without its partner claim sees nothing")
	assert.Zero(t, store.calls)
}

func TestTheSummaryCountsThePartnerOfTheTokenOnly(t *testing.T) {
	store := &fakeActivity{activity: domain.PartnerActivity{QuotesRequested: 12_480, PoliciesIssued: 1_872}}

	rec := callSummary(t, store, summaryPath+"&partner_id=socio-b", "partner:ana@socio-a")

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
	assert.Equal(t, "socio-a", store.gotPartner, "a partner_id in the query is ignored (CA-4)")
	assert.Equal(t, time.Date(2026, 8, 18, 5, 0, 0, 0, time.UTC), store.gotStart.UTC(), "from 00:00 in Colombia")
	assert.Equal(t, time.Date(2026, 9, 17, 5, 0, 0, 0, time.UTC), store.gotEnd.UTC(), "the last day counts whole (CA-2)")
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, map[string]any{"partner_id": "socio-a", "from": "2026-08-18", "to": "2026-09-16",
		"quotes_requested": float64(12_480), "policies_issued": float64(1_872), "conversion_rate": 15.0}, body)
}

func TestARangeWithoutActivityIsAllZeros(t *testing.T) {
	rec := callSummary(t, &fakeActivity{}, "/v1/partner-metrics/summary?from=2026-09-16&to=2026-09-16", "partner:ana@socio-a")

	require.Equal(t, http.StatusOK, rec.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, float64(0), body["quotes_requested"])
	assert.Equal(t, float64(0), body["policies_issued"])
	assert.Equal(t, float64(0), body["conversion_rate"], "0 % without quotes (CA-1, CA-3)")
}

func TestTheSummaryRefusesARangeItCannotRead(t *testing.T) {
	for name, tc := range map[string]struct {
		query string
		code  string
	}{
		"no from":        {"?to=2026-09-16", "invalid_request"},
		"no to":          {"?from=2026-09-16", "invalid_request"},
		"not a date":     {"?from=16/09/2026&to=2026-09-16", "invalid_request"},
		"inverted range": {"?from=2026-09-17&to=2026-09-16", "out_of_range"},
		"over a year":    {"?from=2025-01-01&to=2026-09-16", "range_too_long"},
	} {
		t.Run(name, func(t *testing.T) {
			store := &fakeActivity{}
			rec := callSummary(t, store, "/v1/partner-metrics/summary"+tc.query, "partner:ana@socio-a")

			assert.Equal(t, http.StatusBadRequest, rec.Code)
			assert.Contains(t, rec.Body.String(), `"error":"`+tc.code+`"`)
			assert.Zero(t, store.calls)
		})
	}
}

func TestTheSummaryIsUnavailableWhenTheDatabaseFails(t *testing.T) {
	rec := callSummary(t, &fakeActivity{err: errors.New("database down")}, summaryPath, "partner:ana@socio-a")

	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	assert.Contains(t, rec.Body.String(), `"error":"metrics_unavailable"`)
}
