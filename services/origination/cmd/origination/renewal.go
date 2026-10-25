package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"time"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/postgres"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/profiling"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/rating"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/renewal"
)

// renewalConfig configures the online renewal of a policy (HU-W1.5-02). It
// needs the premium charge: the renewal charges and issues through it.
type renewalConfig struct {
	rules        domain.RenewalPolicy
	pollInterval time.Duration
}

// loadRenewalConfig reads the HU-W1.5-02 variables; every one has a default.
func loadRenewalConfig(getenv func(string) string, cfg *renewalConfig) error {
	cfg.rules = domain.DefaultRenewalPolicy
	var err error
	if cfg.rules.WindowDays, err = intVar(getenv, "RENEWAL_WINDOW_DAYS", cfg.rules.WindowDays); err != nil {
		return err
	}
	if cfg.rules.TermMonths, err = intVar(getenv, "RENEWAL_TERM_MONTHS", cfg.rules.TermMonths); err != nil {
		return err
	}
	hours, err := intVar(getenv, "RENEWAL_PAYMENT_RETRY_HOURS", int(cfg.rules.RetryWithin/time.Hour))
	if err != nil {
		return err
	}
	cfg.rules.RetryWithin = time.Duration(hours) * time.Hour
	if cfg.rules.GraceDays, err = nonNegativeIntVar(getenv, "RENEWAL_GRACE_DAYS", cfg.rules.GraceDays); err != nil {
		return err
	}
	pollMs, err := intVar(getenv, "RENEWAL_POLL_INTERVAL_MS", int(renewal.DefaultPollInterval/time.Millisecond))
	if err != nil {
		return err
	}
	cfg.pollInterval = time.Duration(pollMs) * time.Millisecond
	if err := cfg.rules.Validate(); err != nil {
		return fmt.Errorf("renewal configuration: %w", err)
	}
	return nil
}

// newRenewalService wires the online renewal over origination's database and
// starts the sweep that closes the windows nobody answered. wake wakes the
// charge worker after a renewal is accepted; notifier may be nil.
func newRenewalService(ctx context.Context, cfg renewalConfig, db *sql.DB, resolver *profiling.Resolver,
	notifier ports.RenewalNotifier, wake func()) (*renewal.Service, error) {
	if db == nil {
		return nil, fmt.Errorf("the policy renewal needs origination's database (%s)", envDatabaseURL)
	}
	svc, err := renewal.NewService(renewal.Config{
		Policies: postgres.NewPolicyStore(db), Renewals: postgres.NewRenewalStore(db), Charges: postgres.NewChargeStore(db),
		Customers: postgres.NewCustomerStore(db), Profiles: resolver, Rater: rating.New(),
		Notifier: notifier, Rules: cfg.rules, Clock: systemClock{}, Wake: wake,
	})
	if err != nil {
		return nil, err
	}
	go svc.Run(ctx, cfg.pollInterval)
	log.Printf("policy renewal: window %d days before the end, term %d months, %s to fix a payment, grace %d days, sweep every %s",
		cfg.rules.WindowDays, cfg.rules.TermMonths, cfg.rules.RetryWithin, cfg.rules.GraceDays, cfg.pollInterval)
	return svc, nil
}
