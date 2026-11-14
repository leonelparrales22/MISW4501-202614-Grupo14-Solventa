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

// HU-M3.2-01: recording the notification that a policy is about to expire.

func expiryAlert() domain.ExpiryAlert {
	return domain.ExpiryAlert{PolicyID: "AU-2025-118204", Subject: "sub-a", Source: domain.ExpirySourcePolicyCore,
		Line: "auto", CoverageEnd: now.AddDate(0, 0, 16), MilestoneDays: 30, DaysLeft: 16, DetectedAt: now.Add(-5 * time.Second)}
}

func TestAnExpiryAlertIsRecordedAndWakesTheWorker(t *testing.T) {
	store := &fakeStore{created: true, deliveries: []string{"d-1"}}
	woken := 0

	require.NoError(t, newService(t, store, func() { woken++ }).RecordExpiryAlert(context.Background(), expiryAlert()))

	require.Len(t, store.recorded, 1)
	assert.Equal(t, domain.KindPolicyExpiry, store.recorded[0].Kind)
	assert.Equal(t, 1, woken)
}

func TestAnExpiryAlertWithoutDevicesWaitsAndARedeliveryChangesNothing(t *testing.T) {
	woken := 0
	require.NoError(t, newService(t, &fakeStore{created: true}, func() { woken++ }).
		RecordExpiryAlert(context.Background(), expiryAlert()))
	require.NoError(t, newService(t, &fakeStore{created: false, deliveries: []string{"x"}}, func() { woken++ }).
		RecordExpiryAlert(context.Background(), expiryAlert()))
	assert.Zero(t, woken)
}

func TestAnInvalidExpiryAlertOrAStoreFailureIsAnError(t *testing.T) {
	bad := expiryAlert()
	bad.MilestoneDays = 10
	require.ErrorIs(t, newService(t, &fakeStore{}, nil).RecordExpiryAlert(context.Background(), bad),
		domain.ErrInvalidExpiryAlert)

	boom := errors.New("database down")
	require.ErrorIs(t, newService(t, &fakeStore{err: boom}, nil).RecordExpiryAlert(context.Background(), expiryAlert()), boom)
}
