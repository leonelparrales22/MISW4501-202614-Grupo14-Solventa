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

// HU-M3.3-01: the consumer of ClaimStageChanged.

type fakeStageRecorder struct {
	got []domain.ClaimStageChange
	err error
}

func (f *fakeStageRecorder) RecordStageChange(_ context.Context, c domain.ClaimStageChange) error {
	f.got = append(f.got, c)
	return f.err
}

func stageEnvelope(t *testing.T, payload map[string]any) events.Envelope {
	t.Helper()
	e, err := events.New("claim-stage-changed-42", messaging.EventTypeClaimStageChanged, "claims", 1, occurredAt, payload)
	require.NoError(t, err)
	return e
}

func TestTheEventBecomesTheChangeOfStage(t *testing.T) {
	rec := &fakeStageRecorder{}
	payload := map[string]any{"change_id": 42, "claim_id": "cl-1", "filing_number": "SIN-2026-01655",
		"policy_id": "HG-2025-880231", "subject": "sub-a", "stage": "paid", "changed_at": "2026-09-20T14:40:05Z",
		"amount_minor": 413_250_000, "currency": "COP", "paid_at": "2026-09-20T14:40:00Z"}

	require.NoError(t, messaging.NewClaimStageChangedHandler(rec).Handle(context.Background(), stageEnvelope(t, payload)))

	require.Len(t, rec.got, 1)
	c := rec.got[0]
	amount := int64(413_250_000)
	paid := time.Date(2026, 9, 20, 14, 40, 0, 0, time.UTC)
	assert.Equal(t, domain.ClaimStageChange{ChangeID: 42, ClaimID: "cl-1", FilingNumber: "SIN-2026-01655",
		PolicyID: "HG-2025-880231", Subject: "sub-a", Stage: "paid",
		ChangedAt: time.Date(2026, 9, 20, 14, 40, 5, 0, time.UTC), AmountMinor: &amount, Currency: "COP", PaidAt: &paid}, c)
}

func TestWithoutChangedAtTheChangeIsWhenTheEventOccurred(t *testing.T) {
	rec := &fakeStageRecorder{}
	payload := map[string]any{"change_id": 42, "claim_id": "cl-1", "filing_number": "SIN-2026-01655",
		"subject": "sub-a", "stage": "in_evaluation"}

	require.NoError(t, messaging.NewClaimStageChangedHandler(rec).Handle(context.Background(), stageEnvelope(t, payload)))
	assert.Equal(t, occurredAt, rec.got[0].ChangedAt)
	assert.Nil(t, rec.got[0].AmountMinor)
}

func TestAFailureToRecordTheChangeIsRetried(t *testing.T) {
	err := messaging.NewClaimStageChangedHandler(&fakeStageRecorder{err: errors.New("database down")}).
		Handle(context.Background(), stageEnvelope(t, map[string]any{"change_id": 42}))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "claim-stage-changed-42")
}

func TestAPayloadThatIsNotAChangeIsAnError(t *testing.T) {
	err := messaging.NewClaimStageChangedHandler(&fakeStageRecorder{}).
		Handle(context.Background(), stageEnvelope(t, map[string]any{"change_id": "many"}))
	require.Error(t, err)
}
