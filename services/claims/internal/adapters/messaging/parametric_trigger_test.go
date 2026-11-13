package messaging_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/events"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/adapters/messaging"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/parametric"
)

// HU-M2.4-01: the consumer of ParametricTriggerObserved.

type fakeObserver struct {
	got []domain.Observation
	err error
}

func (f *fakeObserver) Observe(_ context.Context, o domain.Observation) (parametric.Result, error) {
	f.got = append(f.got, o)
	return parametric.Result{}, f.err
}

var occurredAt = time.Date(2026, 9, 11, 21, 39, 30, 0, time.UTC)

func envelope(t *testing.T, payload map[string]any) events.Envelope {
	t.Helper()
	e, err := events.New("parametric-trigger-observed-obs-1", messaging.EventTypeParametricTriggerObserved, "iot", 1,
		occurredAt, payload)
	require.NoError(t, err)
	return e
}

// The flight of the prototype, as nube.sh publishes it.
func payload() map[string]any {
	return map[string]any{
		"observation_id": "obs-1", "subject": "sub-a", "policy_id": "VJ-2026-000931", "parameter": "flight_delay",
		"event_reference": "AV-8421", "observed": 215, "unit": "minutes", "observed_at": "2026-09-11T21:39:00Z",
	}
}

func TestTheEventBecomesTheObservation(t *testing.T) {
	obs := &fakeObserver{}
	require.NoError(t, messaging.NewParametricTriggerHandler(obs).Handle(context.Background(), envelope(t, payload())))

	require.Len(t, obs.got, 1)
	assert.Equal(t, domain.Observation{ID: "obs-1", Subject: "sub-a", PolicyID: "VJ-2026-000931",
		Parameter: domain.IncidentFlightDelay, EventReference: "AV-8421", Observed: 215, Unit: "minutes",
		ObservedAt: occurredAt.Add(-30 * time.Second)}, obs.got[0])
}

func TestWithoutIDOrInstantTheEventGivesThem(t *testing.T) {
	p := payload()
	delete(p, "observation_id")
	delete(p, "observed_at")
	obs := &fakeObserver{}
	require.NoError(t, messaging.NewParametricTriggerHandler(obs).Handle(context.Background(), envelope(t, p)))
	assert.Equal(t, "parametric-trigger-observed-obs-1", obs.got[0].ID)
	assert.Equal(t, occurredAt, obs.got[0].ObservedAt)
}

func TestAnEventWithoutObservedValueIsAnError(t *testing.T) {
	p := payload()
	delete(p, "observed")
	obs := &fakeObserver{}
	err := messaging.NewParametricTriggerHandler(obs).Handle(context.Background(), envelope(t, p))
	assert.ErrorIs(t, err, domain.ErrInvalidObservation)
	assert.Empty(t, obs.got)
}

func TestAPayloadThatIsNotAnObservationIsAnError(t *testing.T) {
	err := messaging.NewParametricTriggerHandler(&fakeObserver{}).Handle(context.Background(),
		envelope(t, map[string]any{"observed": "many"}))
	require.Error(t, err)
}

func TestAFailureOfTheEngineIsRetriedByTheQueue(t *testing.T) {
	err := messaging.NewParametricTriggerHandler(&fakeObserver{err: errors.New("core down")}).
		Handle(context.Background(), envelope(t, payload()))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parametric-trigger-observed-obs-1")
}
