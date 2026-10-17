package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"time"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/adapters/payments"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/resilience"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/security"
	paymentsadapter "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/payments"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/postgres"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/billing"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
)

// Variables that turn the premium charge on (HA-D02).
const (
	envPaymentGatewayURL = "PAYMENT_GATEWAY_URL"
	envDatabaseURL       = "DATABASE_URL"
)

// Demonstration coverage terms of the embedded offer: the same values the
// contractual document used before the real charge existed. PENDING:
// HU-W1.2-03 persists the insured amount, the term and the coverages with the
// offer (rating engine); then demoCoverageTerms reads them from the offer.
const (
	demoInsuredAmount = 250_000_000
	demoTermMonths    = 120
)

// billingConfig configures the premium charge (HA-D02). It is enabled when
// PAYMENT_GATEWAY_URL is set; then DATABASE_URL (the charge is always durable,
// decision D-2b), EMAIL_URL (every confirmed charge issues its contractual
// document, decision D-16 b) and AUTH_ISSUER (the charge hangs from the human
// routes, so without them saved charges and paid activations would wait
// unprocessed) are required too.
type billingConfig struct {
	enabled        bool
	gatewayURL     string
	databaseURL    string
	gatewayTimeout time.Duration
	// retry is the growing wait and the 24-hour window of the charge.
	retry  resilience.RetryPolicy
	worker billing.WorkerConfig
}

// loadBillingConfig reads the HA-D02 variables.
func loadBillingConfig(getenv func(string) string, cfg *billingConfig) error {
	cfg.gatewayURL = getenv(envPaymentGatewayURL)
	if cfg.gatewayURL == "" {
		return nil
	}
	cfg.enabled = true
	cfg.databaseURL = getenv(envDatabaseURL)
	if cfg.databaseURL == "" {
		return fmt.Errorf("%s is required when %s is set: the premium charge is always persisted (HA-D02)",
			envDatabaseURL, envPaymentGatewayURL)
	}
	if getenv(envEmailURL) == "" {
		return fmt.Errorf("%s is required when %s is set: every confirmed charge must issue its contractual document and notify the insured (HA-D02, decision D-16)",
			envEmailURL, envPaymentGatewayURL)
	}
	if getenv(security.EnvIssuer) == "" {
		return fmt.Errorf("%s is required when %s is set: without the human routes the saved charges and the confirmed activations would not be processed (HA-D02)",
			security.EnvIssuer, envPaymentGatewayURL)
	}

	var err error
	var timeoutMs, windowSeconds, baseMs, maxMs, pollMs, leaseMs int
	if timeoutMs, err = intVar(getenv, "PAYMENT_GATEWAY_TIMEOUT_MS", int(payments.DefaultTimeout/time.Millisecond)); err != nil {
		return err
	}
	if windowSeconds, err = intVar(getenv, "RETRY_WINDOW_SECONDS", int(resilience.DefaultRetryWindow/time.Second)); err != nil {
		return err
	}
	if baseMs, err = intVar(getenv, "CHARGE_RETRY_BASE_MS", int(resilience.DefaultRetryBase/time.Millisecond)); err != nil {
		return err
	}
	if maxMs, err = intVar(getenv, "CHARGE_RETRY_MAX_MS", int(resilience.DefaultRetryMax/time.Millisecond)); err != nil {
		return err
	}
	if maxMs < baseMs {
		return fmt.Errorf("CHARGE_RETRY_MAX_MS (%d) must not be lower than CHARGE_RETRY_BASE_MS (%d)", maxMs, baseMs)
	}
	if pollMs, err = intVar(getenv, "CHARGE_POLL_INTERVAL_MS", int(billing.DefaultPollInterval/time.Millisecond)); err != nil {
		return err
	}
	if leaseMs, err = intVar(getenv, "CHARGE_CLAIM_LEASE_MS", int(billing.DefaultClaimLease/time.Millisecond)); err != nil {
		return err
	}
	if leaseMs <= timeoutMs {
		// A call must never outlive its claim (decision D-8).
		return fmt.Errorf("CHARGE_CLAIM_LEASE_MS (%d) must be greater than PAYMENT_GATEWAY_TIMEOUT_MS (%d)", leaseMs, timeoutMs)
	}
	cfg.gatewayTimeout = time.Duration(timeoutMs) * time.Millisecond
	cfg.retry = resilience.RetryPolicy{
		Base:   time.Duration(baseMs) * time.Millisecond,
		Max:    time.Duration(maxMs) * time.Millisecond,
		Window: time.Duration(windowSeconds) * time.Second,
	}
	cfg.worker = billing.WorkerConfig{
		PollInterval: time.Duration(pollMs) * time.Millisecond,
		ClaimLease:   time.Duration(leaseMs) * time.Millisecond,
		Retry:        cfg.retry,
	}
	return nil
}

