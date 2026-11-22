package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/adapters/payments"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/resilience"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/security"
	httpadapter "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/http"
	paymentsadapter "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/payments"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/postgres"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/billing"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/cancellation"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
)

// cancellationOptions mounts the cancellation of a policy at the request of
// its insured (HU-W1.6-01) when the premium charge is on: the policies that
// can be cancelled are the ones its charge activated, and their refunds go
// back through the same payment gateway (PAYMENT_GATEWAY_URL), sent by a
// worker with the waits, the window and the lease of the charge
// (RETRY_WINDOW_SECONDS, CHARGE_RETRY_*, CHARGE_POLL_INTERVAL_MS and
// CHARGE_CLAIM_LEASE_MS; no variable of its own). Without the charge there is
// nothing to cancel and nothing is mounted.
func cancellationOptions(ctx context.Context, cfg billingConfig, rules domain.InstallmentRules, db *sql.DB,
	verifier security.Verifier) ([]httpadapter.Option, error) {
	if !cfg.enabled {
		return nil, nil
	}
	if db == nil {
		return nil, fmt.Errorf("the cancellation needs origination's database (%s)", envDatabaseURL)
	}
	store := postgres.NewCancellationStore(db)
	gateway := paymentsadapter.NewRefundGateway(payments.NewClient(payments.Config{
		BaseURL: cfg.gatewayURL, Timeout: cfg.gatewayTimeout, Breaker: resilience.DefaultConfig(),
	}))
	worker, err := billing.NewRefundWorker(store, gateway, systemClock{}, cfg.worker)
	if err != nil {
		return nil, err
	}
	svc, err := cancellation.NewService(cancellation.Config{Store: store, Rules: rules, Clock: systemClock{},
		RetryWindow: cfg.retry.Window, Refunds: worker})
	if err != nil {
		return nil, err
	}
	go worker.Run(ctx)
	log.Printf("cancellation (HU-W1.6-01): /v1/policies/{policy_id}/cancellation/quote and /v1/policies/{policy_id}/cancellation for the insured; refunds through %s within %s",
		cfg.gatewayURL, cfg.retry.Window)
	return []httpadapter.Option{httpadapter.WithCancellations(svc, verifier)}, nil
}
