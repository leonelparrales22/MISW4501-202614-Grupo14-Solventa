package memory

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/outbox"
)

// Outbox is an in-memory outbox.Store, the one origination uses without
// DATABASE_URL (HU-M1.3-01): the events are lost on restart, like the rest of
// the in-memory data. It keeps the invariants of the contract: a message with
// a live lease is never claimed again, and a lost claim cannot write.
type Outbox struct {
	mu   sync.Mutex
	rows map[string]*outboxRow
	seq  int
}

type outboxRow struct {
	eventID, eventType string
	body               []byte
	order              int
	attempts           int
	nextAttemptAt      time.Time
	claimedUntil       time.Time
	publishedAt        time.Time
	lastError          string
}

var _ outbox.Store = (*Outbox)(nil)

// NewOutbox builds an empty Outbox.
func NewOutbox() *Outbox { return &Outbox{rows: map[string]*outboxRow{}} }

// Add writes an encoded envelope due at dueAt. An event id already present is
// ignored, as the primary key of the PostgreSQL table does.
func (o *Outbox) Add(eventID, eventType string, body []byte, dueAt time.Time) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if _, ok := o.rows[eventID]; ok {
		return
	}
	o.seq++
	o.rows[eventID] = &outboxRow{eventID: eventID, eventType: eventType, body: append([]byte(nil), body...),
		order: o.seq, nextAttemptAt: dueAt}
}

// Claim implements outbox.Store.
func (o *Outbox) Claim(_ context.Context, now time.Time, lease time.Duration, limit int) ([]outbox.Message, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	var due []*outboxRow
	for _, r := range o.rows {
		if r.publishedAt.IsZero() && !r.nextAttemptAt.After(now) && (r.claimedUntil.IsZero() || r.claimedUntil.Before(now)) {
			due = append(due, r)
		}
	}
	sort.Slice(due, func(i, j int) bool { return due[i].order < due[j].order })
	if len(due) > limit {
		due = due[:limit]
	}
	out := make([]outbox.Message, 0, len(due))
	for _, r := range due {
		r.claimedUntil = now.Add(lease)
		r.attempts++
		out = append(out, outbox.Message{EventID: r.eventID, EventType: r.eventType,
			Body: append([]byte(nil), r.body...), Attempts: r.attempts, ClaimedUntil: r.claimedUntil})
	}
	return out, nil
}

// MarkPublished implements outbox.Store.
func (o *Outbox) MarkPublished(_ context.Context, m outbox.Message, at time.Time) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	r, err := o.held(m)
	if err != nil {
		return err
	}
	r.publishedAt, r.claimedUntil = at, time.Time{}
	return nil
}

// Reschedule implements outbox.Store.
func (o *Outbox) Reschedule(_ context.Context, m outbox.Message, next time.Time, cause string) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	r, err := o.held(m)
	if err != nil {
		return err
	}
	r.nextAttemptAt, r.lastError, r.claimedUntil = next, cause, time.Time{}
	return nil
}

func (o *Outbox) held(m outbox.Message) (*outboxRow, error) {
	r, ok := o.rows[m.EventID]
	if !ok || !r.publishedAt.IsZero() || !r.claimedUntil.Equal(m.ClaimedUntil) {
		return nil, outbox.ErrClaimLost
	}
	return r, nil
}

// OutboxEntry is what Pending reports of a message.
type OutboxEntry struct {
	EventID   string
	EventType string
	Body      []byte
	Attempts  int
	LastError string
}

// Pending lists the messages not published yet, in the order they were added.
func (o *Outbox) Pending() []OutboxEntry {
	o.mu.Lock()
	defer o.mu.Unlock()
	rows := make([]*outboxRow, 0, len(o.rows))
	for _, r := range o.rows {
		if r.publishedAt.IsZero() {
			rows = append(rows, r)
		}
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].order < rows[j].order })
	out := make([]OutboxEntry, len(rows))
	for i, r := range rows {
		out[i] = OutboxEntry{EventID: r.eventID, EventType: r.eventType, Body: append([]byte(nil), r.body...),
			Attempts: r.attempts, LastError: r.lastError}
	}
	return out
}
