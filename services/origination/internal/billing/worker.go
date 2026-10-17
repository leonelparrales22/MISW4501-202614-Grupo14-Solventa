package billing

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/idempotency"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/observability"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/resilience"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
)

// Defaults of the premium charge (HA-D02).
const (
	// DefaultPollInterval is how often due charges are looked up (a new
	// charge wakes the worker at once).
	DefaultPollInterval = time.Second
	// DefaultClaimLease is how long a claim holds a charge or the publication
	// of its activation; it must be longer than the gateway timeout (D-8).
	DefaultClaimLease = 30 * time.Second
	batchSize         = 50
	instrumentation   = "origination/billing"
)

// WorkerConfig tunes the worker. Zero values take the defaults.
type WorkerConfig struct {
	PollInterval time.Duration
	ClaimLease   time.Duration
	// Retry is the growing wait between charge attempts (EC-D02: 500 ms
	// doubling up to 60 s). Its window is set on each charge when it is
	// created or resumed; here only Base and Max matter.
	Retry resilience.RetryPolicy
}

func (c WorkerConfig) withDefaults() WorkerConfig {
	if c.PollInterval <= 0 {
		c.PollInterval = DefaultPollInterval
	}
	if c.ClaimLease <= 0 {
		c.ClaimLease = DefaultClaimLease
	}
	if c.Retry.Base <= 0 {
		c.Retry = resilience.DefaultRetryPolicy()
	}
	return c
}

// Worker charges the due premiums and publishes the coverage activation of
// every confirmed charge. Several workers (one per origination task) may run
// at once: the atomic claim lets only one of them call the gateway for a
// charge, and only one publish its activation.
type Worker struct {
	repo       ports.ChargeRepository
	gateway    ports.PaymentGateway
	handlers   []ports.CoverageActivationHandler
	clock      ports.Clock
	cfg        WorkerConfig
	runner     *idempotency.Runner[domain.Charge]
	activation resilience.RetryPolicy
	wake       chan struct{}

	attempts       metric.Int64Counter
	failed         metric.Int64Counter
	published      metric.Int64Counter
	publishFailed  metric.Int64Counter
	toConfirmation metric.Float64Histogram
}

// NewWorker builds a Worker that publishes every activation to the given
// consumers. At least one is required (decision D-16 b): a confirmed payment
// must always reach the contractual document, never stay without a policy.
func NewWorker(repo ports.ChargeRepository, gateway ports.PaymentGateway, clock ports.Clock, cfg WorkerConfig,
	handlers ...ports.CoverageActivationHandler) (*Worker, error) {
	if len(handlers) == 0 {
		return nil, errors.New("billing: at least one coverage activation consumer is required")
	}
	cfg = cfg.withDefaults()
	runner, err := idempotency.NewRunner[domain.Charge](repo, idempotency.RunnerConfig{
		Policy: cfg.Retry, Lease: cfg.ClaimLease, Now: clock.Now,
	})
	if err != nil {
		return nil, err
	}
	m := observability.Meter(instrumentation)
	attempts, _ := m.Int64Counter("charge.attempt.completed",
		metric.WithDescription("Premium charge attempts, by outcome (confirmed, rejected, ambiguous, not_sent)"))
	failed, _ := m.Int64Counter("charge.failed", metric.WithDescription("Premium charges that ended in an issuance exception, by reason"))
	published, _ := m.Int64Counter("charge.activation.published", metric.WithDescription("Coverage activations handed to their consumers"))
	publishFailed, _ := m.Int64Counter("charge.activation.publish_failed", metric.WithDescription("Failed publications of a coverage activation"))
	toConfirmation, _ := m.Float64Histogram("charge.acceptance_to_confirmed",
		metric.WithDescription("Seconds from the offer acceptance until the premium charge is confirmed"), metric.WithUnit("s"))
	return &Worker{
		repo: repo, gateway: gateway, handlers: handlers, clock: clock, cfg: cfg, runner: runner,
		// The publication is retried with the same waits but without a
		// window: the premium is already charged, so giving up would leave a
		// paid policy without its activation.
		activation: resilience.RetryPolicy{Base: cfg.Retry.Base, Max: cfg.Retry.Max},
		wake:       make(chan struct{}, 1),
		attempts:   attempts, failed: failed, published: published, publishFailed: publishFailed,
		toConfirmation: toConfirmation,
	}, nil
}

