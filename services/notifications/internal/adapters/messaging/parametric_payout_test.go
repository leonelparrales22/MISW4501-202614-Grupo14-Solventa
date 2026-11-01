package messaging_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/events"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/notifications/internal/adapters/messaging"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/notifications/internal/domain"
)

type fakeRecorder struct {
	got []domain.ParametricPayout
	err error
}

func (f *fakeRecorder) RecordPayout(_ context.Context, p domain.ParametricPayout) error {
	f.got = append(f.got, p)
	return f.err
}

var occurredAt = time.Date(2026, 9, 11, 21, 40, 0, 0, time.UTC)

// The payload of docs/events/README.md for the payment of the prototype.
func envelope(t *testing.T, payload map[string]any) events.Envelope {
	t.Helper()
	e, err := events.New("parametric-payout-completed-ppo-1", messaging.EventTypeParametricPayoutCompleted, "claims", 1,
		occurredAt, payload)
	require.NoError(t, err)
	return e
}

func prototypePayload() map[string]any {
	return map[string]any{
		"payout_id": "ppo-1", "claim_id": "SIN-2026-01702", "policy_id": "VJ-2026-000931", "subject": "sub-a",
		"amount_minor": 18_000_000, "currency": "COP",
		"trigger": map[string]any{"parameter": "flight_delay", "event_reference": "AV-8421", "threshold": 180,
			"observed": 215, "unit": "minutes", "observed_at": "2026-09-11T21:39:00Z"},
		"payment_method":  map[string]any{"brand": "visa", "last4": "1111"},
		"idempotency_key": "pay-ppo-1", "paid_at": "2026-09-11T21:40:00Z",
	}
}

func TestTheEventBecomesThePayoutOfTheNotification(t *testing.T) {
	rec := &fakeRecorder{}

	require.NoError(t, messaging.NewParametricPayoutHandler(rec).Handle(context.Background(), envelope(t, prototypePayload())))

	require.Len(t, rec.got, 1)
	p := rec.got[0]
	assert.Equal(t, "ppo-1", p.PayoutID)
	assert.Equal(t, "SIN-2026-01702", p.ClaimID)
	assert.Equal(t, "VJ-2026-000931", p.PolicyID)
	assert.Equal(t, "sub-a", p.Subject)
	assert.Equal(t, int64(18_000_000), p.AmountMinor)
	assert.Equal(t, domain.Trigger{Parameter: "flight_delay", EventReference: "AV-8421", Threshold: 180, Observed: 215,
		Unit: "minutes", ObservedAt: occurredAt.Add(-time.Minute)}, p.Trigger)
	assert.Equal(t, domain.PaymentMethod{Brand: "visa", Last4: "1111"}, p.Method)
	assert.Equal(t, "pay-ppo-1", p.IdempotencyKey)
	assert.Equal(t, occurredAt, p.PaidAt)
}

func TestWithoutPaidAtThePaymentIsWhenTheEventOccurred(t *testing.T) {
	rec := &fakeRecorder{}
	payload := prototypePayload()
	delete(payload, "paid_at")

	require.NoError(t, messaging.NewParametricPayoutHandler(rec).Handle(context.Background(), envelope(t, payload)))

	assert.Equal(t, occurredAt, rec.got[0].PaidAt)
}

func TestARecordingFailureIsRetriedByTheQueue(t *testing.T) {
	err := messaging.NewParametricPayoutHandler(&fakeRecorder{err: errors.New("database down")}).
		Handle(context.Background(), envelope(t, prototypePayload()))

	require.Error(t, err)
	assert.Contains(t, err.Error(), "parametric-payout-completed-ppo-1")
}

func TestAPayloadThatIsNotThePayoutIsAnError(t *testing.T) {
	e := envelope(t, map[string]any{"amount_minor": "many"})

	require.Error(t, messaging.NewParametricPayoutHandler(&fakeRecorder{}).Handle(context.Background(), e))
}
