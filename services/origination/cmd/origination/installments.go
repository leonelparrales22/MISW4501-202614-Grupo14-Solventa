package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/postgres"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/installments"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
)

// Variables of the monthly instalments and the cancellation for non-payment
// (HU-W1.6-02). INSTALLMENT_DAY_MS shortens the day for a demonstration: with
// 60000, a month is 30 minutes and the grace period 30 minutes too.
const (
	envInstallmentInterval    = "INSTALLMENT_SWEEP_INTERVAL_MS"
	envInstallmentGraceDays   = "INSTALLMENT_GRACE_DAYS"
	envInstallmentAttemptDays = "INSTALLMENT_ATTEMPT_DAYS"
	envInstallmentDay         = "INSTALLMENT_DAY_MS"
	envInstallmentRetryMs     = "INSTALLMENT_UNKNOWN_RETRY_MS"
)

// installmentConfig configures the instalments.
type installmentConfig struct {
	interval time.Duration
	rules    domain.InstallmentRules
}

// loadInstallmentConfig reads the rules of the instalments: 30 days of grace,
// charges on days 1, 8 and 15 and a sweep every hour, unless the variables
// say otherwise.
func loadInstallmentConfig(getenv func(string) string, cfg *installmentConfig) error {
	def := domain.DefaultInstallmentRules
	interval, err := intVar(getenv, envInstallmentInterval, int(installments.DefaultInterval/time.Millisecond))
	if err != nil {
		return err
	}
	grace, err := intVar(getenv, envInstallmentGraceDays, def.GraceDays)
	if err != nil {
		return err
	}
	day, err := intVar(getenv, envInstallmentDay, int(def.Day/time.Millisecond))
	if err != nil {
		return err
	}
	retry, err := intVar(getenv, envInstallmentRetryMs, int(def.RetryUnknownAfter/time.Millisecond))
	if err != nil {
		return err
	}
	attempts := def.AttemptDays
	if raw := strings.TrimSpace(getenv(envInstallmentAttemptDays)); raw != "" {
		attempts = nil
		for _, part := range strings.Split(raw, ",") {
			d, err := strconv.Atoi(strings.TrimSpace(part))
			if err != nil {
				return fmt.Errorf("%s must be a list of days separated by commas, like 1,8,15: %w", envInstallmentAttemptDays, err)
			}
			attempts = append(attempts, d)
		}
	}
	cfg.interval = time.Duration(interval) * time.Millisecond
	cfg.rules = domain.InstallmentRules{
		GraceDays: grace, AttemptDays: attempts, Day: time.Duration(day) * time.Millisecond,
		RetryUnknownAfter: time.Duration(retry) * time.Millisecond,
	}
	if err := cfg.rules.Validate(); err != nil {
		return fmt.Errorf("the instalments (%s, %s, %s): %w", envInstallmentGraceDays, envInstallmentAttemptDays,
			envInstallmentDay, err)
	}
	if cfg.interval <= 0 {
		return fmt.Errorf("%s must be positive", envInstallmentInterval)
	}
	return nil
}

// startInstallments starts the sweep that bills the monthly instalments of the
// policies origination issued, charges them and cancels for non-payment the
// ones that are not paid (HU-W1.6-02). It needs the database and the payment
// gateway of the premium charge; notifier may be nil (no e-mail provider):
// the cancellations then wait for their e-mail, and the push still goes out
// from the event.
func startInstallments(ctx context.Context, cfg installmentConfig, db *sql.DB, gateway ports.InstallmentGateway,
	notifier ports.CancellationNotifier) error {
	store := postgres.NewInstallmentStore(db)
	sweeper, err := installments.New(installments.Config{
		Store: store, Methods: store, Gateway: gateway, Notifier: notifier, Rules: cfg.rules, Clock: systemClock{},
	})
	if err != nil {
		return err
	}
	go sweeper.Run(ctx, cfg.interval)
	log.Printf("instalments (HU-W1.6-02): grace of %d days, charges on days %v, a day lasts %s, sweep every %s, e-mail=%t",
		cfg.rules.GraceDays, cfg.rules.AttemptDays, cfg.rules.Day, cfg.interval, notifier != nil)
	return nil
}