// Notify asks Run to look for due charges now. It never blocks.
func (w *Worker) Notify() {
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

// Run processes due charges and activations every PollInterval and on Notify
// until ctx ends.
func (w *Worker) Run(ctx context.Context) {
	ticker := time.NewTicker(w.cfg.PollInterval)
	defer ticker.Stop()
	for {
		w.ProcessDue(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-w.wake:
		}
	}
}

// ProcessDue runs one attempt for every due charge and one publication for
// every due activation, and returns how many it looked at.
func (w *Worker) ProcessDue(ctx context.Context) int {
	now := w.clock.Now().UTC()
	ids, err := w.repo.ListDue(ctx, now, batchSize)
	if err != nil {
		slog.ErrorContext(ctx, "could not list the due premium charges", "error", err)
	}
	for _, id := range ids {
		if ctx.Err() != nil {
			return len(ids)
		}
		w.charge(ctx, id)
	}
	pending, err := w.repo.ListActivationsDue(ctx, w.clock.Now().UTC(), batchSize)
	if err != nil {
		slog.ErrorContext(ctx, "could not list the due coverage activations", "error", err)
	}
	for _, id := range pending {
		if ctx.Err() != nil {
			break
		}
		w.retryActivation(ctx, id)
	}
	return len(ids) + len(pending)
}

// charge runs one attempt of the charge of an acceptance and, when it is
// confirmed, publishes its activation right away (the claim is still held).
func (w *Worker) charge(ctx context.Context, acceptanceID string) {
	ctx, span := observability.Tracer(instrumentation).Start(ctx, "charge.attempt",
		trace.WithAttributes(attribute.String("acceptance_id", acceptanceID)))
	defer span.End()

	rep, err := w.runner.Process(ctx, acceptanceID, func(cctx context.Context, c domain.Charge) (idempotency.Outcome, error) {
		span.SetAttributes(attribute.Int("attempt", c.Attempts+1))
		ref, err := w.gateway.Charge(cctx, c)
		return idempotency.Outcome{Reference: ref}, err
	})
	if !rep.Claimed && err == nil {
		return // another worker holds it, or it is not due anymore
	}
	if rep.Outcome != "" {
		w.attempts.Add(ctx, 1, metric.WithAttributes(attribute.String("outcome", string(rep.Outcome))))
	}
	if err != nil {
		span.RecordError(err)
		if !rep.Claimed {
			// Nothing was called and nothing changed: the charge is still due.
			span.SetStatus(codes.Error, "could not claim the charge")
			slog.ErrorContext(ctx, "could not claim the premium charge; it is retried on the next poll",
				"acceptance_id", acceptanceID, "error", err)
			return
		}
		span.SetStatus(codes.Error, "could not record the charge attempt")
		slog.ErrorContext(ctx, "could not record the premium charge attempt; it is retried when its claim expires",
			"acceptance_id", acceptanceID, "outcome", rep.Outcome, "error", err)
		return
	}

	c := rep.Record
	switch rep.Status {
	case idempotency.StatusConfirmed:
		slog.InfoContext(ctx, "premium charge confirmed", "acceptance_id", c.AcceptanceID, "policy_id", c.PolicyID,
			"attempts", c.Attempts+1)
		w.toConfirmation.Record(ctx, c.ConfirmedAt.Sub(c.AcceptedAt).Seconds())
		w.publish(ctx, c)
	case idempotency.StatusFailed:
		span.SetStatus(codes.Error, "premium charge failed")
		w.failed.Add(ctx, 1, metric.WithAttributes(attribute.String("reason", string(c.FailureReason))))
		slog.WarnContext(ctx, "premium charge failed: issuance exception", "acceptance_id", c.AcceptanceID,
			"reason", c.FailureReason, "attempts", c.Attempts, "error", rep.CallErr)
	default:
		span.RecordError(rep.CallErr)
		slog.WarnContext(ctx, "premium charge attempt failed, it will be retried with the same key",
			"acceptance_id", c.AcceptanceID, "outcome", rep.Outcome, "attempts", c.Attempts,
			"next_attempt_at", c.NextAttemptAt, "error", rep.CallErr)
	}
}

// retryActivation claims the publication of a confirmed charge whose
// activation is still unpublished, and publishes it.
func (w *Worker) retryActivation(ctx context.Context, acceptanceID string) {
	c, claimed, err := w.repo.ClaimActivation(ctx, acceptanceID, w.clock.Now().UTC(), w.cfg.ClaimLease)
	if err != nil {
		slog.ErrorContext(ctx, "could not claim the coverage activation", "acceptance_id", acceptanceID, "error", err)
		return
	}
	if claimed {
		w.publish(ctx, c)
	}
}

// publish hands the activation of a confirmed charge, held by this worker, to
// every consumer. The consumers are idempotent by policy, so a failed
// publication is retried with exactly the same event.
func (w *Worker) publish(ctx context.Context, c domain.Charge) {
	ctx, span := observability.Tracer(instrumentation).Start(ctx, "charge.activation.publish",
		trace.WithAttributes(attribute.String("acceptance_id", c.AcceptanceID), attribute.String("policy_id", c.PolicyID)))
	defer span.End()

	ev := c.ActivationEvent()
	var perr error
	for _, h := range w.handlers {
		if err := h.Receive(ctx, ev); err != nil {
			perr = fmt.Errorf("a coverage activation consumer failed: %w", err)
			break
		}
	}
	now := w.clock.Now().UTC()
	if perr == nil {
		if _, err := w.repo.MarkActivationPublished(ctx, c, now); err != nil {
			// The consumers already have it; publishing again is harmless.
			slog.ErrorContext(ctx, "could not record the published coverage activation", "acceptance_id", c.AcceptanceID,
				"policy_id", c.PolicyID, "error", err)
			return
		}
		w.published.Add(ctx, 1)
		slog.InfoContext(ctx, "coverage activation published", "acceptance_id", c.AcceptanceID, "policy_id", c.PolicyID,
			"event_id", ev.EventID)
		return
	}

	span.RecordError(perr)
	span.SetStatus(codes.Error, "coverage activation publication failed")
	w.publishFailed.Add(ctx, 1)
	next := now.Add(w.activation.Backoff(c.ActivationAttempts + 1))
	slog.WarnContext(ctx, "coverage activation publication failed, it will be retried with the same event",
		"acceptance_id", c.AcceptanceID, "policy_id", c.PolicyID, "attempts", c.ActivationAttempts+1,
		"next_attempt_at", next, "error", perr)
	if _, err := w.repo.RescheduleActivation(ctx, c, now, next, perr.Error()); err != nil {
		slog.ErrorContext(ctx, "could not schedule the next coverage activation attempt", "acceptance_id", c.AcceptanceID,
			"error", err)
	}
}
