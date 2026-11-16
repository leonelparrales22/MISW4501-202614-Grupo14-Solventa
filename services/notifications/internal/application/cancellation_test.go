package application_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/notifications/internal/domain"
)

// HU-W1.6-02: recording the notification that a policy was cancelled for
// non-payment.

func cancellation() domain.PolicyCancellation {
	due := now.AddDate(0, 0, -31)
	return domain.PolicyCancellation{PolicyID: "VH-2026-004512", Subject: "sub-a", Reason: domain.ReasonNonPayment,
		Line: "life", InstallmentSeq: 2, DueAt: due, GraceEndsAt: due.AddDate(0, 0, 30), CancelledAt: now.Add(-5 * time.Second),
		AmountMinor: 14_230_000, Currency: "COP"}
}

func TestACancellationIsRecordedAndWakesTheWorker(t *testing.T) {
	store := &fakeStore{created: true, deliveries: []string{"d-1"}}
	woken := 0

	require.NoError(t, newService(t, store, func() { woken++ }).RecordCancellation(context.Background(), cancellation()))

	require.Len(t, store.recorded, 1)
	assert.Equal(t, domain.KindPolicyCancelled, store.recorded[0].Kind)
	assert.Equal(t, 1, woken)
}

func TestARedeliveredCancellationChangesNothing(t *testing.T) {
	woken := 0
	require.NoError(t, newService(t, &fakeStore{created: true}, func() { woken++ }).
		RecordCancellation(context.Background(), cancellation()))
	require.NoError(t, newService(t, &fakeStore{created: false, deliveries: []string{"x"}}, func() { woken++ }).
		RecordCancellation(context.Background(), cancellation()))
	assert.Zero(t, woken, "without devices it waits in the center; a redelivery wakes nothing")
}

func TestAnInvalidCancellationOrAStoreFailureIsAnError(t *testing.T) {
	bad := cancellation()
	bad.Reason = "requested"
	require.ErrorIs(t, newService(t, &fakeStore{}, nil).RecordCancellation(context.Background(), bad),
		domain.ErrInvalidCancellation)

	boom := errors.New("database down")
	require.ErrorIs(t, newService(t, &fakeStore{err: boom}, nil).RecordCancellation(context.Background(), cancellation()), boom)
}
