package httpadapter_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	httpadapter "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/partners/internal/adapters/http"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/partners/internal/domain"
)

// HU-W3.2-03: the technical performance of the partner API, for the partner.

var perfNow = time.Date(2026, 9, 17, 3, 30, 0, 0, time.UTC)

type fakePerformance struct {
	perf   domain.Performance
	err    error
	period domain.Period
	calls  int
}

func (f *fakePerformance) Performance(_ context.Context, p domain.Period) (domain.Performance, error) {
	f.calls++
	f.period = p
	return f.perf, f.err
}

func callPerformance(t *testing.T, svc *fakePerformance, query, token string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/v1/partner-performance"+query, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	httpadapter.NewRouter(httpadapter.WithPerformance(svc, verifier, func() time.Time { return perfNow })).ServeHTTP(rec, req)
	return rec
}

func degraded() domain.Performance {
	p95 := 1150.0
	ended := perfNow.Add(-8*24*time.Hour + 14*time.Minute)
	return domain.Performance{
		Period: domain.Period7d, From: time.Date(2026, 9, 10, 5, 0, 0, 0, time.UTC), To: perfNow,
		Status: domain.StatusDegraded, Traffic: domain.Traffic{Requests: 10_000, ServerErrors: 4, P95Ms: &p95},
		ObjectivePercent: 99.9, BudgetMs: 400,
		Incidents: []domain.Incident{
			{ID: "slo-latency-post-offers-1", Kind: domain.IncidentDegraded, Route: "POST /v1/offers", StartedAt: perfNow.Add(-42 * time.Minute)},
			{ID: "slo-errors-post-offers-1", Kind: domain.IncidentUnavailable, Route: "POST /v1/offers",
				StartedAt: perfNow.Add(-8 * 24 * time.Hour), EndedAt: &ended},
		},
	}
}

func TestThePerformanceIsOnlyForThePartnerPortal(t *testing.T) {
	svc := &fakePerformance{perf: degraded()}

	assert.Equal(t, http.StatusUnauthorized, callPerformance(t, svc, "", "").Code)
	for _, token := range []string{"ops", "advisor", "insured"} {
		assert.Equal(t, http.StatusForbidden, callPerformance(t, svc, "", token).Code, token)
	}
	assert.Zero(t, svc.calls)
}

func TestThePerformanceOfThePeriod(t *testing.T) {
	svc := &fakePerformance{perf: degraded()}

	rec := callPerformance(t, svc, "?period=7d", "partner")

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
	assert.Equal(t, domain.Period7d, svc.period)
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, "7d", body["period"])
	assert.Equal(t, "degraded", body["status"])
	assert.Equal(t, map[string]any{"percent": 99.96, "objective_percent": 99.9, "requests": 10000.0, "server_errors": 4.0}, body["availability"])
	assert.Equal(t, map[string]any{"p95_ms": 1150.0, "budget_ms": 400.0}, body["latency"])
	incidents := body["incidents"].([]any)
	require.Len(t, incidents, 2)
	open := incidents[0].(map[string]any)
	assert.Equal(t, "degraded", open["kind"])
	assert.Equal(t, "POST /v1/offers", open["route"])
	assert.Nil(t, open["ended_at"])
	assert.Equal(t, false, open["resolved"])
	assert.Equal(t, 2520.0, open["duration_seconds"], "an open incident lasts until now")
	closed := incidents[1].(map[string]any)
	assert.Equal(t, true, closed["resolved"])
	assert.Equal(t, 840.0, closed["duration_seconds"])
}

func TestWithoutIncidentsTheListIsEmptyAndWithoutRequestsTheP95IsNull(t *testing.T) {
	svc := &fakePerformance{perf: domain.Performance{Period: domain.PeriodToday, Status: domain.StatusOperational,
		ObjectivePercent: 99.9, BudgetMs: 400}}

	rec := callPerformance(t, svc, "?period=today", "partner")

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `"incidents":[]`)
	assert.Contains(t, rec.Body.String(), `"p95_ms":null`)
	assert.Contains(t, rec.Body.String(), `"percent":100`)
}

func TestTheDefaultPeriodIsTheLastSevenDays(t *testing.T) {
	svc := &fakePerformance{perf: degraded()}

	require.Equal(t, http.StatusOK, callPerformance(t, svc, "", "partner").Code)
	assert.Equal(t, domain.Period7d, svc.period)
}

func TestAnUnknownPeriodIsRefused(t *testing.T) {
	svc := &fakePerformance{}

	rec := callPerformance(t, svc, "?period=90d", "partner")

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), `"error":"invalid_request"`)
	assert.Zero(t, svc.calls)
}

func TestWithoutTelemetryThePerformanceIsUnavailable(t *testing.T) {
	for _, err := range []error{fmt.Errorf("%w: no alarm", domain.ErrTelemetryUnavailable), fmt.Errorf("boom")} {
		rec := callPerformance(t, &fakePerformance{err: err}, "", "partner")

		assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
		assert.Contains(t, rec.Body.String(), `"error":"telemetry_unavailable"`)
	}
}
