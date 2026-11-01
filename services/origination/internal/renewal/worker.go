package renewal

import (
	"context"
	"log/slog"
	"time"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
)

const (
	// DefaultPollInterval is how often the worker closes windows and payments.
	DefaultPollInterval = time.Minute
	// sweepBatch bounds the rows one sweep handles.
	sweepBatch = 100
	// LapseLookback is how far back a policy that ended without a renewal is
	// still told: older ones ended before the feature or long ago.
	LapseLookback = 30 * 24 * time.Hour
)

// Projector projects the coverage activations into the policies the insured
// sees (HU-W1.5-02). It is a ports.CoverageActivationHandler: the charge worker
// hands it every activation, and it is idempotent by policy.
type Projector struct {
	policies ports.PolicyRepository
}

var _ ports.CoverageActivationHandler = (*Projector)(nil)

// NewProjector builds a Projector.
func NewProjector(policies ports.PolicyRepository) *Projector { return &Projector{policies: policies} }

// Receive stores the policy of the activation.
func (p *Projector) Receive(ctx context.Context, e domain.CoverageActivated) error {
	return p.policies.Create(ctx, domain.PolicyFrom(e))
}

// Sweep closes what nobody closed and tells the insured. It records a lapse for
// each policy whose window ended with no answer (not renewed, no response),
// settles the renewals whose payment ended, and sends the notice of every lapse
// not yet told. It returns how many lapses it recorded.
func (s *Service) Sweep(ctx context.Context) (int, error) {
	now := s.clock.Now().UTC()
	lapsed, err := s.policies.ListLapsed(ctx, now.Add(-LapseLookback), now, sweepBatch)
	if err != nil {
		return 0, err
	}
	recorded := 0
	for _, p := range lapsed {
		r := domain.Renewal{
			ID: domain.RenewalID(p.ID, p.CoverageEnd), PolicyID: p.ID, CustomerID: p.CustomerID, PeriodEnd: p.CoverageEnd,
			Status: domain.RenewalNotRenewed, Reason: domain.NotRenewedNoResponse, Currency: p.Currency,
			ClosedAt: now, CreatedAt: now,
		}
		// UNIQUE (policy, period): if the insured accepted at the last moment
		// the acceptance stands and nothing is recorded here.
		if _, created, err := s.renewals.Create(ctx, r); err != nil {
			slog.ErrorContext(ctx, "could not record the lapse of a policy", "policy_id", p.ID, "error", err)
		} else if created {
			recorded++
			slog.InfoContext(ctx, "policy not renewed: the window closed with no answer", "policy_id", p.ID)
		}
	}

	accepted, err := s.renewals.ListAccepted(ctx, sweepBatch)
	if err != nil {
		return recorded, err
	}
	for _, r := range accepted {
		s.settle(ctx, r, now)
	}

	s.notifyLapses(ctx, now)
	return recorded, nil
}

// notifyLapses sends the notice of the lapses the insured was not told about.
// A failed send is retried by the next sweep with the same idempotency key.
func (s *Service) notifyLapses(ctx context.Context, now time.Time) {
	if s.notifier == nil {
		return
	}
	pending, err := s.renewals.ListUnnotified(ctx, sweepBatch)
	if err != nil {
		slog.ErrorContext(ctx, "could not list the renewals to notify", "error", err)
		return
	}
	for _, r := range pending {
		p, err := s.policies.Get(ctx, r.PolicyID)
		if err != nil {
			slog.ErrorContext(ctx, "could not read the policy of a lapse", "policy_id", r.PolicyID, "error", err)
			continue
		}
		err = s.notifier.Notify(ctx, domain.RenewalNotice{
			To: p.Holder.Email, HolderName: p.Holder.FullName, PolicyID: p.ID, CoverageEnded: p.CoverageEnd,
			Reason: r.Reason, GraceDays: s.rules.GraceDays, IdempotencyKey: "renewal-lapsed:" + r.ID,
		})
		if err != nil {
			slog.WarnContext(ctx, "the not-renewed notice could not be sent; it is retried", "renewal_id", r.ID, "error", err)
			continue
		}
		if err := s.renewals.MarkNotified(ctx, r.ID, now, false); err != nil {
			slog.ErrorContext(ctx, "could not record the not-renewed notice", "renewal_id", r.ID, "error", err)
		}
	}
}

// Run sweeps every interval until ctx ends.
func (s *Service) Run(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = DefaultPollInterval
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		if _, err := s.Sweep(ctx); err != nil && ctx.Err() == nil {
			slog.ErrorContext(ctx, "renewal sweep failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
