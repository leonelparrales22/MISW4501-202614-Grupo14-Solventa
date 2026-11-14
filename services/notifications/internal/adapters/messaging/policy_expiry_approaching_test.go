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

// HU-M3.2-01: the consumer of PolicyExpiryApproaching.

type fakeExpiryRecorder struct {
	got []domain.ExpiryAlert
	err error
}

func (f *fakeExpiryRecorder) RecordExpiryAlert(_ context.Context, e domain.ExpiryAlert) error {
	f.got = append(f.got, e)
	return f.err
}

func expiryEnvelope(t *testing.T, payload map[string]any) events.Envelope {
	t.Helper()
	e, err := events.New("policy-expiry-approaching-AU-2025-118204-2026-11-29-30",
		messaging.EventTypePolicyExpiryApproaching, "origination", 1, occurredAt, payload)
	require.NoError(t, err)
	return e
}

func TestTheEventBecomesTheExpiryAlert(t *testing.T) {
	rec := &fakeExpiryRecorder{}
	payload := map[string]any{"policy_id": "AU-2025-118204", "subject": "sub-a", "source": "policy_core", "line": "auto",
		"product": "Autos", "insured_object": "KJT-482", "coverage_end": "2026-11-30T00:00:00Z", "milestone_days": 30,
		"days_left": 15, "detected_at": "2026-11-14T15:00:00Z"}

	require.NoError(t, messaging.NewPolicyExpiryApproachingHandler(rec).Handle(context.Background(), expiryEnvelope(t, payload)))

	require.Len(t, rec.got, 1)
	assert.Equal(t, domain.ExpiryAlert{PolicyID: "AU-2025-118204", Subject: "sub-a", Source: "policy_core", Line: "auto",
		Product: "Autos", InsuredObject: "KJT-482", CoverageEnd: time.Date(2026, 11, 30, 0, 0, 0, 0, time.UTC),
		MilestoneDays: 30, DaysLeft: 15, DetectedAt: time.Date(2026, 11, 14, 15, 0, 0, 0, time.UTC)}, rec.got[0])
}

func TestANullProductAndNoDetectionTimeAreTolerated(t *testing.T) {
	rec := &fakeExpiryRecorder{}
	payload := map[string]any{"policy_id": "pol-acc-1", "subject": "sub-a", "source": "origination", "line": "life",
		"product": nil, "insured_object": nil, "coverage_end": "2026-11-19T04:59:59Z", "milestone_days": 5, "days_left": 4}

	require.NoError(t, messaging.NewPolicyExpiryApproachingHandler(rec).Handle(context.Background(), expiryEnvelope(t, payload)))

	assert.Empty(t, rec.got[0].Product)
	assert.Empty(t, rec.got[0].InsuredObject)
	assert.Equal(t, occurredAt, rec.got[0].DetectedAt)
}

func TestAnExpiryAlertThatCannotBeRecordedOrDecodedIsRetried(t *testing.T) {
	rec := &fakeExpiryRecorder{err: errors.New("database down")}
	err := messaging.NewPolicyExpiryApproachingHandler(rec).Handle(context.Background(),
		expiryEnvelope(t, map[string]any{"policy_id": "x"}))
	assert.Error(t, err)

	bad, err := events.New("e-1", messaging.EventTypePolicyExpiryApproaching, "origination", 1, occurredAt,
		map[string]any{"milestone_days": "thirty"})
	require.NoError(t, err)
	assert.Error(t, messaging.NewPolicyExpiryApproachingHandler(&fakeExpiryRecorder{}).Handle(context.Background(), bad))
}
