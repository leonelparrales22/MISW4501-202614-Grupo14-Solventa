package domain

import (
	"errors"
	"math"
	"sort"
	"time"
)

// HU-W3.2-03: technical performance of the partner API on the dashboard of
// the partner. The SLO of the integration is the one of the platform: latency
// p95 within the budget of HA-L01 (400 ms) and availability >= 99.9 %
// (RC-02), measured in CloudWatch. Each partner route has two SLO alarms: one
// on its p95 latency (an incident that degrades the API) and one on its share
// of 5XX answers (an incident that stops it).

// Period is the range the partner looks at: today, the last 7 days or the
// last 30 days, in civil days of Colombia up to now.
type Period string

// The periods of the dashboard.
const (
	PeriodToday Period = "today"
	Period7d    Period = "7d"
	Period30d   Period = "30d"
)

// DefaultPeriod is the one of the dashboard when the partner chooses none.
const DefaultPeriod = Period7d

// ErrUnknownPeriod: the period is none of today, 7d and 30d.
var ErrUnknownPeriod = errors.New("the period must be today, 7d or 30d")

// ErrTelemetryUnavailable: CloudWatch did not answer, or there are no SLO
// alarms to read the performance from.
var ErrTelemetryUnavailable = errors.New("the telemetry of the partner API is not available")

// colombia is the time zone of the civil days of the periods (UTC-5, no DST).
var colombia = time.FixedZone("UTC-5", -5*60*60)

// ParsePeriod reads a period; empty is DefaultPeriod.
func ParsePeriod(raw string) (Period, error) {
	switch p := Period(raw); p {
	case "":
		return DefaultPeriod, nil
	case PeriodToday, Period7d, Period30d:
		return p, nil
	default:
		return "", ErrUnknownPeriod
	}
}

// Days is how many civil days the period covers, today included.
func (p Period) Days() int {
	switch p {
	case Period7d:
		return 7
	case Period30d:
		return 30
	default:
		return 1
	}
}

// Window is the period as instants: from 00:00 in Colombia of its first day
// to now.
func (p Period) Window(now time.Time) (start, end time.Time) {
	y, m, d := now.In(colombia).Date()
	return time.Date(y, m, d-(p.Days()-1), 0, 0, 0, 0, colombia), now
}

// APIStatus is the current state of the partner API (CA-1).
type APIStatus string

// The states of the API.
const (
	// StatusOperational: no SLO alarm is active.
	StatusOperational APIStatus = "operational"
	// StatusDegraded: a latency alarm is active (the SLO is affected).
	StatusDegraded APIStatus = "degraded"
	// StatusUnavailable: an errors alarm is active (the API cannot serve).
	StatusUnavailable APIStatus = "unavailable"
)

// IncidentKind says what an incident did to the API.
type IncidentKind string

// The kinds of incident, one per kind of SLO alarm.
const (
	IncidentDegraded    IncidentKind = "degraded"
	IncidentUnavailable IncidentKind = "unavailable"
)

// SLOAlarm is one SLO alarm of a partner route and its current state.
type SLOAlarm struct {
	Name string
	Kind IncidentKind
	// Route is the method and resource of the route ("POST /v1/offers").
	Route string
	// Dimensions are the ones of the metric the alarm watches (API, stage,
	// resource and method): the metrics of the dashboard are read with them.
	Dimensions map[string]string
	// Threshold is the one of the alarm: the latency budget in ms for a
	// degraded alarm, the share of 5XX answers for an unavailable one.
	Threshold float64
	// Active is true while the alarm is in ALARM, since Since.
	Active bool
	Since  time.Time
}

// AlarmTransition is one change of state of an SLO alarm: Active is true when
// it entered ALARM and false when it left it.
type AlarmTransition struct {
	Alarm  string
	Active bool
	At     time.Time
}

// Incident is one period in which an SLO alarm was active (CA-3).
type Incident struct {
	ID        string
	Kind      IncidentKind
	Route     string
	StartedAt time.Time
	// EndedAt is nil while the incident is open.
	EndedAt *time.Time
}