// billingWiring is the premium charge, ready to start issuances and serve.
type billingWiring struct {
	starter *billing.Starter
	service *billing.Service
	db      *sql.DB
}

// newBilling wires the premium charge (HA-D02): it opens origination's
// PostgreSQL database and applies its migrations, builds the payment gateway
// client and starts the worker that charges and publishes every activation to
// the given consumers (the contractual document of HU-W1.3-03).
func newBilling(ctx context.Context, cfg billingConfig, handlers ...ports.CoverageActivationHandler) (billingWiring, error) {
	db, err := postgres.Open(ctx, cfg.databaseURL)
	if err != nil {
		return billingWiring{}, err
	}
	if err := postgres.Migrate(ctx, db); err != nil {
		_ = db.Close()
		return billingWiring{}, err
	}
	repo := postgres.NewChargeStore(db)
	gateway := paymentsadapter.NewGateway(payments.NewClient(payments.Config{
		BaseURL: cfg.gatewayURL, Timeout: cfg.gatewayTimeout, Breaker: resilience.DefaultConfig(),
	}))
	clock := systemClock{}
	worker, err := billing.NewWorker(repo, gateway, clock, cfg.worker, handlers...)
	if err != nil {
		_ = db.Close()
		return billingWiring{}, err
	}
	go worker.Run(ctx)
	log.Printf("premium charge: PostgreSQL migrated, gateway %s (timeout %s), retries from %s up to %s within %s, poll every %s, claim lease %s",
		cfg.gatewayURL, cfg.gatewayTimeout, cfg.retry.Base, cfg.retry.Max, cfg.retry.Window, cfg.worker.PollInterval, cfg.worker.ClaimLease)
	return billingWiring{
		starter: billing.NewStarter(repo, demoCoverageTerms, clock, cfg.retry, worker.Notify),
		service: billing.NewService(repo, clock, cfg.retry, worker.Notify),
		db:      db,
	}, nil
}

// demoCoverageTerms returns the coverage terms of an embedded offer: today the
// demonstration values (see demoInsuredAmount). PENDING: HU-W1.2-03.
func demoCoverageTerms(domain.Offer) (domain.CoverageTerms, error) {
	return domain.CoverageTerms{
		InsuredAmount: demoInsuredAmount,
		TermMonths:    demoTermMonths,
		Coverages: []domain.Coverage{
			{Code: domain.CoverageDeath, InsuredAmount: demoInsuredAmount},
			{Code: domain.CoverageDisability, InsuredAmount: demoInsuredAmount},
		},
	}, nil
}

// logWithoutBilling explains what happens to an approved acceptance when the
// premium charge is not configured.
func logWithoutBilling(contractsEnabled bool) {
	log.Printf("%s is not set: an approved acceptance only records the start of its issuance; no premium is charged and no coverage is activated (HA-D02)",
		envPaymentGatewayURL)
	if contractsEnabled {
		log.Printf("%s is set without %s: no contractual document will be issued, because only a confirmed charge activates a coverage (HA-D02, decision D-14)",
			envEmailURL, envPaymentGatewayURL)
	}
}
