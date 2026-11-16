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

// HU-W1.6-02: the consumer of PolicyCancelledForNonPayment.

type cancellations struct {
	got []domain.PolicyCancellation
	err error
}

func (c *cancellations) RecordCancellation(_ context.Context, p domain.PolicyCancellation) error {
	c.got = append(c.got, p)
	return c.err
}

func cancelledEvent(t *testing.T, payload map[string]any) events.Envelope {
	t.Helper()
	e, err := events.New("policy-cancelled-non-payment-VH-2026-004512", messaging.EventTypePolicyCancelledForNonPayment,
		"origination", 1, time.Date(2026, 9, 12, 5, 0, 0, 0, time.UTC), payload)
	require.NoError(t, err)
	return e
}

func TestTheCancellationOfOriginationBecomesANotification(t *testing.T) {
	rec := &cancellations{}
	e := cancelledEvent(t, map[string]any{
		"policy_id": "VH-2026-004512", "subject": "sub-a", "reason": "non_payment", "line": "life",
		"installment_seq": 2, "due_at": "2026-08-12T05:00:00Z", "grace_ends_at": "2026-09-11T05:00:00Z",
		"cancelled_at": "2026-09-12T05:00:00Z", "amount_minor": 14230000, "currency": "COP", "refund": "not_applicable",
	})

	require.NoError(t, messaging.NewPolicyCancelledHandler(rec).Handle(context.Background(), e))

	require.Len(t, rec.got, 1)
	c := rec.got[0]
	assert.Equal(t, "VH-2026-004512", c.PolicyID)
	assert.Equal(t, 2, c.InstallmentSeq)
	assert.Equal(t, int64(14_230_000), c.AmountMinor)
	assert.Equal(t, time.Date(2026, 9, 12, 5, 0, 0, 0, time.UTC), c.CancelledAt)
}

func TestACancellationWithoutItsDateTakesTheOneOfTheEvent(t *testing.T) {
	rec := &cancellations{}
	e := cancelledEvent(t, map[string]any{"policy_id": "VH-1", "subject": "sub-a", "reason": "non_payment",
		"installment_seq": 2, "amount_minor": 1, "currency": "COP"})

	require.NoError(t, messaging.NewPolicyCancelledHandler(rec).Handle(context.Background(), e))

	assert.Equal(t, e.OccurredAt, rec.got[0].CancelledAt)
}

func TestAFailureToRecordTheCancellationIsRetried(t *testing.T) {
	boom := errors.New("database down")
	e := cancelledEvent(t, map[string]any{"policy_id": "VH-1"})

	err := messaging.NewPolicyCancelledHandler(&cancellations{err: boom}).Handle(context.Background(), e)

	assert.ErrorIs(t, err, boom)
}
