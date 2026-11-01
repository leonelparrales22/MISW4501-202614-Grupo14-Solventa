// Package push sends the pushes of the notification center (HU-M3.1-01) with
// pkg/idempotency: each delivery is claimed by one worker, sent with the same
// key on every attempt and retried with a growing wait until the provider
// accepts it or its window of 24 h ends (criterion 7). The notification stays
// in the center whatever happens to its push.
package push

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/idempotency"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/resilience"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/notifications/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/notifications/internal/ports"
)

// Defaults of the worker. The first attempt goes out as soon as the payout is
// recorded (Wake) and the retries start at 1 s, so a provider that recovers
// within a few attempts still meets the 60 s of criterion 1.
const (
	DefaultPollInterval = time.Second
	DefaultBatch        = 50
	// DefaultLease is longer than the 2 s timeout of one call to the provider.
	DefaultLease     = 10 * time.Second
	DefaultRetryBase = time.Second
	DefaultRetryMax  = time.Minute
)

// Config configures a Worker; zero values take the defaults above.
type Config struct {
	PollInterval time.Duration
	Batch        int
	Lease        time.Duration
	RetryBase    time.Duration
	RetryMax     time.Duration
	// Now is the clock; nil uses time.Now.
	Now func() time.Time
}

// Worker sends the due deliveries.
type Worker struct {
	store  ports.DeliveryStore
	sender ports.PushSender
	runner *idempotency.Runner[domain.Delivery]
	poll   time.Duration
	batch  int
	now    func() time.Time
	wake   chan struct{}
}

// NewWorker builds the worker.
func NewWorker(store ports.DeliveryStore, sender ports.PushSender, cfg Config) (*Worker, error) {
	if store == nil || sender == nil {
		return nil, errors.New("push: the worker needs a store and a sender")
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = DefaultPollInterval
	}
	if cfg.Batch <= 0 {
		cfg.Batch = DefaultBatch
	}
	if cfg.Lease <= 0 {
		cfg.Lease = DefaultLease
	}
	if cfg.RetryBase <= 0 {
		cfg.RetryBase = DefaultRetryBase
	}
	if cfg.RetryMax <= 0 {
		cfg.RetryMax = DefaultRetryMax
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	runner, err := idempotency.NewRunner[domain.Delivery](store, idempotency.RunnerConfig{
		Policy: resilience.RetryPolicy{Base: cfg.RetryBase, Max: cfg.RetryMax},
		Lease:  cfg.Lease,
		Now:    cfg.Now,
	})
	if err != nil {
		return nil, err
	}
	return &Worker{store: store, sender: sender, runner: runner, poll: cfg.PollInterval, batch: cfg.Batch, now: cfg.Now,
		wake: make(chan struct{}, 1)}, nil
}

// Wake asks the worker to look for due deliveries now. It never blocks.
func (w *Worker) Wake() {
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

// Run sends the due deliveries until ctx ends: on every poll interval and
// whenever Wake is called.
func (w *Worker) Run(ctx context.Context) {
	ticker := time.NewTicker(w.poll)
	defer ticker.Stop()
	for {
		if _, err := w.ProcessOnce(ctx); err != nil && ctx.Err() == nil {
			slog.ErrorContext(ctx, "could not process the pushes", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-w.wake:
		}
	}
}

// ProcessOnce sends the deliveries due now, at most one batch, and returns
// how many this worker claimed. An error is about the store; a failure of the
// provider is recorded on the delivery.
func (w *Worker) ProcessOnce(ctx context.Context) (int, error) {
	ids, err := w.store.Due(ctx, w.now().UTC(), w.batch)
	if err != nil {
		return 0, err
	}
	claimed := 0
	var firstErr error
	for _, id := range ids {
		if ctx.Err() != nil {
			break
		}
		rep, err := w.runner.Process(ctx, id, w.sender.Send)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if !rep.Claimed {
			continue
		}
		claimed++
		w.log(ctx, rep)
	}
	return claimed, firstErr
}

func (w *Worker) log(ctx context.Context, rep idempotency.Report[domain.Delivery]) {
	d := rep.Record
	attrs := []any{"delivery_id", d.ID, "notification_id", d.NotificationID, "installation_id", d.InstallationID,
		"attempts", d.FailedAttempts}
	switch rep.Status {
	case idempotency.StatusConfirmed:
		// The latency of criterion 1: from the payment confirmation to the
		// provider accepting the push.
		if p := d.Notification.Payout; p != nil {
			attrs = append(attrs, "latency_ms", w.now().Sub(p.PaidAt).Milliseconds())
		}
		slog.InfoContext(ctx, "push delivered", append(attrs, "reference", d.Reference)...)
	case idempotency.StatusFailed:
		slog.WarnContext(ctx, "push not delivered; the notification stays in the center",
			append(attrs, "reason", rep.Reason, "error", rep.CallErr)...)
	default:
		slog.InfoContext(ctx, "push will be retried", append(attrs, "next_attempt_at", d.NextAttemptAt, "error", rep.CallErr)...)
	}
}
