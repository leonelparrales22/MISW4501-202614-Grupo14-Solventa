package main

import (
	"context"
	"fmt"
	"log"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/adapters/email"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/adapters/payments"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/resilience"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/security"
	httpadapter "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/adapters/http"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/adapters/mail"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/adapters/memory"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/adapters/payouts"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/adapters/postgres"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/decision"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/ports"
)

// Variables of the decision of a claim (HU-W2.2-03). The payment gateway is
// what mounts it: without it an approval could not be paid. Without the
// e-mail provider the claims are still decided, and the notices of rejection
// wait until it is configured.
const (
	envPaymentGatewayURL = "PAYMENT_GATEWAY_URL"
	envEmailURL          = "EMAIL_URL"
)

// decisionStoreOf keeps the decisions where the claims are: in the same
// database, or in memory beside the claims of the memory store.
func decisionStoreOf(records ports.ClaimRecords) (ports.DecisionStore, error) {
	switch r := records.(type) {
	case *postgres.ClaimRecordStore:
		return postgres.DecisionStoreOf(r), nil
	case *memory.CaseStore:
		return memory.NewDecisionStore(r), nil
	}
	return nil, fmt.Errorf("the decision has no store for the claims of %T", records)
}

// decisionRoutes mounts the decision of a claim and starts the worker that
// pays the approvals and notifies the rejections, which runs until ctx ends.
func decisionRoutes(ctx context.Context, records ports.ClaimRecords, store ports.DecisionStore,
	terms ports.PolicyTermsReader, verifier security.Verifier, getenv func(string) string) []httpadapter.Option {
	gatewayURL := getenv(envPaymentGatewayURL)
	if gatewayURL == "" {
		log.Printf("%s is not set: the decision of a claim (HU-W2.2-03) is not mounted", envPaymentGatewayURL)
		return nil
	}
	gateway := payouts.NewGateway(payments.NewClient(payments.Config{BaseURL: gatewayURL, Breaker: resilience.DefaultConfig()}))
	var mailer ports.Mailer
	if url := getenv(envEmailURL); url != "" {
		mailer = mail.NewMailer(email.NewClient(email.Config{BaseURL: url, Breaker: resilience.DefaultConfig()}))
	} else {
		log.Printf("%s is not set: the notices of rejection (HU-W2.2-03) wait until it is", envEmailURL)
	}
	worker, err := decision.NewWorker(store, gateway, mailer, systemClock{}, decision.WorkerConfig{})
	if err != nil {
		log.Fatalf("invalid configuration: %v", err)
	}
	go worker.Run(ctx)
	log.Printf("decision (HU-W2.2-03): payment gateway at %s, %d automatic attempts per payout",
		gatewayURL, domain.MaxAutomaticPayoutAttempts)
	return []httpadapter.Option{httpadapter.WithDecisions(
		decision.NewService(records, store, terms, systemClock{}, worker.Notify), verifier)}
}
