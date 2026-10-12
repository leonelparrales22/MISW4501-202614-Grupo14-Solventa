package issuance

import (
	"context"
	"log/slog"
	"time"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
)

// Defaults of the acceptance processing (HU-W1.3-01).
const (
	DefaultPollInterval   = time.Second
	DefaultAttemptTimeout = 5 * time.Second
	DefaultRetryBase      = 2 * time.Second
	DefaultRetryMax       = time.Minute
	defaultBatchSize      = 50
)

// ProcessorConfig tunes the acceptance processing. Zero values take the defaults.
type ProcessorConfig struct {
	// PollInterval is how often due acceptances are looked up.
	PollInterval time.Duration
	// AttemptTimeout bounds one processing attempt of one acceptance.
	AttemptTimeout time.Duration
	// RetryBase is the wait after the first failed attempt; it doubles after
	// each failure up to RetryMax.
	RetryBase time.Duration
	RetryMax  time.Duration
}

func (c ProcessorConfig) withDefaults() ProcessorConfig {
	if c.PollInterval <= 0 {
		c.PollInterval = DefaultPollInterval
	}
	if c.AttemptTimeout <= 0 {
		c.AttemptTimeout = DefaultAttemptTimeout
	}
	if c.RetryBase <= 0 {
		c.RetryBase = DefaultRetryBase
	}
	if c.RetryMax < c.RetryBase {
		c.RetryMax = max(DefaultRetryMax, c.RetryBase)
	}
	return c
}

// Processor runs underwriting and the later steps of every registered
// acceptance, asynchronously. A failed attempt keeps the acceptance registered
// and schedules a new attempt with a growing wait, so the journey resumes from
// the acceptance without the customer repeating it (CA-5).
type Processor struct {
	offers      ports.OfferRepository
	acceptances ports.AcceptanceRepository
	underwriter ports.Underwriter
	reviews     ports.ReviewQueue
	issuance    ports.IssuanceStarter
	clock       ports.Clock
	cfg         ProcessorConfig
	wake        chan struct{}
}

// NewProcessor builds a Processor.
func NewProcessor(offers ports.OfferRepository, acceptances ports.AcceptanceRepository, underwriter ports.Underwriter,
	reviews ports.ReviewQueue, issuance ports.IssuanceStarter, clock ports.Clock, cfg ProcessorConfig) *Processor {
	return &Processor{
		offers: offers, acceptances: acceptances, underwriter: underwriter, reviews: reviews, issuance: issuance,
		clock: clock, cfg: cfg.withDefaults(), wake: make(chan struct{}, 1),
	}
}

// Notify asks Run to look for due acceptances now. It never blocks.
func (p *Processor) Notify() {
	select {
	case p.wake <- struct{}{}:
	default:
	}
}

// Run processes due acceptances every PollInterval and on Notify until ctx ends.
func (p *Processor) Run(ctx context.Context) {
	ticker := time.NewTicker(p.cfg.PollInterval)
	defer ticker.Stop()
	for {
		p.ProcessDue(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-p.wake:
		}
	}
}

// ProcessDue runs one attempt for each acceptance whose next attempt is due
// and returns how many it attempted.
func (p *Processor) ProcessDue(ctx context.Context) int {
	due, err := p.acceptances.ListDue(ctx, p.clock.Now().UTC(), defaultBatchSize)
	if err != nil {
		slog.Error("could not list the due acceptances", "error", err)
		return 0
	}
	for _, a := range due {
		if ctx.Err() != nil {
			break
		}
		p.attempt(ctx, a)
	}
	return len(due)
}

func (p *Processor) attempt(ctx context.Context, a domain.Acceptance) {
	actx, cancel := context.WithTimeout(ctx, p.cfg.AttemptTimeout)
	defer cancel()

	err := p.advance(actx, &a)
	if err == nil {
		return
	}
	now := p.clock.Now().UTC()
	a.Attempts++
	a.LastFailureAt = now
	a.NextAttemptAt = now.Add(p.backoff(a.Attempts))
	a.UpdatedAt = now
	slog.Warn("acceptance processing failed, it will be resumed",
		"acceptance_id", a.ID, "attempts", a.Attempts, "next_attempt_at", a.NextAttemptAt, "error", err)
	if uerr := p.acceptances.Update(ctx, a); uerr != nil {
		// The acceptance stays registered and due, so the next poll retries it.
		slog.Error("could not schedule the next acceptance attempt", "acceptance_id", a.ID, "error", uerr)
	}
}

// advance moves a registered acceptance to issuance_pending (CA-2) or
// in_review (CA-3). The underwriting decision is persisted before acting on
// it, so a resumed acceptance only repeats the idempotent step that failed.
func (p *Processor) advance(ctx context.Context, a *domain.Acceptance) error {
	offer, err := p.offers.Get(ctx, a.OfferID)
	if err != nil {
		return err
	}

	if a.Decision == nil {
		d, err := p.underwriter.Decide(ctx, offer, *a)
		if err != nil {
			return err
		}
		a.Decision = &d
		a.UpdatedAt = p.clock.Now().UTC()
		if err := p.acceptances.Update(ctx, *a); err != nil {
			return err
		}
	}

	next := domain.AcceptanceIssuancePending
	switch a.Decision.Outcome {
	case domain.UnderwritingHumanReview:
		next = domain.AcceptanceInReview
		err = p.reviews.Enqueue(ctx, domain.ReviewRequest{
			AcceptanceID: a.ID,
			OfferID:      offer.ID,
			CustomerID:   offer.CustomerID,
			Applicant:    offer.Applicant,
			Premium:      offer.Premium,
			Currency:     offer.Currency,
			Reason:       a.Decision.Reason,
			ReceivedAt:   p.clock.Now().UTC(),
		})
	default:
		err = p.issuance.Start(ctx, *a, offer)
	}
	if err != nil {
		return err
	}

	a.Status = next
	a.NextAttemptAt = time.Time{}
	a.UpdatedAt = p.clock.Now().UTC()
	return p.acceptances.Update(ctx, *a)
}

// backoff returns the wait before the next attempt after the given number of
// failed attempts: RetryBase doubled per failure, capped at RetryMax.
func (p *Processor) backoff(attempts int) time.Duration {
	wait := p.cfg.RetryBase
	for i := 1; i < attempts && wait < p.cfg.RetryMax; i++ {
		wait *= 2
	}
	return min(wait, p.cfg.RetryMax)
}
