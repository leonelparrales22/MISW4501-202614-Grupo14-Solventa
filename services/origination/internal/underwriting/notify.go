package underwriting

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
)

// Defaults of the customer notice of a human decision. They are the values of
// the issuance notification of HU-W1.3-03 (issuance.DefaultContract*), so both
// e-mails to the insured retry the same way.
const (
	DefaultNotificationPollInterval   = 500 * time.Millisecond
	DefaultNotificationAttemptTimeout = 10 * time.Second
	DefaultNotificationRetryBase      = time.Second
	DefaultNotificationRetryMax       = 15 * time.Second
	DefaultNotificationMaxAttempts    = 12
	notificationBatchSize             = 50
)

// errNoCustomerEmail marks a notice that can never be sent.
var errNoCustomerEmail = errors.New("the customer has no e-mail address")

// NotificationConfig tunes the customer notice. Zero values take the defaults.
type NotificationConfig struct {
	// PollInterval is how often due notices are looked up (a decision wakes
	// the worker at once).
	PollInterval time.Duration
	// AttemptTimeout bounds one send.
	AttemptTimeout time.Duration
	// RetryBase is the wait after the first failed send; it doubles after each
	// failure up to RetryMax.
	RetryBase time.Duration
	RetryMax  time.Duration
	// MaxAttempts is how many failed sends a notice gets before it is marked
	// failed. The decision never changes: the customer still sees it when
	// they come back to the flow (E6b, E6c).
	MaxAttempts int
}

func (c NotificationConfig) withDefaults() NotificationConfig {
	if c.PollInterval <= 0 {
		c.PollInterval = DefaultNotificationPollInterval
	}
	if c.AttemptTimeout <= 0 {
		c.AttemptTimeout = DefaultNotificationAttemptTimeout
	}
	if c.RetryBase <= 0 {
		c.RetryBase = DefaultNotificationRetryBase
	}
	if c.RetryMax < c.RetryBase {
		c.RetryMax = max(DefaultNotificationRetryMax, c.RetryBase)
	}
	if c.MaxAttempts <= 0 {
		c.MaxAttempts = DefaultNotificationMaxAttempts
	}
	return c
}

// ReviewNotificationWorker sends the scheduled customer notices of the adjust
// and reject decisions (decision D-3 c), outside the advisor request. A failed
// send is retried with a growing wait and the same idempotency key, so a retry
// never duplicates the message.
type ReviewNotificationWorker struct {
	reviews  ports.ReviewRepository
	notifier ports.ReviewOutcomeNotifier
	clock    ports.Clock
	cfg      NotificationConfig
	wake     chan struct{}
}

// NewReviewNotificationWorker builds a ReviewNotificationWorker.
func NewReviewNotificationWorker(reviews ports.ReviewRepository, notifier ports.ReviewOutcomeNotifier, clock ports.Clock,
	cfg NotificationConfig) *ReviewNotificationWorker {
	return &ReviewNotificationWorker{
		reviews: reviews, notifier: notifier, clock: clock, cfg: cfg.withDefaults(), wake: make(chan struct{}, 1),
	}
}

// Notify asks Run to look for due notices now. It never blocks.
func (w *ReviewNotificationWorker) Notify() {
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

// Run sends the due notices every PollInterval and on Notify until ctx ends.
func (w *ReviewNotificationWorker) Run(ctx context.Context) {
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

// ProcessDue runs one send for each notice that is due and returns how many
// it attempted.
func (w *ReviewNotificationWorker) ProcessDue(ctx context.Context) int {
	due, err := w.reviews.ListDueNotifications(ctx, w.clock.Now().UTC(), notificationBatchSize)
	if err != nil {
		slog.ErrorContext(ctx, "could not list the due review notices", "error", err)
		return 0
	}
	for _, c := range due {
		if ctx.Err() != nil {
			break
		}
		w.send(ctx, c)
	}
	return len(due)
}

func (w *ReviewNotificationWorker) send(ctx context.Context, c domain.ReviewCase) {
	id := c.Request.AcceptanceID
	n := *c.Notification
	err := w.deliver(ctx, c)
	now := w.clock.Now().UTC()
	if err == nil {
		n.Status, n.SentAt, n.NextAttemptAt = domain.NotificationSent, now, time.Time{}
		slog.InfoContext(ctx, "review outcome notice sent", "acceptance_id", id, "outcome", c.Decision.Type,
			"channel", n.Channel, "attempts", n.Attempts)
	} else {
		n.Attempts++
		n.LastFailureAt, n.LastFailure = now, err.Error()
		exhausted := n.Attempts >= w.cfg.MaxAttempts || errors.Is(err, errNoCustomerEmail)
		if exhausted {
			n.Status, n.NextAttemptAt = domain.NotificationFailed, time.Time{}
		} else {
			n.NextAttemptAt = now.Add(w.backoff(n.Attempts))
		}
		slog.WarnContext(ctx, "review outcome notice failed", "acceptance_id", id, "attempts", n.Attempts,
			"exhausted", exhausted, "next_attempt_at", n.NextAttemptAt, "error", err)
	}
	if err := w.reviews.UpdateNotification(ctx, id, n); err != nil {
		// The notice stays due, so the next poll sends it again with the same key.
		slog.ErrorContext(ctx, "could not record the review notice", "acceptance_id", id, "error", err)
	}
}

// deliver sends the notice of the decided case to the customer.
func (w *ReviewNotificationWorker) deliver(ctx context.Context, c domain.ReviewCase) error {
	d := c.Decision
	if d == nil || d.Type == domain.DecisionAccept {
		return errors.New("the case has no adjust or reject decision")
	}
	if c.Request.Applicant.Email == "" {
		return errNoCustomerEmail
	}
	actx, cancel := context.WithTimeout(ctx, w.cfg.AttemptTimeout)
	defer cancel()
	notice := domain.ReviewOutcomeNotice{
		To:             c.Request.Applicant.Email,
		CustomerName:   c.Request.Applicant.FullName,
		AcceptanceID:   c.Request.AcceptanceID,
		Outcome:        d.Type,
		DecidedAt:      d.DecidedAt,
		IdempotencyKey: domain.ReviewOutcomeIdempotencyKey(c.Request.AcceptanceID),
	}
	if d.AdjustedOffer != nil {
		o := *d.AdjustedOffer
		notice.AdjustedOffer = &o
	}
	return w.notifier.NotifyReviewOutcome(actx, notice)
}

// backoff returns the wait before the next send after the given number of
// failed sends: RetryBase doubled per failure, capped at RetryMax.
func (w *ReviewNotificationWorker) backoff(attempts int) time.Duration {
	wait := w.cfg.RetryBase
	for i := 1; i < attempts && wait < w.cfg.RetryMax; i++ {
		wait *= 2
	}
	return min(wait, w.cfg.RetryMax)
}
