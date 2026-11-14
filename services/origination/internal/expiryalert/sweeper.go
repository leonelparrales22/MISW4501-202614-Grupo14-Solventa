// Package expiryalert alerts the insured 30, 15 and 5 days before a policy
// expires (HU-M3.2-01). A sweep looks at the active policies of both sources
// of the wallet, origination's own (life) and the policy core's (auto, home
// and travel, asked holder by holder), and records the alert of the last
// milestone each one crossed with its PolicyExpiryApproaching event in the
// outbox. The notifications service turns the event into the push and the
// entry of the notification center.
//
// Several tasks of origination sweep at once: the store records each policy,
// period and milestone once, so nothing is alerted twice.
package expiryalert

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
)

// DefaultInterval is how often the alerts are looked for: the milestones are
// days, so an hour is enough.
const DefaultInterval = time.Hour

// Config is what the sweeper needs. Own and Portfolio are the sources; either
// may be nil (not configured), but not both. Holders is required with the
// Portfolio.
type Config struct {
	Own       ports.ExpiringOwnPolicies
	Portfolio ports.PolicyPortfolio
	Holders   ports.LinkedHolders
	Recorder  ports.ExpiryAlertRecorder
	Clock     ports.Clock
}

// Sweeper finds the policies that crossed a milestone and records their alert.
type Sweeper struct {
	cfg Config
}

// New builds a Sweeper.
func New(cfg Config) (*Sweeper, error) {
	switch {
	case cfg.Recorder == nil || cfg.Clock == nil:
		return nil, errors.New("expiry alerts: a recorder and a clock are required")
	case cfg.Own == nil && cfg.Portfolio == nil:
		return nil, errors.New("expiry alerts: at least one source of policies is required")
	case cfg.Portfolio != nil && cfg.Holders == nil:
		return nil, errors.New("expiry alerts: the policy core needs the linked holders")
	}
	return &Sweeper{cfg: cfg}, nil
}

// Sweep records the alerts due now and returns how many it recorded. A
// failure of the policy core for one holder is logged and that holder skipped;
// a failure to list a source or to record an alert is returned after the rest
// was swept, and the next sweep tries again.
func (s *Sweeper) Sweep(ctx context.Context) (int, error) {
	now := s.cfg.Clock.Now().UTC()
	recorded := 0
	var errs []error
	if s.cfg.Own != nil {
		n, err := s.sweepOwn(ctx, now)
		recorded += n
		errs = append(errs, err)
	}
	if s.cfg.Portfolio != nil {
		n, err := s.sweepCore(ctx, now)
		recorded += n
		errs = append(errs, err)
	}
	return recorded, errors.Join(errs...)
}

// sweepOwn alerts the policies origination issued. Their holder is the
// customer, the subject of the token.
func (s *Sweeper) sweepOwn(ctx context.Context, now time.Time) (int, error) {
	policies, err := s.cfg.Own.ListExpiringUnrenewed(ctx, now, horizon(now))
	if err != nil {
		return 0, err
	}
	recorded := 0
	var errs []error
	for _, p := range policies {
		created, err := s.alert(ctx, domain.WalletPolicyOf(p, now), p.CustomerID, now)
		if created {
			recorded++
		}
		errs = append(errs, err)
	}
	return recorded, errors.Join(errs...)
}

// sweepCore alerts the active policies of the policy core, holder by holder:
// the holder id in the core is the subject of the token.
func (s *Sweeper) sweepCore(ctx context.Context, now time.Time) (int, error) {
	subjects, err := s.cfg.Holders.LinkedSubjects(ctx)
	if err != nil {
		return 0, err
	}
	recorded := 0
	var errs []error
	for _, subject := range subjects {
		policies, err := s.cfg.Portfolio.HolderPolicies(ctx, subject)
		if err != nil {
			slog.WarnContext(ctx, "the policy core did not answer for a holder; their expiry alerts wait for the next sweep",
				"error", err)
			continue
		}
		for _, p := range policies {
			created, err := s.alert(ctx, p, subject, now)
			if created {
				recorded++
			}
			errs = append(errs, err)
		}
	}
	return recorded, errors.Join(errs...)
}

// alert records the alert of p if it crossed a milestone not yet alerted.
func (s *Sweeper) alert(ctx context.Context, p domain.WalletPolicy, subject string, now time.Time) (bool, error) {
	e, due := domain.NewPolicyExpiryApproaching(p, subject, now)
	if !due {
		return false, nil
	}
	created, err := s.cfg.Recorder.RecordExpiryAlert(ctx, e)
	if err != nil {
		slog.ErrorContext(ctx, "could not record the expiry alert of a policy", "policy_id", p.ID, "error", err)
		return false, err
	}
	if created {
		slog.InfoContext(ctx, "policy expiry alert recorded", "policy_id", e.PolicyID, "source", e.Source,
			"milestone_days", e.MilestoneDays, "days_left", e.DaysLeft, "event_id", e.EventID())
	}
	return created, nil
}

// horizon is the start of the day, in Colombia, after the farthest milestone:
// a coverage ending before it has at most 30 days left.
func horizon(now time.Time) time.Time {
	last := domain.ExpiryMilestones[len(domain.ExpiryMilestones)-1]
	y, m, d := now.In(domain.ExpiryZone).Date()
	return time.Date(y, m, d+last+1, 0, 0, 0, 0, domain.ExpiryZone).UTC()
}

// Run sweeps every interval until ctx ends.
func (s *Sweeper) Run(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = DefaultInterval
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		if _, err := s.Sweep(ctx); err != nil && ctx.Err() == nil {
			slog.ErrorContext(ctx, "expiry alert sweep failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
