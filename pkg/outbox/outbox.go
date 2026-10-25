package outbox

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/events"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/resilience"
)

// ErrClaimLost is returned by a Store when the message is no longer held by
// the claim the caller has: its lease expired and another relay claimed it, or
// it was already published.
var ErrClaimLost = errors.New("outbox: the claim on the message was lost")

// Message is one pending event as claimed from the store.
type Message struct {
	EventID   string
	EventType string
	// Body is the encoded envelope (pkg/events).
	Body []byte
	// Attempts counts the claims of the message, this one included.
	Attempts int
	// ClaimedUntil is the end of the lease of this claim; the store uses it
	// to recognize the claim in MarkPublished and Reschedule.
	ClaimedUntil time.Time
}

// Store is the outbox table of a service (see the package documentation).
type Store interface {
	// Claim takes up to limit messages that are not published, are due at now
	// and have no live lease, sets their lease to now+lease, counts the
	// attempt and returns them. It must be one atomic statement: a message
	// with a live lease is never returned to another caller.
	Claim(ctx context.Context, now time.Time, lease time.Duration, limit int) ([]Message, error)
	// MarkPublished records that the message was published at the given
	// instant, or returns ErrClaimLost.
	MarkPublished(ctx context.Context, m Message, at time.Time) error
	// Reschedule releases the claim and makes the message due again at next,
	// recording the cause of the failure, or returns ErrClaimLost.
	Reschedule(ctx context.Context, m Message, next time.Time, cause string) error
}

// Config tunes a Relay.
type Config struct {
	// Interval is how long the relay waits after a pass that found less than
	// a full batch.
	Interval time.Duration
	// Lease is how long a claim holds a message; it must be longer than
	// PublishTimeout so a publication never outlives its lease.
	Lease time.Duration
	// Batch is how many messages a pass claims.
	Batch int
	// PublishTimeout bounds one publication.
	PublishTimeout time.Duration
	// Retry is the growing wait between the attempts of a message. Its Window
	// is ignored: an event is retried until it is published.
	Retry resilience.RetryPolicy
	// Logger receives the relay logs (default slog.Default()).
	Logger *slog.Logger
}

// DefaultConfig polls every second, claims 10 messages for 30 s, gives each
// publication 5 s and retries from 500 ms doubling up to 60 s.
func DefaultConfig() Config {
	return Config{
		Interval:       time.Second,
		Lease:          30 * time.Second,
		Batch:          10,
		PublishTimeout: 5 * time.Second,
		Retry:          resilience.RetryPolicy{Base: 500 * time.Millisecond, Max: time.Minute},
	}
}

// Validate reports why the configuration cannot be used.
func (c Config) Validate() error {
	switch {
	case c.Interval <= 0:
		return errors.New("outbox: the poll interval must be positive")
	case c.Batch <= 0:
		return errors.New("outbox: the batch must be positive")
	case c.PublishTimeout <= 0:
		return errors.New("outbox: the publish timeout must be positive")
	case c.Lease <= c.PublishTimeout:
		return fmt.Errorf("outbox: the lease (%s) must be longer than the publish timeout (%s)", c.Lease, c.PublishTimeout)
	}
	if err := c.Retry.Validate(); err != nil {
		return fmt.Errorf("outbox: %w", err)
	}
	return nil
}

// Relay publishes the pending messages of a Store.
type Relay struct {
	store     Store
	publisher events.Publisher
	now       func() time.Time
	cfg       Config
	log       *slog.Logger
	sleep     func(ctx context.Context, d time.Duration) error
}

// NewRelay builds a relay. clock nil uses time.Now.
func NewRelay(store Store, publisher events.Publisher, clock func() time.Time, cfg Config) (*Relay, error) {
	if store == nil {
		return nil, errors.New("outbox: the store is required")
	}
	if publisher == nil {
		return nil, errors.New("outbox: the publisher is required")
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if clock == nil {
		clock = time.Now
	}
	log := cfg.Logger
	if log == nil {
		log = slog.Default()
	}
	return &Relay{store: store, publisher: publisher, now: clock, cfg: cfg, log: log, sleep: sleep}, nil
}

// Run publishes until ctx ends. After a full batch it goes on at once; after a
// partial one, or when the store fails, it waits Interval.
func (r *Relay) Run(ctx context.Context) {
	for ctx.Err() == nil {
		n, err := r.ProcessOnce(ctx)
		if err != nil && ctx.Err() == nil {
			r.log.WarnContext(ctx, "could not claim the pending events of the outbox", "error", err)
		}
		if err == nil && n >= r.cfg.Batch {
			continue
		}
		if r.sleep(ctx, r.cfg.Interval) != nil {
			return
		}
	}
}

// ProcessOnce claims one batch and publishes it. It returns how many messages
// it claimed; the error is only the claim's (a failed publication is
// rescheduled and logged).
func (r *Relay) ProcessOnce(ctx context.Context) (int, error) {
	msgs, err := r.store.Claim(ctx, r.now(), r.cfg.Lease, r.cfg.Batch)
	if err != nil {
		return 0, fmt.Errorf("claim the outbox: %w", err)
	}
	for _, m := range msgs {
		r.publish(ctx, m)
	}
	return len(msgs), nil
}

func (r *Relay) publish(ctx context.Context, m Message) {
	log := r.log.With("event_id", m.EventID, "event_type", m.EventType, "attempt", m.Attempts)
	e, err := events.Decode(m.Body)
	if err != nil {
		r.reschedule(ctx, log, m, err)
		return
	}
	pctx, cancel := context.WithTimeout(ctx, r.cfg.PublishTimeout)
	err = r.publisher.Publish(pctx, e)
	cancel()
	if err != nil {
		r.reschedule(ctx, log, m, err)
		return
	}
	switch err := r.store.MarkPublished(ctx, m, r.now()); {
	case errors.Is(err, ErrClaimLost):
		log.InfoContext(ctx, "the event was published but its claim was lost; another relay may publish it again")
	case err != nil:
		log.WarnContext(ctx, "the event was published but could not be marked; it will be published again", "error", err)
	default:
		log.InfoContext(ctx, "domain event published")
	}
}

func (r *Relay) reschedule(ctx context.Context, log *slog.Logger, m Message, cause error) {
	wait := r.cfg.Retry.Backoff(m.Attempts)
	next := r.now().Add(wait)
	switch err := r.store.Reschedule(ctx, m, next, cause.Error()); {
	case errors.Is(err, ErrClaimLost):
		log.InfoContext(ctx, "could not publish the event and its claim was lost", "error", cause)
	case err != nil:
		log.WarnContext(ctx, "could not publish the event nor reschedule it; it is retried when its lease expires",
			"error", cause, "reschedule_error", err)
	default:
		log.WarnContext(ctx, "could not publish the event; it stays in the outbox", "error", cause, "retry_in", wait.String())
	}
}

// sleep waits d or until ctx ends, whichever comes first.
func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
