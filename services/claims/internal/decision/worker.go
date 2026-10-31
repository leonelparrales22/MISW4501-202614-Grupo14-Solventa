package decision

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/adapters/payments"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/idempotency"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/resilience"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/ports"
)

// Defaults of the worker.
const (
	// DefaultPollInterval is how often due payouts and notices are looked up
	// (a decision wakes the worker at once).
	DefaultPollInterval = 2 * time.Second
	// DefaultClaimLease is how long a claim holds a payout; it must be longer
	// than the timeout of the gateway (700 ms).
	DefaultClaimLease = 30 * time.Second
	batchSize         = 50
)

// DefaultRetry is the wait between the automatic attempts of a payout: 2 s,
// then 4 s. With MaxAutomaticPayoutAttempts attempts, a gateway that does not
// answer turns the claim into a payment exception in seconds, which is what
// the analyst needs to act on (screen O6b), instead of the 24 h of a premium.
var DefaultRetry = resilience.RetryPolicy{Base: 2 * time.Second, Max: 30 * time.Second}

// WorkerConfig tunes the worker. Zero values take the defaults.
type WorkerConfig struct {
	PollInterval time.Duration
	ClaimLease   time.Duration
	Retry        resilience.RetryPolicy
}

func (c WorkerConfig) withDefaults() WorkerConfig {
	if c.PollInterval <= 0 {
		c.PollInterval = DefaultPollInterval
	}
	if c.ClaimLease <= 0 {
		c.ClaimLease = DefaultClaimLease
	}
	if c.Retry.Base <= 0 {
		c.Retry = DefaultRetry
	}
	return c
}

// Worker pays the due payouts and sends the due notices of rejection. Several
// claims tasks may run it at once: the atomic claim of the store lets only one
// of them call the gateway for a payout, and the e-mail provider delivers a
// notice once per key.
type Worker struct {
	store   ports.DecisionStore
	gateway ports.PayoutGateway
	mailer  ports.Mailer
	clock   ports.Clock
	cfg     WorkerConfig
	runner  *idempotency.Runner[domain.Decision]
	wake    chan struct{}
}

// NewWorker builds the worker. mailer may be nil: then the notices wait until
// one is configured.
func NewWorker(store ports.DecisionStore, gateway ports.PayoutGateway, mailer ports.Mailer, clock ports.Clock,
	cfg WorkerConfig) (*Worker, error) {
	cfg = cfg.withDefaults()
	runner, err := idempotency.NewRunner[domain.Decision](capped{store}, idempotency.RunnerConfig{
		Policy: cfg.Retry, Lease: cfg.ClaimLease, Now: clock.Now,
	})
	if err != nil {
		return nil, err
	}
	return &Worker{store: store, gateway: gateway, mailer: mailer, clock: clock, cfg: cfg, runner: runner,
		wake: make(chan struct{}, 1)}, nil
}

