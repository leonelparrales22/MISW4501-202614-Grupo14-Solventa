// Package dashboard is the technical performance of the partner API on the
// dashboard of the partner (HU-W3.2-03): the current state of the API, its
// availability and its p95 latency in the period, and the incidents of the
// period. Everything comes from the SLO alarms of the partner API in
// CloudWatch and from the metrics they watch, so what the partner sees is
// measured as the SLO is (CA-5).
package dashboard

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/partners/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/partners/internal/ports"
)

// DefaultObjectivePercent is the availability objective of RC-02.
const DefaultObjectivePercent = 99.9

// HistoryRetention is how far back CloudWatch keeps the history of an alarm:
// enough for the 30 days of the longest period.
const HistoryRetention = 30 * 24 * time.Hour

// Service reads the performance.
type Service struct {
	telemetry ports.Telemetry
	clock     ports.Clock
	objective float64
}

// NewService builds the service; objective is the availability objective in
// percent (DefaultObjectivePercent when zero).
func NewService(telemetry ports.Telemetry, clock ports.Clock, objective float64) (*Service, error) {
	if telemetry == nil || clock == nil {
		return nil, errors.New("dashboard: the telemetry and the clock are required")
	}
	if objective == 0 {
		objective = DefaultObjectivePercent
	}
	if objective <= 0 || objective > 100 {
		return nil, fmt.Errorf("dashboard: the availability objective must be in (0, 100], got %v", objective)
	}
	return &Service{telemetry: telemetry, clock: clock, objective: objective}, nil
}

// Performance returns the performance of the partner API in the period. With
// no SLO alarm there is nothing to measure against: ErrTelemetryUnavailable.
func (s *Service) Performance(ctx context.Context, period domain.Period) (domain.Performance, error) {
	now := s.clock.Now()
	start, end := period.Window(now)
	alarms, err := s.telemetry.Alarms(ctx)
	if err != nil {
		return domain.Performance{}, err
	}
	if len(alarms) == 0 {
		return domain.Performance{}, fmt.Errorf("%w: no SLO alarm", domain.ErrTelemetryUnavailable)
	}
	history, err := s.telemetry.History(ctx, alarms, now.Add(-HistoryRetention))
	if err != nil {
		return domain.Performance{}, err
	}
	traffic, err := s.telemetry.Traffic(ctx, alarms, start, end)
	if err != nil {
		return domain.Performance{}, err
	}
	return domain.Performance{
		Period: period, From: start, To: end, Status: domain.CurrentStatus(alarms), Traffic: traffic,
		ObjectivePercent: s.objective, BudgetMs: budgetOf(alarms),
		Incidents: domain.Incidents(alarms, history, start, end),
	}, nil
}

// budgetOf is the latency budget of the SLO: the threshold of the latency
// alarms (the strictest when they differ).
func budgetOf(alarms []domain.SLOAlarm) float64 {
	budget := 0.0
	for _, a := range alarms {
		if a.Kind == domain.IncidentDegraded && (budget == 0 || a.Threshold < budget) {
			budget = a.Threshold
		}
	}
	return budget
}
