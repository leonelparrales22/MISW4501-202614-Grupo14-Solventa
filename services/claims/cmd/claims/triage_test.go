package main

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/adapters/memory"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/adapters/postgres"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/ports"
)

// The composition of the triage (HU-W2.2-01): its interval and its store.

func TestTheSweepRunsEveryTenSecondsByDefault(t *testing.T) {
	interval, err := loadTriageConfig(env(nil))

	require.NoError(t, err)
	assert.Equal(t, 10*time.Second, interval)
}

func TestTheIntervalOfTheSweepAtItsLimits(t *testing.T) {
	for raw, want := range map[string]time.Duration{"1": time.Second, "30": 30 * time.Second, "2": 2 * time.Second} {
		interval, err := loadTriageConfig(env(map[string]string{"TRIAGE_SWEEP_INTERVAL_SECONDS": raw}))
		require.NoError(t, err, raw)
		assert.Equal(t, want, interval, raw)
	}
	for _, raw := range []string{"0", "31", "-1", "abc", "1.5", "10s"} {
		_, err := loadTriageConfig(env(map[string]string{"TRIAGE_SWEEP_INTERVAL_SECONDS": raw}))
		assert.Error(t, err, raw)
	}
}

// fakeRecords is a store of claims the triage does not know.
type fakeRecords struct{ ports.ClaimRecords }

func TestTheCalculationsLiveWhereTheClaimsLive(t *testing.T) {
	store, err := triageStoreOf(postgres.NewClaimRecordStore(nil))
	require.NoError(t, err)
	assert.IsType(t, &postgres.TriageStore{}, store)

	store, err = triageStoreOf(memory.NewCaseStore())
	require.NoError(t, err)
	assert.IsType(t, &memory.TriageStore{}, store)

	_, err = triageStoreOf(fakeRecords{})
	assert.Error(t, err)
}

func TestTheTriageStartsItsSweep(t *testing.T) {
	cases := memory.NewCaseStore()
	_, _, err := cases.Create(context.Background(), domain.Claim{ID: "cl-1", Subject: "sub-carolina", FilingKey: "k",
		IncidentType: domain.IncidentTheft, Status: domain.StatusActive, FiledAt: time.Now().UTC(), Details: map[string]string{}})
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	svc, interval, err := startTriage(ctx, cases, env(map[string]string{"TRIAGE_SWEEP_INTERVAL_SECONDS": "1"}))

	require.NoError(t, err)
	assert.Equal(t, time.Second, interval)
	require.Eventually(t, func() bool {
		got, err := svc.Summaries(context.Background(), []string{"cl-1"})
		return err == nil && got[0].State == domain.TriageAssessed
	}, 5*time.Second, 20*time.Millisecond)

	_, _, err = startTriage(ctx, cases, env(map[string]string{"TRIAGE_SWEEP_INTERVAL_SECONDS": "0"}))
	assert.Error(t, err)
	_, _, err = startTriage(ctx, fakeRecords{}, env(nil))
	assert.Error(t, err)
}
