package assistance

import (
	"context"
	"log/slog"
	"time"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/ports"
)

// Defaults of the worker of the notices, the same as those of the notices of
// HU-W2.3-01.
const (
	// DefaultNoticePoll is how often the due notices are looked up (a
	// dispatch wakes the worker at once).
	DefaultNoticePoll = 5 * time.Second
	// DefaultNoticeLease is how long a task holds the notices it is sending.
	// It must be longer than the timeout of the e-mail provider (700 ms).
	DefaultNoticeLease = time.Minute
	noticeBatch        = 50
)

// DefaultNoticeBackoff is the wait after each failed delivery of a notice:
// 5 s, 30 s, 2 min, and then every 10 min.
var DefaultNoticeBackoff = []time.Duration{5 * time.Second, 30 * time.Second, 2 * time.Minute, 10 * time.Minute}

// NoticeConfig tunes the worker. Zero values take the defaults.
type NoticeConfig struct {
	Poll    time.Duration
	Lease   time.Duration
	Backoff []time.Duration
}

func (c NoticeConfig) withDefaults() NoticeConfig {
	if c.Poll <= 0 {
		c.Poll = DefaultNoticePoll
	}
	if c.Lease <= 0 {
		c.Lease = DefaultNoticeLease
	}
	if len(c.Backoff) == 0 {
		c.Backoff = DefaultNoticeBackoff
	}
	return c
}

// backoff is the wait after the failed delivery number failures (from 1).
func (c NoticeConfig) backoff(failures int) time.Duration {
	return c.Backoff[min(max(failures, 1), len(c.Backoff))-1]
}

// NoticeWorker sends the notices of the dispatches to the providers
// (decision D4). Several claims tasks may run it at once: a notice is held by
// the task that sends it, and the e-mail provider delivers it once per key.
type NoticeWorker struct {
	store  ports.AssistanceInboxStore
	mailer ports.Mailer
	clock  ports.Clock
	cfg    NoticeConfig
	wake   chan struct{}
}

// NewNoticeWorker builds the worker. mailer may be nil: then the notices wait
// until one is configured, and the dispatches are recorded all the same.
func NewNoticeWorker(store ports.AssistanceInboxStore, mailer ports.Mailer, clock ports.Clock,
	cfg NoticeConfig) *NoticeWorker {
	return &NoticeWorker{store: store, mailer: mailer, clock: clock, cfg: cfg.withDefaults(),
		wake: make(chan struct{}, 1)}
}

// Notify asks Run to send the due notices now. It never blocks.
func (w *NoticeWorker) Notify() {
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

// Run sends the due notices every Poll and on Notify, until ctx ends.
func (w *NoticeWorker) Run(ctx context.Context) {
	poll := time.NewTicker(w.cfg.Poll)
	defer poll.Stop()
	w.SendDue(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-poll.C:
			w.SendDue(ctx)
		case <-w.wake:
			w.SendDue(ctx)
		}
	}
}

// SendDue sends the due notices and returns how many it tried. Without a
// mailer they wait.
func (w *NoticeWorker) SendDue(ctx context.Context) int {
	if w.mailer == nil {
		return 0
	}
	notices, err := w.store.ClaimNoticesDue(ctx, w.clock.Now().UTC(), w.cfg.Lease, noticeBatch)
	if err != nil {
		slog.ErrorContext(ctx, "could not list the due notices of the dispatches", "error", err)
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

// send delivers one notice. The log names the request, never the address of
// the provider nor the location (assumption S15).
func (w *NoticeWorker) send(ctx context.Context, n ports.AssistanceNotice) {
	err := w.mailer.Send(ctx, n.To, n.Subject, n.Body, domain.AssistanceNoticeKey(n.RequestID))
	now := w.clock.Now().UTC()
	if err == nil {
		if err := w.store.NoticeSent(ctx, n.RequestID, now); err != nil {
			slog.ErrorContext(ctx, "could not record the notice of the dispatch as sent", "request_id", n.RequestID,
				"error", err)
		}
		return
	}
	next := now.Add(w.cfg.backoff(n.Attempts + 1))
	slog.WarnContext(ctx, "could not send the notice of the dispatch, it is retried", "request_id", n.RequestID,
		"attempts", n.Attempts+1, "next_attempt_at", next, "error", err)
	if err := w.store.NoticeFailed(ctx, n.RequestID, next, err.Error()); err != nil {
		slog.ErrorContext(ctx, "could not record the failed notice of the dispatch", "request_id", n.RequestID,
			"error", err)
	}
}
