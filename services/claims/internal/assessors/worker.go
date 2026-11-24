package assessors

import (
	"context"
	"log/slog"
	"time"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/ports"
)

// Defaults of the worker.
const (
	// DefaultRetryInterval is how often the claims pending assignment are
	// searched again (assumption S8).
	DefaultRetryInterval = 300 * time.Second
	// DefaultNoticePoll is how often the due notices are looked up (an
	// assignment wakes the worker at once).
	DefaultNoticePoll = 5 * time.Second
	// DefaultNoticeLease is how long a task holds the notices it is sending.
	// It must be longer than the timeout of the e-mail provider (700 ms).
	DefaultNoticeLease = time.Minute
	batchSize          = 50
)

// DefaultNoticeBackoff is the wait after each failed delivery of a notice:
// 5 s, 30 s, 2 min, and then every 10 min.
var DefaultNoticeBackoff = []time.Duration{5 * time.Second, 30 * time.Second, 2 * time.Minute, 10 * time.Minute}

// WorkerConfig tunes the worker. Zero values take the defaults.
type WorkerConfig struct {
	RetryInterval time.Duration
	NoticePoll    time.Duration
	NoticeLease   time.Duration
	NoticeBackoff []time.Duration
}

func (c WorkerConfig) withDefaults() WorkerConfig {
	if c.RetryInterval <= 0 {
		c.RetryInterval = DefaultRetryInterval
	}
	if c.NoticePoll <= 0 {
		c.NoticePoll = DefaultNoticePoll
	}
	if c.NoticeLease <= 0 {
		c.NoticeLease = DefaultNoticeLease
	}
	if len(c.NoticeBackoff) == 0 {
		c.NoticeBackoff = DefaultNoticeBackoff
	}
	return c
}

// backoff is the wait after the failed delivery number failures (from 1).
func (c WorkerConfig) backoff(failures int) time.Duration {
	return c.NoticeBackoff[min(max(failures, 1), len(c.NoticeBackoff))-1]
}

// Retrier searches again the provider of a claim pending assignment.
type Retrier interface {
	Retry(ctx context.Context, claimID string) (domain.AssignmentState, error)
}

// Worker searches again the claims pending assignment and sends the notices
// to the assigned providers. Several claims tasks may run it at once: a
// search that loses its place is not recorded twice, a notice is held by the
// task that sends it, and the e-mail provider delivers it once per key.
type Worker struct {
	retrier Retrier
	store   ports.AssignmentStore
	mailer  ports.Mailer
	clock   ports.Clock
	cfg     WorkerConfig
	wake    chan struct{}
}

// NewWorker builds the worker. mailer may be nil: then the notices wait until
// one is configured.
func NewWorker(retrier Retrier, store ports.AssignmentStore, mailer ports.Mailer, clock ports.Clock,
	cfg WorkerConfig) *Worker {
	return &Worker{retrier: retrier, store: store, mailer: mailer, clock: clock, cfg: cfg.withDefaults(),
		wake: make(chan struct{}, 1)}
}

// RetryInterval is how often the worker searches again.
func (w *Worker) RetryInterval() time.Duration { return w.cfg.RetryInterval }

// Notify asks Run to send the due notices now. It never blocks.
func (w *Worker) Notify() {
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

// Run searches again every RetryInterval and sends the due notices every
// NoticePoll and on Notify, until ctx ends.
func (w *Worker) Run(ctx context.Context) {
	retry := time.NewTicker(w.cfg.RetryInterval)
	defer retry.Stop()
	notices := time.NewTicker(w.cfg.NoticePoll)
	defer notices.Stop()
	w.RetryPending(ctx)
	w.SendDue(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-retry.C:
			w.RetryPending(ctx)
		case <-notices.C:
			w.SendDue(ctx)
		case <-w.wake:
			w.SendDue(ctx)
		}
	}
}

// RetryPending searches again the claims pending assignment and returns how
// many it looked at. A claim that fails is logged and the others go on.
func (w *Worker) RetryPending(ctx context.Context) int {
	ids, err := w.store.PendingAssignment(ctx, batchSize)
	if err != nil {
		slog.ErrorContext(ctx, "could not list the claims pending assignment", "error", err)
		return 0
	}
	for _, id := range ids {
		if ctx.Err() != nil {
			break
		}
		state, err := w.retrier.Retry(ctx, id)
		switch {
		case err != nil:
			slog.ErrorContext(ctx, "could not search again the provider of a claim pending assignment",
				"claim_id", id, "error", err)
		case state == domain.AssignmentProposed:
			slog.InfoContext(ctx, "a provider was found for a claim pending assignment; the analyst confirms it",
				"claim_id", id)
		}
	}
	return len(ids)
}

// SendDue sends the due notices to the assigned providers and returns how
// many it tried. Without a mailer they wait.
func (w *Worker) SendDue(ctx context.Context) int {
	if w.mailer == nil {
		return 0
	}
	notices, err := w.store.ClaimNoticesDue(ctx, w.clock.Now().UTC(), w.cfg.NoticeLease, batchSize)
	if err != nil {
		slog.ErrorContext(ctx, "could not list the due notices to the providers", "error", err)
		return 0
	}
	for _, n := range notices {
		if ctx.Err() != nil {
			break
		}
		w.send(ctx, n)
	}
	return len(notices)
}

// send delivers one notice. The log names the claim, never the address.
func (w *Worker) send(ctx context.Context, n ports.ProviderNotice) {
	err := w.mailer.Send(ctx, n.To, n.Subject, n.Body, domain.AssignmentNoticeKey(n.ClaimID))
	now := w.clock.Now().UTC()
	if err == nil {
		if err := w.store.NoticeSent(ctx, n.ClaimID, now); err != nil {
			slog.ErrorContext(ctx, "could not record the notice to the provider as sent", "claim_id", n.ClaimID,
				"error", err)
		}
		return
	}
	next := now.Add(w.cfg.backoff(n.Attempts + 1))
	slog.WarnContext(ctx, "could not send the notice to the provider, it is retried", "claim_id", n.ClaimID,
		"attempts", n.Attempts+1, "next_attempt_at", next, "error", err)
	if err := w.store.NoticeFailed(ctx, n.ClaimID, next, err.Error()); err != nil {
		slog.ErrorContext(ctx, "could not record the failed notice to the provider", "claim_id", n.ClaimID,
			"error", err)
	}
}
