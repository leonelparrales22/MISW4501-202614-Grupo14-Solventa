package main

import (
	"log"

	corepolicies "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/adapters/policies"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/resilience"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/adapters/pdf"
	policiesadapter "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/adapters/policies"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/evaluation"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/filing"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/ports"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/triage"
)

// evaluationService builds what operations reads to evaluate a claim
// (HU-W2.2-03). The claims store is also the queue of the claims in
// evaluation. Without the policy core (terms nil) the detail is still served
// and says the terms of the policy are unavailable; without the object store
// the evidence and the supports come without links.
func evaluationService(records ports.ClaimRecords, priorities *triage.Service, objects ports.ObjectStore,
	terms ports.PolicyTermsReader) *evaluation.Service {
	queue, ok := records.(ports.ClaimQueue)
	if !ok {
		log.Fatalf("the evaluation has no queue for the claims of %T", records)
	}
	signer := filing.NewQueryService(records, pdf.NewReceiptRenderer()).WithObjects(objects)
	return evaluation.NewService(records, queue, priorities, signer, terms, objects)
}

// policyTerms reads the terms of the policies and the insured from the policy
// core, or is nil when the core is not configured.
func policyTerms(getenv func(string) string) ports.PolicyTermsReader {
	cfg, err := loadPolicyCoreConfig(getenv)
	switch {
	case err != nil:
		log.Fatalf("invalid configuration: %v", err)
	case cfg.url == "":
		log.Printf("%s is not set: the evaluation of a claim (HU-W2.2-03) shows no terms of the policy", envPolicyCoreURL)
		return nil
	}
	return policiesadapter.NewTerms(corepolicies.NewClient(corepolicies.Config{
		BaseURL: cfg.url, Timeout: cfg.timeout, Breaker: resilience.DefaultConfig(),
	}))
}
