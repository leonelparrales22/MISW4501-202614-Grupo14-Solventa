package consents

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/idempotency"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/resilience"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/audit/internal/ports"
)

// Defaults of the notice worker.
const (
	// DefaultNoticePollInterval is how often the worker looks for due notices
	// besides being woken up by a new revocation.
	DefaultNoticePollInterval = time.Second
	// DefaultNoticeBatch bounds the notices processed in one round.
	DefaultNoticeBatch = 50
	// DefaultNoticeLease is how long a claim holds a notice: longer than the
	// 700 ms timeout of one call to the entity.
	DefaultNoticeLease = 5 * time.Second
	// noticeRetryBase and noticeRetryMax are the growing wait between the
	// attempts of one notice, within its window of 5 minutes.
	noticeRetryBase = time.Second
	noticeRetryMax  = 30 * time.Second
)

// NoticeWorkerConfig configures a NoticeWorker; zero values take the
// defaults above.
type NoticeWorkerConfig struct {
	PollInterval time.Duration
	Batch        int
	Lease        time.Duration
	// RetryBase and RetryMax are the wait between the attempts of a notice.
	RetryBase time.Duration
	RetryMax  time.Duration
	// Now is the clock; nil uses time.Now.
	Now func() time.Time
}

// NoticeWorker tells every entity that a consent was revoked (HU-M1.3-02
// criterion 3) with pkg/idempotency (ADR-01, EC-D02): each notice is claimed
// by one worker, sent with the same key on every attempt and retried with a
// growing wait until the entity confirms or its window ends. Confirmed, the
// store writes revocation_confirmed; rejected or out of window,
// revocation_not_confirmed, and the consent is partially revoked. Several
// tasks of audit can run it at once: the claim lets only one of them call.
type NoticeWorker struct {
	store    ports.NoticeStore
	notifier ports.EntityNotifier
	runner   *idempotency.Runner[ports.NoticeRecord]
	poll     time.Duration
	batch    int
	now      func() time.Time
	wake     chan struct{}
}

// NewNoticeWorker builds the worker.
func NewNoticeWorker(store ports.NoticeStore, notifier ports.EntityNotifier, cfg NoticeWorkerConfig) (*NoticeWorker, error) {
	if store == nil || notifier == nil {
		return nil, errors.New("the notice worker needs a store and a notifier")
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = DefaultNoticePollInterval
	}
	if cfg.Batch <= 0 {
		cfg.Batch = DefaultNoticeBatch
	}
	if cfg.Lease <= 0 {
		cfg.Lease = DefaultNoticeLease
	}
	if cfg.RetryBase <= 0 {
		cfg.RetryBase = noticeRetryBase
	}
	if cfg.RetryMax <= 0 {
		cfg.RetryMax = noticeRetryMax
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	// The window of each notice is set when it is recorded (WindowEndsAt),
	// so the policy carries only the wait between attempts.
	runner, err := idempotency.NewRunner[ports.NoticeRecord](store, idempotency.RunnerConfig{
		Policy: resilience.RetryPolicy{Base: cfg.RetryBase, Max: cfg.RetryMax},
		Lease:  cfg.Lease,
		Now:    cfg.Now,
	})
	if err != nil {
		return nil, err
	}
	return &NoticeWorker{
		store: store, notifier: notifier, runner: runner, poll: cfg.PollInterval, batch: cfg.Batch, now: cfg.Now,
		wake: make(chan struct{}, 1),
	}, nil
}

// Wake asks the worker to look for due notices now. It never blocks.
func (w *NoticeWorker) Wake() {
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

// Run processes the due notices until ctx ends: on every poll interval and
// whenever Wake is called.
func (w *NoticeWorker) Run(ctx context.Context) {
	ticker := time.NewTicker(w.poll)
	defer ticker.Stop()
	for {
		if _, err := w.ProcessOnce(ctx); err != nil && ctx.Err() == nil {
			slog.ErrorContext(ctx, "could not process the revocation notices", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-w.wake:
		}
	}
}

// ProcessOnce processes the notices due now, at most one batch, and returns
// how many this worker claimed. An error is about the store; a failure of
// the entity is recorded on the notice.
func (w *NoticeWorker) ProcessOnce(ctx context.Context) (int, error) {
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
		rep, err := w.runner.Process(ctx, id, func(ctx context.Context, rec ports.NoticeRecord) (idempotency.Outcome, error) {
			return w.notifier.Notify(ctx, rec)
		})
		if err != nil {
			if !errors.Is(err, idempotency.ErrClaimLost) && firstErr == nil {
				firstErr = err
			}
			continue
		}
		if !rep.Claimed {
			continue
		}
		claimed++
		logNotice(ctx, rep)
	}
	return claimed, firstErr
}

// logNotice logs the result of one attempt: the consent, the entity and the
// outcome, never the holder.
func logNotice(ctx context.Context, rep idempotency.Report[ports.NoticeRecord]) {
	rec := rep.Record
	attrs := []any{"consent_id", rec.ConsentID, "entity", rec.EntityCode, "outcome", string(rep.Outcome),
		"status", string(rep.Status), "failed_attempts", rec.FailedAttempts}
	switch rep.Status {
	case idempotency.StatusConfirmed:
		slog.InfoContext(ctx, "revocation notice confirmed", attrs...)
	case idempotency.StatusFailed:
		slog.WarnContext(ctx, "revocation notice not confirmed", append(attrs, "reason", string(rep.Reason), "error", errString(rep.CallErr))...)
	default:
		slog.InfoContext(ctx, "revocation notice rescheduled", append(attrs, "next_attempt_at", rec.NextAttemptAt, "error", errString(rep.CallErr))...)
	}
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
