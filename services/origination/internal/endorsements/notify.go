package endorsements

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
)

// Defaults of the e-mail of a decision (decision D-12 a). They are the values
// of the notice of a human decision of HU-W1.1-02, so every e-mail to the
// insured retries the same way.
const (
	DefaultNoticePollInterval   = 500 * time.Millisecond
	DefaultNoticeAttemptTimeout = 10 * time.Second
	DefaultNoticeRetryBase      = time.Second
	DefaultNoticeRetryMax       = 15 * time.Second
	DefaultNoticeMaxAttempts    = 12
	noticeBatchSize             = 50
)

// errNoInsuredEmail marks a notice that can never be sent.
var errNoInsuredEmail = errors.New("the insured has no e-mail address")

// NoticeConfig tunes the e-mails. Zero values take the defaults.
type NoticeConfig struct {
	// PollInterval is how often due e-mails are looked up (a decision wakes
	// the worker at once).
	PollInterval time.Duration
	// AttemptTimeout bounds one send.
	AttemptTimeout time.Duration
	// RetryBase is the wait after the first failed send; it doubles after
	// each failure up to RetryMax.
	RetryBase time.Duration
	RetryMax  time.Duration
	// MaxAttempts is how many failed sends an e-mail gets before it is marked
	// failed. The decision never changes: the insured still sees it in the
	// portal (CA-5).
	MaxAttempts int
}

func (c NoticeConfig) withDefaults() NoticeConfig {
	if c.PollInterval <= 0 {
		c.PollInterval = DefaultNoticePollInterval
	}
	if c.AttemptTimeout <= 0 {
		c.AttemptTimeout = DefaultNoticeAttemptTimeout
	}
	if c.RetryBase <= 0 {
		c.RetryBase = DefaultNoticeRetryBase
	}
	if c.RetryMax < c.RetryBase {
		c.RetryMax = max(DefaultNoticeRetryMax, c.RetryBase)
	}
	if c.MaxAttempts <= 0 {
		c.MaxAttempts = DefaultNoticeMaxAttempts
	}
	return c
}

// NoticeWorker sends the e-mails of the decisions outside the request of the
// analyst. A failed send is retried with a growing wait and the same
// idempotency key, so a retry never duplicates the message.
type NoticeWorker struct {
	notices  ports.EndorsementNoticeStore
	notifier ports.EndorsementOutcomeNotifier
	clock    ports.Clock
	cfg      NoticeConfig
	wake     chan struct{}
}

// NewNoticeWorker builds a NoticeWorker.
func NewNoticeWorker(notices ports.EndorsementNoticeStore, notifier ports.EndorsementOutcomeNotifier, clock ports.Clock,
	cfg NoticeConfig) *NoticeWorker {
	return &NoticeWorker{notices: notices, notifier: notifier, clock: clock, cfg: cfg.withDefaults(), wake: make(chan struct{}, 1)}
}

// Notify asks Run to look for due e-mails now. It never blocks.
func (w *NoticeWorker) Notify() {
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

// Run sends the due e-mails every PollInterval and on Notify until ctx ends.
func (w *NoticeWorker) Run(ctx context.Context) {
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

// ProcessDue runs one send for each e-mail that is due and returns how many it
// attempted.
func (w *NoticeWorker) ProcessDue(ctx context.Context) int {
	due, err := w.notices.ListDueNotices(ctx, w.clock.Now().UTC(), noticeBatchSize)
	if err != nil {
		slog.ErrorContext(ctx, "could not list the due endorsement notices", "error", err)
		return 0
	}
	for _, n := range due {
		if ctx.Err() != nil {
			break
		}
		w.send(ctx, n)
	}
	return len(due)
}

func (w *NoticeWorker) send(ctx context.Context, n domain.EndorsementNotice) {
	d := n.Delivery
	err := w.deliver(ctx, n)
	now := w.clock.Now().UTC()
	if err == nil {
		d.Status, d.SentAt, d.NextAttemptAt = domain.NotificationSent, now, time.Time{}
		slog.InfoContext(ctx, "endorsement outcome notice sent", "request_id", n.RequestID, "outcome", string(n.Outcome),
			"attempts", d.Attempts)
	} else {
		d.Attempts++
		d.LastFailureAt, d.LastFailure = now, err.Error()
		exhausted := d.Attempts >= w.cfg.MaxAttempts || errors.Is(err, errNoInsuredEmail)
		if exhausted {
			d.Status, d.NextAttemptAt = domain.NotificationFailed, time.Time{}
		} else {
			d.NextAttemptAt = now.Add(w.backoff(d.Attempts))
		}
		slog.WarnContext(ctx, "endorsement outcome notice failed", "request_id", n.RequestID, "attempts", d.Attempts,
			"exhausted", exhausted, "next_attempt_at", d.NextAttemptAt, "error", err)
	}
	if err := w.notices.UpdateNotice(ctx, n.RequestID, d); err != nil {
		// The e-mail stays due, so the next poll sends it again with the same key.
		slog.ErrorContext(ctx, "could not record the endorsement notice", "request_id", n.RequestID, "error", err)
	}
}

func (w *NoticeWorker) deliver(ctx context.Context, n domain.EndorsementNotice) error {
	if n.To == "" {
		return errNoInsuredEmail
	}
	actx, cancel := context.WithTimeout(ctx, w.cfg.AttemptTimeout)
	defer cancel()
	return w.notifier.NotifyEndorsementOutcome(actx, n)
}

// backoff returns the wait before the next send after the given number of
// failed sends: RetryBase doubled per failure, capped at RetryMax.
func (w *NoticeWorker) backoff(attempts int) time.Duration {
	wait := w.cfg.RetryBase
	for i := 1; i < attempts && wait < w.cfg.RetryMax; i++ {
		wait *= 2
	}
	return min(wait, w.cfg.RetryMax)
}