// Resolved is true when the incident ended.
func (i Incident) Resolved() bool { return i.EndedAt != nil }

// Duration is how long the incident lasted, or has lasted until now.
func (i Incident) Duration(now time.Time) time.Duration {
	if i.EndedAt != nil {
		return i.EndedAt.Sub(i.StartedAt)
	}
	return now.Sub(i.StartedAt)
}

// CurrentStatus is the state of the API from its SLO alarms: unavailable when
// an errors alarm is active, degraded when only a latency alarm is, and
// operational when none is (CA-1).
func CurrentStatus(alarms []SLOAlarm) APIStatus {
	status := StatusOperational
	for _, a := range alarms {
		if !a.Active {
			continue
		}
		if a.Kind == IncidentUnavailable {
			return StatusUnavailable
		}
		status = StatusDegraded
	}
	return status
}

// Incidents pairs the transitions of each alarm into incidents and keeps the
// ones that overlap [start, end), the most recent first. A transition to ALARM
// opens an incident and the next one to OK closes it; an alarm that is still
// active keeps its incident open, from Since when its opening is older than
// the history. An OK whose opening is older than the history is dropped: its
// start is unknown.
func Incidents(alarms []SLOAlarm, transitions []AlarmTransition, start, end time.Time) []Incident {
	byAlarm := map[string][]AlarmTransition{}
	for _, t := range transitions {
		byAlarm[t.Alarm] = append(byAlarm[t.Alarm], t)
	}
	var out []Incident
	for _, a := range alarms {
		ts := byAlarm[a.Name]
		sort.SliceStable(ts, func(i, j int) bool { return ts[i].At.Before(ts[j].At) })
		var open *time.Time
		for _, t := range ts {
			switch {
			case t.Active && open == nil:
				at := t.At
				open = &at
			case !t.Active && open != nil:
				ended := t.At
				out = append(out, newIncident(a, *open, &ended))
				open = nil
			}
		}
		if a.Active {
			since := a.Since
			if open != nil {
				since = *open
			}
			out = append(out, newIncident(a, since, nil))
		}
	}
	kept := out[:0]
	for _, i := range out {
		if i.StartedAt.Before(end) && (i.EndedAt == nil || i.EndedAt.After(start)) {
			kept = append(kept, i)
		}
	}
	sort.SliceStable(kept, func(i, j int) bool { return kept[i].StartedAt.After(kept[j].StartedAt) })
	return kept
}

func newIncident(a SLOAlarm, started time.Time, ended *time.Time) Incident {
	return Incident{
		ID: a.Name + "-" + started.UTC().Format("20060102T150405Z"), Kind: a.Kind, Route: a.Route,
		StartedAt: started, EndedAt: ended,
	}
}

// Traffic is what the partner routes served in a window.
type Traffic struct {
	Requests     int64
	ServerErrors int64
	// P95Ms is the p95 latency of the slowest partner route with requests in
	// the window, the same statistic its latency alarm watches; nil without
	// requests.
	P95Ms *float64
}

// AvailabilityPercent is the share of requests answered without a 5XX, with
// two decimals rounded down so a fraction of an error never shows as 100 %.
// Without requests the API was not unavailable to anyone: 100 % (RC-02 counts
// what fails, and a 4XX is the partner's request, not a failure of the API).
func (t Traffic) AvailabilityPercent() float64 {
	if t.Requests <= 0 {
		return 100
	}
	ok := float64(t.Requests-t.ServerErrors) / float64(t.Requests) * 100
	return math.Floor(ok*100) / 100
}

// Performance is the technical performance of the partner API in a period.
type Performance struct {
	Period Period
	From   time.Time
	To     time.Time
	Status APIStatus
	Traffic
	// ObjectivePercent is the availability objective of RC-02.
	ObjectivePercent float64
	// BudgetMs is the p95 latency budget, the threshold of the latency alarms.
	BudgetMs float64
	// Incidents are the ones of the period, the most recent first.
	Incidents []Incident
}
