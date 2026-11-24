package dashboard_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/partners/internal/dashboard"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/partners/internal/domain"
)

// HU-W3.2-03: the technical performance of the partner API.

var now = time.Date(2026, 9, 17, 3, 30, 0, 0, time.UTC)

type clock struct{}

func (clock) Now() time.Time { return now }

type fakeTelemetry struct {
	alarms     []domain.SLOAlarm
	history    []domain.AlarmTransition
	traffic    domain.Traffic
	alarmsErr  error
	historyErr error
	trafficErr error
	since      time.Time
	start, end time.Time
}

func (f *fakeTelemetry) Alarms(context.Context) ([]domain.SLOAlarm, error) {
	return f.alarms, f.alarmsErr
}

func (f *fakeTelemetry) History(_ context.Context, _ []domain.SLOAlarm, since time.Time) ([]domain.AlarmTransition, error) {
	f.since = since
	return f.history, f.historyErr
}

func (f *fakeTelemetry) Traffic(_ context.Context, _ []domain.SLOAlarm, start, end time.Time) (domain.Traffic, error) {
	f.start, f.end = start, end
	return f.traffic, f.trafficErr
}

func sloAlarms(latencyActive bool) []domain.SLOAlarm {
	return []domain.SLOAlarm{
		{Name: "slo-latency-post-offers", Kind: domain.IncidentDegraded, Route: "POST /v1/offers", Threshold: 400,
			Active: latencyActive, Since: now.Add(-42 * time.Minute)},
		{Name: "slo-errors-post-offers", Kind: domain.IncidentUnavailable, Route: "POST /v1/offers", Threshold: 0.05},
	}
}

func TestThePerformanceOfThePeriod(t *testing.T) {
	p95 := 1150.0
	tel := &fakeTelemetry{alarms: sloAlarms(true), traffic: domain.Traffic{Requests: 10_000, ServerErrors: 4, P95Ms: &p95}}
	svc, err := dashboard.NewService(tel, clock{}, 0)
	require.NoError(t, err)

	p, err := svc.Performance(context.Background(), domain.Period7d)

	require.NoError(t, err)
	assert.Equal(t, domain.StatusDegraded, p.Status, "CA-1: an active latency incident degrades the API")
	assert.Equal(t, 99.96, p.AvailabilityPercent())
	assert.Equal(t, 1150.0, *p.P95Ms)
	assert.Equal(t, 99.9, p.ObjectivePercent, "RC-02")
	assert.Equal(t, 400.0, p.BudgetMs, "CA-5: the budget is the threshold of the latency alarm")
	require.Len(t, p.Incidents, 1)
	assert.False(t, p.Incidents[0].Resolved())
	assert.Equal(t, time.Date(2026, 9, 10, 5, 0, 0, 0, time.UTC), tel.start.UTC())
	assert.Equal(t, now, tel.end)
	assert.Equal(t, now.Add(-dashboard.HistoryRetention), tel.since, "the whole history CloudWatch keeps")
}

func TestWithoutSLOAlarmsThereIsNothingToMeasure(t *testing.T) {
	svc, err := dashboard.NewService(&fakeTelemetry{}, clock{}, 99.5)
	require.NoError(t, err)

	_, err = svc.Performance(context.Background(), domain.PeriodToday)

	assert.ErrorIs(t, err, domain.ErrTelemetryUnavailable)
}

func TestAFailureOfTheTelemetryIsReturned(t *testing.T) {
	down := fmt.Errorf("%w: boom", domain.ErrTelemetryUnavailable)
	for name, tel := range map[string]*fakeTelemetry{
		"alarms":  {alarmsErr: down},
		"history": {alarms: sloAlarms(false), historyErr: down},
		"traffic": {alarms: sloAlarms(false), trafficErr: down},
	} {
		svc, err := dashboard.NewService(tel, clock{}, 0)
		require.NoError(t, err)
		_, err = svc.Performance(context.Background(), domain.Period30d)
		assert.ErrorIs(t, err, domain.ErrTelemetryUnavailable, name)
	}
}

func TestTheServiceNeedsItsParts(t *testing.T) {
	_, err := dashboard.NewService(nil, clock{}, 0)
	assert.Error(t, err)
	_, err = dashboard.NewService(&fakeTelemetry{}, nil, 0)
	assert.Error(t, err)
	_, err = dashboard.NewService(&fakeTelemetry{}, clock{}, 120)
	assert.Error(t, err)
	assert.False(t, errors.Is(err, domain.ErrTelemetryUnavailable))
}
