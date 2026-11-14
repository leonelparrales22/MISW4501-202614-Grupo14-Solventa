package main

import (
	"context"
	"log"
	"time"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/expiryalert"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
)

// envExpiryAlertInterval is how often the expiry alerts (HU-M3.2-01) are
// looked for.
const envExpiryAlertInterval = "EXPIRY_ALERT_INTERVAL_MS"

// expiryAlertConfig configures the alerts before a policy expires.
type expiryAlertConfig struct {
	interval time.Duration
}

// loadExpiryAlertConfig reads the interval of the sweep; one hour by default.
func loadExpiryAlertConfig(getenv func(string) string, cfg *expiryAlertConfig) error {
	ms, err := intVar(getenv, envExpiryAlertInterval, int(expiryalert.DefaultInterval/time.Millisecond))
	if err != nil {
		return err
	}
	cfg.interval = time.Duration(ms) * time.Millisecond
	return nil
}

// newExpiryAlertSweeper wires the alerts 30, 15 and 5 days before a policy
// expires (HU-M3.2-01) over the stores of origination and the policy core
// (nil without POLICY_CORE_URL). In memory there are no policies of
// origination, so only the policy core's are alerted; without either source
// there is nothing to alert and it answers nil.
func newExpiryAlertSweeper(st stores, portfolio ports.PolicyPortfolio) (*expiryalert.Sweeper, error) {
	if st.expiringOwn == nil && portfolio == nil {
		log.Printf("expiry alerts (HU-M3.2-01): no source of policies (%s or %s), nothing is alerted",
			envDatabaseURL, envPolicyCoreURL)
		return nil, nil
	}
	return expiryalert.New(expiryalert.Config{
		Own: st.expiringOwn, Portfolio: portfolio, Holders: st.linked, Recorder: st.expiryAlerts, Clock: systemClock{},
	})
}

// startExpiryAlerts starts the sweep of the expiry alerts, if there is a
// source of policies. The alerts reach the insured as the
// PolicyExpiryApproaching events the relay publishes.
func startExpiryAlerts(ctx context.Context, cfg expiryAlertConfig, st stores, portfolio ports.PolicyPortfolio) error {
	sweeper, err := newExpiryAlertSweeper(st, portfolio)
	if err != nil || sweeper == nil {
		return err
	}
	go sweeper.Run(ctx, cfg.interval)
	log.Printf("expiry alerts (HU-M3.2-01): 30, 15 and 5 days before the end of the policies of origination=%t and of the policy core=%t, sweep every %s",
		st.expiringOwn != nil, portfolio != nil, cfg.interval)
	return nil
}