// Notify asks Run to look for due work now. It never blocks.
func (w *Worker) Notify() {
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

// Run processes the due payouts and notices every PollInterval and on Notify
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

// ProcessDue runs one attempt of every due payout and one delivery of every
// due notice, and returns how many it looked at.
func (w *Worker) ProcessDue(ctx context.Context) int {
	ids, err := w.store.ListPayoutsDue(ctx, w.clock.Now().UTC(), batchSize)
	if err != nil {
		slog.ErrorContext(ctx, "could not list the due payouts", "error", err)
	}
	for _, id := range ids {
		if ctx.Err() != nil {
			return len(ids)
		}
		w.pay(ctx, id)
	}
	if w.mailer == nil {
		return len(ids)
	}
	notices, err := w.store.ListNoticesDue(ctx, w.clock.Now().UTC(), batchSize)
	if err != nil {
		slog.ErrorContext(ctx, "could not list the due notices of rejection", "error", err)
	}
	for _, d := range notices {
		if ctx.Err() != nil {
			break
		}
		w.notify(ctx, d)
	}
	return len(ids) + len(notices)
}

// pay runs one attempt of the payout of a claim.
func (w *Worker) pay(ctx context.Context, claimID string) {
	rep, err := w.runner.Process(ctx, claimID, func(cctx context.Context, d domain.Decision) (idempotency.Outcome, error) {
		ref, err := w.gateway.Payout(cctx, d)
		return idempotency.Outcome{Reference: ref}, err
	})
	if err != nil {
		slog.ErrorContext(ctx, "could not record the payout attempt; it is retried when its claim expires",
			"claim_id", claimID, "claimed", rep.Claimed, "outcome", rep.Outcome, "error", err)
		return
	}
	if !rep.Claimed {
		return // another worker holds it, or it is not due anymore
	}
	p := rep.Record.Payout
	switch p.Status {
	case idempotency.StatusConfirmed:
		slog.InfoContext(ctx, "indemnity paid", "claim_id", claimID, "payout_id", p.Reference, "attempts", p.Attempts+1)
	case idempotency.StatusFailed:
		slog.WarnContext(ctx, "indemnity payout failed: payment exception", "claim_id", claimID,
			"reason", p.Failure, "attempts", p.Attempts, "error", rep.CallErr)
		w.recordGatewayCode(ctx, claimID, rep.CallErr)
	default:
		slog.WarnContext(ctx, "indemnity payout attempt failed, it is retried with the same key", "claim_id", claimID,
			"outcome", rep.Outcome, "attempts", p.Attempts, "next_attempt_at", p.NextAttemptAt, "error", rep.CallErr)
	}
}

// recordGatewayCode keeps why the gateway rejected the payout, so screen O6b
// can show it. The failure is already recorded; this only adds the code.
func (w *Worker) recordGatewayCode(ctx context.Context, claimID string, callErr error) {
	var rejected *payments.RejectedError
	if !errors.As(callErr, &rejected) || rejected.Code == "" {
		return
	}
	if err := w.store.RecordGatewayCode(ctx, claimID, rejected.Code); err != nil {
		slog.WarnContext(ctx, "could not keep the reason of the gateway; the payout is failed all the same",
			"claim_id", claimID, "error", err)
	}
}

// notify sends the notice of a rejection.
func (w *Worker) notify(ctx context.Context, d domain.Decision) {
	subject, body := RejectionNotice(d)
	err := w.mailer.Send(ctx, d.Notice.To, subject, body, domain.NoticeKey(d.ClaimID))
	now := w.clock.Now().UTC()
	if err == nil {
		if err := w.store.NoticeSent(ctx, d.ClaimID, now); err != nil {
			slog.ErrorContext(ctx, "could not record the notice of rejection as sent", "claim_id", d.ClaimID, "error", err)
		}
		return
	}
	next := now.Add(w.cfg.Retry.Backoff(d.Notice.Attempts + 1))
	slog.WarnContext(ctx, "could not send the notice of rejection, it is retried", "claim_id", d.ClaimID,
		"attempts", d.Notice.Attempts+1, "next_attempt_at", next, "error", err)
	if err := w.store.NoticeFailed(ctx, d.ClaimID, now, next, err.Error()); err != nil {
		slog.ErrorContext(ctx, "could not record the failed notice of rejection", "claim_id", d.ClaimID, "error", err)
	}
}

// RejectionNotice is the e-mail that tells the insured the claim was
// rejected and why (criterion 3).
func RejectionNotice(d domain.Decision) (subject, body string) {
	subject = fmt.Sprintf("Tu siniestro %s fue rechazado", d.FilingNumber)
	body = fmt.Sprintf("Hola,\n\nRevisamos tu siniestro %s y no fue aprobado.\n\nMotivo: %s\n\n"+
		"Puedes ver el detalle en Mis siniestros. Si tienes preguntas, responde a este correo.\n\nSolventa",
		d.FilingNumber, d.Reason)
	return subject, body
}

// capped bounds the automatic attempts of a payout by count, not by time: the
// attempt that would be rescheduled after MaxAutomaticPayoutAttempts failures
// fails instead, and the claim becomes a payment exception.
type capped struct {
	ports.DecisionStore
}

func (c capped) Reschedule(ctx context.Context, d domain.Decision, now, next time.Time, cause string) (domain.Decision, error) {
	if d.Payout != nil && d.Payout.Attempts+1 >= domain.MaxAutomaticPayoutAttempts {
		return c.Fail(ctx, d, now, idempotency.ReasonWindowExpired, cause)
	}
	return c.DecisionStore.Reschedule(ctx, d, now, next, cause)
}
