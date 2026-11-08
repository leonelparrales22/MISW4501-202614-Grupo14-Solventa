package main

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"time"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/adapters/email"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/resilience"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/security"
	httpadapter "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/adapters/http"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/adapters/mail"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/adapters/memory"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/adapters/postgres"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/assessors"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/ports"
)

// envAssignmentRetryInterval is how often the claims pending assignment are
// searched again (HU-W2.3-01, assumption S8). Optional: only a local run or
// an end-to-end test shortens it.
const envAssignmentRetryInterval = "ASSIGNMENT_RETRY_INTERVAL_SECONDS"

// Bounds of the interval of the automatic search.
const (
	minAssignmentRetrySeconds = 10
	maxAssignmentRetrySeconds = 3600
)

// loadAssignmentConfig reads the interval of the automatic search: 300
// seconds unless ASSIGNMENT_RETRY_INTERVAL_SECONDS says otherwise, between 10
// and 3600.
func loadAssignmentConfig(getenv func(string) string) (time.Duration, error) {
	raw := getenv(envAssignmentRetryInterval)
	if raw == "" {
		return assessors.DefaultRetryInterval, nil
	}
	seconds, err := strconv.Atoi(raw)
	if err != nil || seconds < minAssignmentRetrySeconds || seconds > maxAssignmentRetrySeconds {
		return 0, fmt.Errorf("%s must be a whole number of seconds between %d and %d, got %q",
			envAssignmentRetryInterval, minAssignmentRetrySeconds, maxAssignmentRetrySeconds, raw)
	}
	return time.Duration(seconds) * time.Second, nil
}

// assignmentStoresOf keeps the assignments and the registry of providers
// where the claims are: in the same database, or in memory beside the claims
// of the memory store, with the reference providers of 0009_provider_seed.
func assignmentStoresOf(records ports.ClaimRecords) (ports.AssignmentStore, ports.ProviderStore, error) {
	switch r := records.(type) {
	case *postgres.ClaimRecordStore:
		return postgres.AssignmentStoreOf(r), postgres.ProviderStoreOf(r), nil
	case *memory.CaseStore:
		providers := memory.NewProviderStore(memory.ReferenceProviders()...)
		return memory.NewAssignmentStore(r, providers), providers, nil
	}
	return nil, nil, fmt.Errorf("the assignment has no store for the claims of %T", records)
}

// assignmentRoutes mounts the assignment of the provider and the registry of
// providers (HU-W2.3-01) and starts the worker that searches again the claims
// pending assignment and sends the notices to the providers, which runs until
// ctx ends. Without the e-mail provider the claims are still assigned, and
// the notices wait until it is configured.
func assignmentRoutes(ctx context.Context, records ports.ClaimRecords, getenv func(string) string,
	verifier security.Verifier) ([]httpadapter.Option, error) {
	interval, err := loadAssignmentConfig(getenv)
	if err != nil {
		return nil, err
	}
	store, providers, err := assignmentStoresOf(records)
	if err != nil {
		return nil, err
	}
	var mailer ports.Mailer
	if url := getenv(envEmailURL); url != "" {
		mailer = mail.NewMailer(email.NewClient(email.Config{BaseURL: url, Breaker: resilience.DefaultConfig()}))
	} else {
		log.Printf("%s is not set: the notices to the assigned providers (HU-W2.3-01) wait until it is", envEmailURL)
	}
	var worker *assessors.Worker
	svc := assessors.NewService(store, providers, systemClock{}, func() { worker.Notify() })
	worker = assessors.NewWorker(svc, store, mailer, systemClock{}, assessors.WorkerConfig{RetryInterval: interval})
	go worker.Run(ctx)
	log.Printf("assignment (HU-W2.3-01): policy %s, claims pending assignment searched again every %s",
		svc.Policy().Version, interval)
	return []httpadapter.Option{
		httpadapter.WithAssignment(svc, interval, verifier),
		httpadapter.WithProviders(assessors.NewProviderService(providers, systemClock{}), verifier),
	}, nil
}
