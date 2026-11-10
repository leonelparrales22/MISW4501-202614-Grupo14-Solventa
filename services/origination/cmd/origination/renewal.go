package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"time"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/security"
	emailadapter "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/email"
	httpadapter "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/http"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/postgres"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/formalization"
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

// newExpiringService wires the list of the policies about to expire of the
// advisor (HU-W1.5-01) over origination's database, with the renewal window
// of HU-W1.5-02 (RENEWAL_WINDOW_DAYS): it adds no variable of its own.
func newExpiringService(rules domain.RenewalPolicy, db *sql.DB) (*renewal.ExpiringService, error) {
	if db == nil {
		return nil, fmt.Errorf("the list of the policies about to expire needs origination's database (%s)", envDatabaseURL)
	}
	return renewal.NewExpiringService(postgres.NewExpiringPolicyStore(db), rules, systemClock{})
}

// expiringOptions mounts GET /v1/policies/expiring (HU-W1.5-01) when the
// premium charge is on: the policies it lists are the ones whose charge
// activated them (HU-W1.5-02). Without the charge there is nothing to list
// and nothing is mounted.
func expiringOptions(billing bool, rules domain.RenewalPolicy, db *sql.DB,
	verifier security.Verifier) ([]httpadapter.Option, error) {
	if !billing {
		return nil, nil
	}
	svc, err := newExpiringService(rules, db)
	if err != nil {
		return nil, err
	}
	log.Printf("policies about to expire (HU-W1.5-01): GET /v1/policies/expiring for the advisor, default range of %d days",
		rules.WindowDays)
	return []httpadapter.Option{httpadapter.WithExpiringPolicies(svc, verifier)}, nil
}

// assistedRenewalWiring is what the assisted renewal (HU-W1.5-03) takes from
// the rest of the composition: the online renewal it extends, the profiling,
// the offers its policies were bought from, the consent verifier of the
// offers and the e-mail client.
type assistedRenewalWiring struct {
	renewals *renewal.Service
	db       *sql.DB
	resolver *profiling.Resolver
	offers   ports.OfferReader
	// consents is the verifier of POST /v1/offers (AUDIT_URL); without it the
	// requote of a policy born in the embedded channel fails closed (503).
	consents ports.ConsentVerifier
	// email sends the confirmation code; without EMAIL_URL the code cannot be
	// delivered (503), as in the purchase formalization (HU-W1.1-03).
	email emailadapter.Sender
}

// newAssistedRenewalService wires the assisted renewal over origination's
// database. It adds no variable: it uses the ones of the online renewal,
// AUDIT_URL and EMAIL_URL.
func newAssistedRenewalService(w assistedRenewalWiring) (*renewal.AssistedService, error) {
	if w.db == nil {
		return nil, fmt.Errorf("the assisted renewal needs origination's database (%s)", envDatabaseURL)
	}
	var sender ports.ConsentCodeSender = formalization.UnavailableSender{}
	if w.email != nil {
		sender = emailadapter.NewRenewalConsentNotifier(w.email)
	}
	customers := postgres.NewCustomerStore(w.db)
	return renewal.NewAssistedService(renewal.AssistedConfig{
		Renewals: w.renewals, Requotes: postgres.NewRenewalRequoteStore(w.db), Assisted: postgres.NewRenewalStore(w.db),
		Origins: renewal.OriginFinder{Policies: postgres.NewPolicyStore(w.db), Charges: postgres.NewChargeStore(w.db),
			Offers: w.offers, BySubject: customers, ByDocument: customers},
		Profiles: w.resolver, Consents: w.consents,
		Challenges: postgres.NewRenewalConsentChallengeStore(w.db), Sender: sender,
	})
}

// assistedRenewalOptions mounts the assisted renewal (HU-W1.5-03) when the
// online renewal of HU-W1.5-02 is wired (database and premium charge) and the
// human routes are on (AUTH_ISSUER, a verifier). Otherwise nothing is mounted.
func assistedRenewalOptions(w assistedRenewalWiring, verifier security.Verifier) ([]httpadapter.Option, error) {
	if w.renewals == nil || verifier == nil {
		return nil, nil
	}
	svc, err := newAssistedRenewalService(w)
	if err != nil {
		return nil, err
	}
	log.Printf("assisted renewal (HU-W1.5-03): /v1/policies/{policy_id}/assisted-renewal for the advisor")
	if w.consents == nil {
		log.Printf("assisted renewal (HU-W1.5-03): AUDIT_URL is not set, the requote of a policy born in the embedded channel answers 503 (fail-closed)")
	}
	if w.email == nil {
		log.Printf("assisted renewal (HU-W1.5-03): %s is not set, the confirmation code cannot be delivered", envEmailURL)
	}
	return []httpadapter.Option{httpadapter.WithAssistedRenewals(svc, verifier)}, nil
}
