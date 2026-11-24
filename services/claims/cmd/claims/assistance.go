package main

import (
	"fmt"
	"log"

	corepolicies "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/adapters/policies"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/resilience"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/security"
	httpadapter "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/adapters/http"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/adapters/memory"
	policiesadapter "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/adapters/policies"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/adapters/postgres"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/assistance"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/ports"
)

// assistanceStoreOf keeps the requests for assistance (HU-M2.3-01) where the
// claims are: in the same database, or in memory when there is none.
func assistanceStoreOf(records ports.ClaimRecords) (ports.AssistanceStore, error) {
	switch r := records.(type) {
	case *postgres.ClaimRecordStore:
		return postgres.AssistanceStoreOf(r), nil
	case *memory.CaseStore:
		return memory.NewAssistanceStore(), nil
	}
	return nil, fmt.Errorf("the assistance has no store for the claims of %T", records)
}

// assistanceRoutes mounts the request for on-site assistance (HU-M2.3-01)
// when the policy core is configured. Without the core nothing is mounted:
// nobody could tell which policies are the insured's, and the routes are not
// left open without that check (decision D2, assumption S12).
func assistanceRoutes(records ports.ClaimRecords, getenv func(string) string,
	verifier security.Verifier) ([]httpadapter.Option, error) {
	store, err := assistanceStoreOf(records)
	if err != nil {
		return nil, err
	}
	return assistanceRoutesOver(store, getenv, verifier)
}

// assistanceRoutesOver is assistanceRoutes over a store the caller already
// has: the inbox of operations (HU-W2.3-03) reads the same requests, which in
// memory means the same instance.
func assistanceRoutesOver(store ports.AssistanceStore, getenv func(string) string,
	verifier security.Verifier) ([]httpadapter.Option, error) {
	cfg, err := loadPolicyCoreConfig(getenv)
	if err != nil {
		return nil, err
	}
	if cfg.url == "" {
		log.Printf("%s is not set: the request for assistance (HU-M2.3-01) is not mounted", envPolicyCoreURL)
		return nil, nil
	}
	core := corepolicies.NewClient(corepolicies.Config{
		BaseURL: cfg.url, Timeout: cfg.timeout, Breaker: resilience.DefaultConfig(),
	})
	log.Printf("request for assistance (HU-M2.3-01): policy core at %s, timeout %s", cfg.url, cfg.timeout)
	return []httpadapter.Option{httpadapter.WithAssistance(
		assistance.NewService(store, policiesadapter.NewDirectory(core), systemClock{}), verifier)}, nil
}
