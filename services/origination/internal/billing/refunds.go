package billing

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/idempotency"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/observability"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
)

// HU-W1.6-01 (decision D-5 b): the refunds of the cancellations at the request
// of the insured are sent to the payment gateway in the background, with the
// pattern of the premium charge (EC-D02): a deterministic key per refund
// ("reembolso-" + policy id), an atomic claim before calling the gateway, and
// retries with a growing wait within the window of the charge, ending in a
// terminal status (completed or failed), never in limbo. A refund confirmed
// twice by the gateway is refunded once: the gateway honours the key.

// RefundWorker sends the refunds in process. Several workers (one per
// origination task) may run at once: the claim lets only one of them call the
// gateway for a refund.
type RefundWorker struct {
	store   ports.RefundStore
	gateway ports.RefundGateway
	clock   ports.Clock
	cfg     WorkerConfig
	runner  *idempotency.Runner[domain.Refund]
	wake    chan struct{}

	attempts metric.Int64Counter
	failed   metric.Int64Counter
}

// NewRefundWorker builds a RefundWorker. It takes the same configuration as
// the charge worker (CHARGE_POLL_INTERVAL_MS, CHARGE_CLAIM_LEASE_MS and the
// waits of CHARGE_RETRY_*); the window is set on each refund when it is
// registered.
func NewRefundWorker(store ports.RefundStore, gateway ports.RefundGateway, clock ports.Clock, cfg WorkerConfig) (*RefundWorker, error) {
	if store == nil || gateway == nil || clock == nil {
		return nil, errors.New("billing: the refunds need a store, a gateway and a clock")
	}
	cfg = cfg.withDefaults()
	runner, err := idempotency.NewRunner[domain.Refund](store, idempotency.RunnerConfig{
		Policy: cfg.Retry, Lease: cfg.ClaimLease, Now: clock.Now,
	})
	if err != nil {
		return nil, err
	}
	m := observability.Meter(instrumentation)
	attempts, _ := m.Int64Counter("refund.attempt.completed",
		metric.WithDescription("Refund attempts, by outcome (confirmed, rejected, ambiguous, not_sent)"))
	failed, _ := m.Int64Counter("refund.failed", metric.WithDescription("Refunds that ended failed, by reason"))
	return &RefundWorker{store: store, gateway: gateway, clock: clock, cfg: cfg, runner: runner,
		wake: make(chan struct{}, 1), attempts: attempts, failed: failed}, nil
}

// Notify asks Run to look for due refunds now. It never blocks.
func (w *RefundWorker) Notify() {
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

// Run sends the due refunds every PollInterval and on Notify until ctx ends.
func (w *RefundWorker) Run(ctx context.Context) {
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

// ProcessDue runs one attempt for every due refund and returns how many it
// looked at.
func (w *RefundWorker) ProcessDue(ctx context.Context) int {
	ids, err := w.store.ListDue(ctx, w.clock.Now().UTC(), batchSize)
	if err != nil {
		slog.ErrorContext(ctx, "could not list the due refunds", "error", err)
		return 0
	}
	for _, id := range ids {
		if ctx.Err() != nil {
			break
		}
		w.refund(ctx, id)
	}
	return len(ids)
}

// refund runs one attempt of the refund of the policy.
func (w *RefundWorker) refund(ctx context.Context, policyID string) {
	ctx, span := observability.Tracer(instrumentation).Start(ctx, "refund.attempt",
		trace.WithAttributes(attribute.String("policy_id", policyID)))
	defer span.End()

	rep, err := w.runner.Process(ctx, policyID, func(cctx context.Context, r domain.Refund) (idempotency.Outcome, error) {
		span.SetAttributes(attribute.Int("attempt", r.Attempts+1), attribute.String("refund.reference", r.Reference))
		ref, err := w.gateway.Refund(cctx, r)
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
		slog.ErrorContext(ctx, "could not record the attempt of a refund", "policy_id", policyID, "error", err)
		return
	}
	r := rep.Record
	switch rep.Status {
	case idempotency.StatusConfirmed:
		slog.InfoContext(ctx, "refund completed", "policy_id", policyID, "reference", r.Reference,
			"provider_reference", r.ProviderReference, "amount_minor", r.AmountMinor, "failed_attempts", r.Attempts)
	case idempotency.StatusFailed:
		w.failed.Add(ctx, 1, metric.WithAttributes(attribute.String("reason", string(rep.Reason))))
		slog.WarnContext(ctx, "refund failed", "policy_id", policyID, "reference", r.Reference, "reason", rep.Reason,
			"attempts", r.Attempts, "error", rep.CallErr)
	default:
		slog.WarnContext(ctx, "refund rescheduled", "policy_id", policyID, "reference", r.Reference,
			"outcome", rep.Outcome, "next_attempt_at", r.NextAttemptAt, "attempts", r.Attempts, "error", rep.CallErr)
	}
}
