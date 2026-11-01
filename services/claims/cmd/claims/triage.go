package main

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/adapters/memory"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/adapters/postgres"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/ports"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/triage"
)

// envTriageInterval is how often the triage sweeps the claims in evaluation
// (HU-W2.2-01, decision D8). Optional: only a local run or an end-to-end test
// shortens it.
const envTriageInterval = "TRIAGE_SWEEP_INTERVAL_SECONDS"

// Bounds of the interval: a claim waits at most one interval for its level,
// and criterion 1 allows 60 seconds.
const (
	minTriageIntervalSeconds = 1
	maxTriageIntervalSeconds = 30
)

// loadTriageConfig reads the interval of the sweep: 10 seconds unless
// TRIAGE_SWEEP_INTERVAL_SECONDS says otherwise, between 1 and 30.
func loadTriageConfig(getenv func(string) string) (time.Duration, error) {
	raw := getenv(envTriageInterval)
	if raw == "" {
		return triage.DefaultInterval, nil
	}
	seconds, err := strconv.Atoi(raw)
	if err != nil || seconds < minTriageIntervalSeconds || seconds > maxTriageIntervalSeconds {
		return 0, fmt.Errorf("%s must be a whole number of seconds between %d and %d, got %q",
			envTriageInterval, minTriageIntervalSeconds, maxTriageIntervalSeconds, raw)
	}
	return time.Duration(seconds) * time.Second, nil
}

// triageStoreOf keeps the calculations where the claims are: in the same
// database, or in memory beside the claims of the memory store.
func triageStoreOf(records ports.ClaimRecords) (ports.TriageStore, error) {
	switch r := records.(type) {
	case *postgres.ClaimRecordStore:
		return postgres.TriageStoreOf(r), nil
	case *memory.CaseStore:
		return memory.NewTriageStore(r), nil
	}
	return nil, fmt.Errorf("the triage has no store for the claims of %T", records)
}

// startTriage builds the triage over the claims and starts its sweep, which
// runs until ctx ends.
func startTriage(ctx context.Context, records ports.ClaimRecords, getenv func(string) string) (*triage.Service, time.Duration, error) {
	interval, err := loadTriageConfig(getenv)
	if err != nil {
		return nil, 0, err
	}
	store, err := triageStoreOf(records)
	if err != nil {
		return nil, 0, err
	}
	svc := triage.NewService(store, systemClock{})
	go triage.NewWorker(svc, interval, triage.DefaultBatch).Run(ctx)
	return svc, interval, nil
}
