// Package messaging holds the consumers of the events the claims service
// receives from its SQS queue (docs/events/README.md). They are idempotent:
// the queue delivers at least once and without order.
package messaging

import (
	"context"
	"fmt"
	"time"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/events"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/parametric"
)

// EventTypeParametricTriggerObserved is the external event the parametric
// engine consumes (HU-M2.4-01): the telemetry observed the parameter of a
// parametric policy (the delay of the flight of an insured).
const EventTypeParametricTriggerObserved = "ParametricTriggerObserved"

// Observer resolves an observation (parametric.Service).
type Observer interface {
	Observe(ctx context.Context, o domain.Observation) (parametric.Result, error)
}

// parametricTriggerPayload is the payload of ParametricTriggerObserved v1.
type parametricTriggerPayload struct {
	ObservationID  string    `json:"observation_id"`
	Subject        string    `json:"subject"`
	PolicyID       string    `json:"policy_id"`
	Parameter      string    `json:"parameter"`
	EventReference string    `json:"event_reference"`
	Observed       *float64  `json:"observed"`
	Unit           string    `json:"unit"`
	ObservedAt     time.Time `json:"observed_at"`
}

// NewParametricTriggerHandler handles ParametricTriggerObserved. The
// observation id defaults to the event id, so the same event delivered again
// resolves nothing new. A payload that cannot be decoded or validated, or a
// policy core that does not answer, is an error: the message is retried and
// ends in the dead-letter queue of the claims queue.
func NewParametricTriggerHandler(svc Observer) events.Handler {
	return events.HandlerFunc(func(ctx context.Context, e events.Envelope) error {
		var p parametricTriggerPayload
		if err := e.DecodePayload(&p); err != nil {
			return err
		}
		if p.Observed == nil {
			return fmt.Errorf("%w: event %s has no observed value", domain.ErrInvalidObservation, e.EventID)
		}
		o := domain.Observation{
			ID: p.ObservationID, Subject: p.Subject, PolicyID: p.PolicyID, Parameter: domain.IncidentType(p.Parameter),
			EventReference: p.EventReference, Observed: *p.Observed, Unit: p.Unit, ObservedAt: p.ObservedAt.UTC(),
		}
		if o.ID == "" {
			o.ID = e.EventID
		}
		if o.ObservedAt.IsZero() {
			o.ObservedAt = e.OccurredAt
		}
		if _, err := svc.Observe(ctx, o); err != nil {
			return fmt.Errorf("event %s: %w", e.EventID, err)
		}
		return nil
	})
}
