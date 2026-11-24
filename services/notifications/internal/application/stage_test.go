package application_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/notifications/internal/domain"
)

// HU-M3.3-01: recording the notification of a change of stage.

func stageChange() domain.ClaimStageChange {
	return domain.ClaimStageChange{ChangeID: 42, ClaimID: "cl-1", FilingNumber: "SIN-2026-01655", Subject: "sub-a",
		Stage: domain.StageInEvaluation, ChangedAt: now.Add(-5 * 1e9)}
}

func TestAChangeOfStageIsRecordedAndWakesTheWorker(t *testing.T) {
	store := &fakeStore{created: true, deliveries: []string{"ntf-stage-42:inst-1"}}
	woken := 0
	svc := newService(t, store, func() { woken++ })

	require.NoError(t, svc.RecordStageChange(context.Background(), stageChange()))

	require.Len(t, store.recorded, 1)
	assert.Equal(t, "ntf-stage-42", store.recorded[0].ID)
	assert.Equal(t, domain.KindClaimStageChanged, store.recorded[0].Kind)
	assert.Equal(t, 1, woken)
}

func TestAChangeWithoutDevicesWaitsInTheCenterAndARedeliveryChangesNothing(t *testing.T) {
	woken := 0
	require.NoError(t, newService(t, &fakeStore{created: true}, func() { woken++ }).
		RecordStageChange(context.Background(), stageChange()))
	require.NoError(t, newService(t, &fakeStore{created: false, deliveries: []string{"x"}}, func() { woken++ }).
		RecordStageChange(context.Background(), stageChange()))
	assert.Zero(t, woken)
}

func TestAnInvalidChangeOrAStoreFailureIsAnError(t *testing.T) {
	bad := stageChange()
	bad.Stage = "rejected"
	require.ErrorIs(t, newService(t, &fakeStore{}, nil).RecordStageChange(context.Background(), bad),
		domain.ErrInvalidStageChange)

	boom := errors.New("database down")
	require.ErrorIs(t, newService(t, &fakeStore{err: boom}, nil).RecordStageChange(context.Background(), stageChange()), boom)
}
