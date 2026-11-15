package domain_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/partners/internal/domain"
)

// HU-W3.2-03: the rules of the technical performance of the partner API.

// perfNow is 16 September 2026, 22:30 in Colombia: in UTC it is already the 17th.
var perfNow = time.Date(2026, 9, 17, 3, 30, 0, 0, time.UTC)

func at(h, m int) time.Time { return time.Date(2026, 9, 16, h+5, m, 0, 0, time.UTC) } // h:m of the 16th in Colombia

func TestThePeriods(t *testing.T) {
	p, err := domain.ParsePeriod("")
	require.NoError(t, err)
	assert.Equal(t, domain.Period7d, p, "the dashboard opens with the last 7 days")
	for _, raw := range []string{"today", "7d", "30d"} {
		_, err := domain.ParsePeriod(raw)
		assert.NoError(t, err, raw)
	}
	_, err = domain.ParsePeriod("90d")
	assert.ErrorIs(t, err, domain.ErrUnknownPeriod)
}

func TestTheWindowStartsAtMidnightOfColombia(t *testing.T) {
	for p, first := range map[domain.Period]time.Time{
		domain.PeriodToday: time.Date(2026, 9, 16, 5, 0, 0, 0, time.UTC),
		domain.Period7d:    time.Date(2026, 9, 10, 5, 0, 0, 0, time.UTC),
		domain.Period30d:   time.Date(2026, 8, 18, 5, 0, 0, 0, time.UTC),
	} {
		start, end := p.Window(perfNow)
		assert.Equal(t, first, start.UTC(), p)
		assert.Equal(t, perfNow, end, p)
	}
}

func alarm(name string, kind domain.IncidentKind, active bool, since time.Time) domain.SLOAlarm {
	return domain.SLOAlarm{Name: name, Kind: kind, Route: "POST /v1/offers", Active: active, Since: since}
}

func TestTheStatusComesFromTheActiveAlarms(t *testing.T) {
	latency := alarm("slo-latency-post-offers", domain.IncidentDegraded, false, at(1, 0))
	errs := alarm("slo-errors-post-offers", domain.IncidentUnavailable, false, at(1, 0))
	assert.Equal(t, domain.StatusOperational, domain.CurrentStatus([]domain.SLOAlarm{latency, errs}), "no active incident")

	latency.Active = true
	assert.Equal(t, domain.StatusDegraded, domain.CurrentStatus([]domain.SLOAlarm{latency, errs}), "an incident that affects the SLO")

	errs.Active = true
	assert.Equal(t, domain.StatusUnavailable, domain.CurrentStatus([]domain.SLOAlarm{latency, errs}), "an incident that stops the API wins")
	assert.Equal(t, domain.StatusOperational, domain.CurrentStatus(nil))
}

func TestTheTransitionsBecomeIncidents(t *testing.T) {
	latency := alarm("slo-latency-post-offers", domain.IncidentDegraded, true, at(16, 20))
	errs := alarm("slo-errors-post-offers", domain.IncidentUnavailable, false, at(3, 26))
	history := []domain.AlarmTransition{
		{Alarm: errs.Name, Active: false, At: at(3, 26)},
		{Alarm: errs.Name, Active: true, At: at(3, 12)},
		{Alarm: latency.Name, Active: true, At: at(16, 20)},
	}
	start, end := domain.PeriodToday.Window(perfNow)

	got := domain.Incidents([]domain.SLOAlarm{latency, errs}, history, start, end)

	require.Len(t, got, 2)
	assert.Equal(t, domain.IncidentDegraded, got[0].Kind, "the most recent first")
	assert.Equal(t, at(16, 20), got[0].StartedAt)
	assert.False(t, got[0].Resolved())
	assert.Equal(t, 6*time.Hour+10*time.Minute, got[0].Duration(perfNow), "an open incident lasts until now")
	assert.Equal(t, domain.IncidentUnavailable, got[1].Kind)
	assert.True(t, got[1].Resolved())
	assert.Equal(t, 14*time.Minute, got[1].Duration(perfNow))
	assert.Equal(t, "POST /v1/offers", got[1].Route)
	assert.NotEqual(t, got[0].ID, got[1].ID)
}

func TestAnActiveAlarmOlderThanTheHistoryIsStillOpen(t *testing.T) {
	latency := alarm("slo-latency-post-offers", domain.IncidentDegraded, true, at(2, 0))
	start, end := domain.PeriodToday.Window(perfNow)

	got := domain.Incidents([]domain.SLOAlarm{latency}, nil, start, end)

	require.Len(t, got, 1)
	assert.Equal(t, at(2, 0), got[0].StartedAt)
	assert.False(t, got[0].Resolved())
}

func TestOnlyTheIncidentsThatOverlapThePeriod(t *testing.T) {
	errs := alarm("slo-errors-post-offers", domain.IncidentUnavailable, false, at(0, 0))
	yesterday := func(h, m int) time.Time { return at(h, m).Add(-24 * time.Hour) }
	history := []domain.AlarmTransition{
		{Alarm: errs.Name, Active: true, At: yesterday(10, 0)}, // ended yesterday: outside today
		{Alarm: errs.Name, Active: false, At: yesterday(10, 5)},
		{Alarm: errs.Name, Active: true, At: yesterday(23, 50)}, // crosses midnight: inside today
		{Alarm: errs.Name, Active: false, At: at(0, 10)},
		{Alarm: errs.Name, Active: false, At: at(1, 0)}, // an OK without its ALARM is not an incident
	}
	start, end := domain.PeriodToday.Window(perfNow)

	got := domain.Incidents([]domain.SLOAlarm{errs}, history, start, end)

	require.Len(t, got, 1)
	assert.Equal(t, yesterday(23, 50), got[0].StartedAt)
	assert.Empty(t, domain.Incidents([]domain.SLOAlarm{errs}, history[:2], start, end), "CA-4: a period without incidents")
}

func TestTheAvailabilityIsTheShareOfAnswersWithoutA5XX(t *testing.T) {
	assert.Equal(t, 99.96, domain.Traffic{Requests: 10_000, ServerErrors: 4}.AvailabilityPercent())
	assert.Equal(t, 99.99, domain.Traffic{Requests: 100_000, ServerErrors: 1}.AvailabilityPercent(), "rounded down, never 100 with an error")
	assert.Equal(t, 100.0, domain.Traffic{Requests: 500}.AvailabilityPercent())
	assert.Equal(t, 100.0, domain.Traffic{}.AvailabilityPercent(), "without requests nobody found the API down")
	assert.Equal(t, 50.0, domain.Traffic{Requests: 2, ServerErrors: 1}.AvailabilityPercent())
}
