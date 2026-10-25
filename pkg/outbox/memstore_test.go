package outbox_test

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/events"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/outbox"
)

// row is one event of memStore.
type row struct {
	eventID, eventType string
	body               []byte
	created            int
	attempts           int
	nextAttemptAt      time.Time
	claimedUntil       time.Time
	publishedAt        time.Time
	lastError          string
}

// memStore is an outbox.Store in memory with the invariants of the contract:
// a live lease is never claimed again and a lost claim cannot write.
type memStore struct {
	mu   sync.Mutex
	rows map[string]*row
	seq  int
}

func newMemStore() *memStore { return &memStore{rows: map[string]*row{}} }

// add writes an envelope as the business transaction would, due at dueAt.
func (s *memStore) add(e events.Envelope, dueAt time.Time) {
	// The helpers only add valid envelopes; an invalid one would be stored
	// with an empty body, which the relay reschedules.
	body, _ := e.Encode()
	s.addRaw(e.EventID, e.EventType, body, dueAt)
}

func (s *memStore) addRaw(id, eventType string, body []byte, dueAt time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.rows[id]; ok {
		return
	}
	s.seq++
	s.rows[id] = &row{eventID: id, eventType: eventType, body: body, created: s.seq, nextAttemptAt: dueAt}
}

func (s *memStore) Claim(_ context.Context, now time.Time, lease time.Duration, limit int) ([]outbox.Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var due []*row
	for _, r := range s.rows {
		if r.publishedAt.IsZero() && !r.nextAttemptAt.After(now) && (r.claimedUntil.IsZero() || r.claimedUntil.Before(now)) {
			due = append(due, r)
		}
	}
	sort.Slice(due, func(i, j int) bool { return due[i].created < due[j].created })
	if len(due) > limit {
		due = due[:limit]
	}
	out := make([]outbox.Message, 0, len(due))
	for _, r := range due {
		r.claimedUntil = now.Add(lease)
		r.attempts++
		out = append(out, outbox.Message{EventID: r.eventID, EventType: r.eventType, Body: r.body,
			Attempts: r.attempts, ClaimedUntil: r.claimedUntil})
	}
	return out, nil
}

func (s *memStore) held(m outbox.Message) (*row, error) {
	r, ok := s.rows[m.EventID]
	if !ok || !r.publishedAt.IsZero() || !r.claimedUntil.Equal(m.ClaimedUntil) {
		return nil, outbox.ErrClaimLost
	}
	return r, nil
}

func (s *memStore) MarkPublished(_ context.Context, m outbox.Message, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := s.held(m)
	if err != nil {
		return err
	}
	r.publishedAt, r.claimedUntil = at, time.Time{}
	return nil
}

func (s *memStore) Reschedule(_ context.Context, m outbox.Message, next time.Time, cause string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := s.held(m)
	if err != nil {
		return err
	}
	r.nextAttemptAt, r.lastError, r.claimedUntil = next, cause, time.Time{}
	return nil
}

func (s *memStore) get(id string) row {
	s.mu.Lock()
	defer s.mu.Unlock()
	return *s.rows[id]
}
