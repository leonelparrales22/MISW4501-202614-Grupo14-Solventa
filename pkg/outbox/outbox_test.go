package outbox_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/events"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/outbox"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/resilience"
)

var t0 = time.Date(2026, 10, 24, 20, 0, 0, 0, time.UTC)

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// fakePublisher records what it publishes and fails while err is set.
type fakePublisher struct {
	mu        sync.Mutex
	published []events.Envelope
	err       error
}

func (p *fakePublisher) Publish(_ context.Context, e events.Envelope) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.err != nil {
		return p.err
	}
	p.published = append(p.published, e)
	return nil
}

func (p *fakePublisher) setErr(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.err = err
}

func (p *fakePublisher) ids() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]string, len(p.published))
	for i, e := range p.published {
		out[i] = e.EventID
	}
	return out
}

func linked(t testing.TB, id string) events.Envelope {
	t.Helper()
	e, err := events.New(id, "CustomerLinked", "origination", 1, t0, map[string]string{"customer_id": id})
	require.NoError(t, err)
	return e
}

func newRelay(t *testing.T, store outbox.Store, pub events.Publisher, clk *fakeClock, logs *bytes.Buffer) *outbox.Relay {
	t.Helper()
	cfg := outbox.DefaultConfig()
	cfg.Logger = slog.New(slog.NewJSONHandler(logs, nil))
	r, err := outbox.NewRelay(store, pub, clk.Now, cfg)
	require.NoError(t, err)
	return r
}

func TestProcessOncePublishesAndMarksTheEvents(t *testing.T) {
	store := newMemStore()
	store.add(linked(t, "e1"), t0)
	store.add(linked(t, "e2"), t0)
	pub := &fakePublisher{}
	clk := &fakeClock{now: t0}
	var logs bytes.Buffer

	n, err := newRelay(t, store, pub, clk, &logs).ProcessOnce(context.Background())

	require.NoError(t, err)
	assert.Equal(t, 2, n)
	assert.Equal(t, []string{"e1", "e2"}, pub.ids(), "in the order they were written")
	assert.Equal(t, t0, store.get("e1").publishedAt)
	assert.Equal(t, 1, store.get("e1").attempts)

	n, err = newRelay(t, store, pub, clk, &logs).ProcessOnce(context.Background())
	require.NoError(t, err)
	assert.Zero(t, n, "a published event is not claimed again")
}

func TestAnEventThatIsNotDueYetWaits(t *testing.T) {
	store := newMemStore()
	store.add(linked(t, "e1"), t0.Add(time.Minute))
	pub := &fakePublisher{}
	var logs bytes.Buffer

	n, err := newRelay(t, store, pub, &fakeClock{now: t0}, &logs).ProcessOnce(context.Background())

	require.NoError(t, err)
	assert.Zero(t, n)
	assert.Empty(t, pub.ids())
}

func TestAFailedPublicationIsRescheduledWithAGrowingWaitAndNoWindow(t *testing.T) {
	store := newMemStore()
	store.add(linked(t, "e1"), t0)
	pub := &fakePublisher{err: errors.New("connection refused")}
	clk := &fakeClock{now: t0}
	var logs bytes.Buffer
	relay := newRelay(t, store, pub, clk, &logs)

	var waits []time.Duration
	for range 10 {
		_, err := relay.ProcessOnce(context.Background())
		require.NoError(t, err)
		r := store.get("e1")
		require.True(t, r.publishedAt.IsZero())
		waits = append(waits, r.nextAttemptAt.Sub(clk.Now()))
		clk.advance(r.nextAttemptAt.Sub(clk.Now()))
	}

	assert.Equal(t, []time.Duration{
		500 * time.Millisecond, time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second,
		16 * time.Second, 32 * time.Second, time.Minute, time.Minute, time.Minute,
	}, waits, "500 ms doubling up to 60 s, and still retried after the tenth attempt")
	assert.Equal(t, "connection refused", store.get("e1").lastError)
	assert.Contains(t, logs.String(), "stays in the outbox")

	pub.setErr(nil)
	_, err := relay.ProcessOnce(context.Background())
	require.NoError(t, err)
	assert.Equal(t, []string{"e1"}, pub.ids(), "published once the broker answers")
	assert.Equal(t, 11, store.get("e1").attempts)
}

func TestALostClaimDoesNotOverwriteTheNewOne(t *testing.T) {
	store := newMemStore()
	store.add(linked(t, "e1"), t0)
	ctx := context.Background()

	first, err := store.Claim(ctx, t0, time.Second, 10)
	require.NoError(t, err)
	require.Len(t, first, 1)
	none, err := store.Claim(ctx, t0.Add(500*time.Millisecond), time.Second, 10)
	require.NoError(t, err)
	assert.Empty(t, none, "a live lease is not claimed again")

	second, err := store.Claim(ctx, t0.Add(2*time.Second), time.Second, 10)
	require.NoError(t, err)
	require.Len(t, second, 1, "an expired lease is claimed again")

	assert.ErrorIs(t, store.MarkPublished(ctx, first[0], t0), outbox.ErrClaimLost)
	assert.ErrorIs(t, store.Reschedule(ctx, first[0], t0, "x"), outbox.ErrClaimLost)
	require.NoError(t, store.MarkPublished(ctx, second[0], t0.Add(2*time.Second)))
}

// claimStealer hands the relay its claim and then lets another relay take the
// message, as when the lease expires during a slow publication.
type claimStealer struct {
	*memStore
}

func (s claimStealer) Claim(ctx context.Context, now time.Time, lease time.Duration, limit int) ([]outbox.Message, error) {
	msgs, err := s.memStore.Claim(ctx, now, lease, limit)
	if err == nil && len(msgs) > 0 {
		_, err = s.memStore.Claim(ctx, now.Add(lease+time.Second), lease, limit)
	}
	return msgs, err
}

func TestTheRelayToleratesALostClaim(t *testing.T) {
	store := newMemStore()
	store.add(linked(t, "e1"), t0)
	clk := &fakeClock{now: t0}
	var logs bytes.Buffer

	_, err := newRelay(t, claimStealer{memStore: store}, &fakePublisher{}, clk, &logs).ProcessOnce(context.Background())
	require.NoError(t, err)
	assert.Contains(t, logs.String(), "claim was lost")

	other := newMemStore()
	other.add(linked(t, "e2"), t0)
	_, err = newRelay(t, claimStealer{memStore: other}, &fakePublisher{err: errors.New("down")}, clk, &logs).ProcessOnce(context.Background())
	require.NoError(t, err)
	assert.Contains(t, logs.String(), "could not publish the event and its claim was lost")
}

func TestAnInvalidEnvelopeIsRescheduledWithoutBlockingTheOthers(t *testing.T) {
	store := newMemStore()
	store.addRaw("broken", "CustomerLinked", []byte(`{"event_id":"broken"}`), t0)
	store.add(linked(t, "e2"), t0)
	pub := &fakePublisher{}
	var logs bytes.Buffer

	n, err := newRelay(t, store, pub, &fakeClock{now: t0}, &logs).ProcessOnce(context.Background())

	require.NoError(t, err)
	assert.Equal(t, 2, n)
	assert.Equal(t, []string{"e2"}, pub.ids())
	broken := store.get("broken")
	assert.True(t, broken.publishedAt.IsZero(), "never lost: it stays in the outbox")
	assert.Contains(t, broken.lastError, "invalid envelope")
	assert.Equal(t, t0.Add(500*time.Millisecond), broken.nextAttemptAt)
}

// failingStore fails the operations it is told to.
type failingStore struct {
	*memStore
	claimErr, markErr, rescheduleErr error
}

func (s failingStore) Claim(ctx context.Context, now time.Time, lease time.Duration, limit int) ([]outbox.Message, error) {
	if s.claimErr != nil {
		return nil, s.claimErr
	}
	return s.memStore.Claim(ctx, now, lease, limit)
}

func (s failingStore) MarkPublished(ctx context.Context, m outbox.Message, at time.Time) error {
	if s.markErr != nil {
		return s.markErr
	}
	return s.memStore.MarkPublished(ctx, m, at)
}

func (s failingStore) Reschedule(ctx context.Context, m outbox.Message, next time.Time, cause string) error {
	if s.rescheduleErr != nil {
		return s.rescheduleErr
	}
	return s.memStore.Reschedule(ctx, m, next, cause)
}

func TestStoreFailuresAreReportedOrLogged(t *testing.T) {
	ctx := context.Background()
	var logs bytes.Buffer
	clk := &fakeClock{now: t0}

	boom := errors.New("the database is down")
	_, err := newRelay(t, failingStore{memStore: newMemStore(), claimErr: boom}, &fakePublisher{}, clk, &logs).ProcessOnce(ctx)
	assert.ErrorIs(t, err, boom)

	store := newMemStore()
	store.add(linked(t, "e1"), t0)
	_, err = newRelay(t, failingStore{memStore: store, markErr: boom}, &fakePublisher{}, clk, &logs).ProcessOnce(ctx)
	require.NoError(t, err)
	assert.Contains(t, logs.String(), "could not be marked")

	store = newMemStore()
	store.add(linked(t, "e1"), t0)
	_, err = newRelay(t, failingStore{memStore: store, rescheduleErr: boom}, &fakePublisher{err: errors.New("down")}, clk, &logs).ProcessOnce(ctx)
	require.NoError(t, err)
	assert.Contains(t, logs.String(), "nor reschedule")
}

func TestRunStopsWithTheContextAndGoesOnAfterAFullBatch(t *testing.T) {
	store := newMemStore()
	for _, id := range []string{"e01", "e02", "e03", "e04", "e05", "e06", "e07", "e08", "e09", "e10", "e11"} {
		store.add(linked(t, id), t0)
	}
	pub := &fakePublisher{}
	clk := &fakeClock{now: t0}
	var logs bytes.Buffer
	relay := newRelay(t, failingStore{memStore: store}, pub, clk, &logs)
	ctx, cancel := context.WithCancel(context.Background())
	sleeps := 0
	relay.SetSleep(func(context.Context, time.Duration) error {
		sleeps++
		cancel()
		return context.Canceled
	})

	done := make(chan struct{})
	go func() { relay.Run(ctx); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not stop when the context ended")
	}

	assert.Len(t, pub.ids(), 11, "the second batch runs right after the full first one")
	assert.Equal(t, 1, sleeps, "it only waits after the partial batch")
}

func TestRunLogsAClaimFailureAndWaits(t *testing.T) {
	var logs bytes.Buffer
	relay := newRelay(t, failingStore{memStore: newMemStore(), claimErr: errors.New("db down")}, &fakePublisher{}, &fakeClock{now: t0}, &logs)
	ctx, cancel := context.WithCancel(context.Background())
	relay.SetSleep(func(context.Context, time.Duration) error { cancel(); return context.Canceled })

	relay.Run(ctx)

	assert.Contains(t, logs.String(), "could not claim")
}

func TestNewRelayValidatesItsArguments(t *testing.T) {
	store, pub := newMemStore(), &fakePublisher{}
	_, err := outbox.NewRelay(nil, pub, nil, outbox.DefaultConfig())
	assert.Error(t, err)
	_, err = outbox.NewRelay(store, nil, nil, outbox.DefaultConfig())
	assert.Error(t, err)
	r, err := outbox.NewRelay(store, pub, nil, outbox.DefaultConfig())
	require.NoError(t, err, "a nil clock uses time.Now")
	assert.NotNil(t, r)

	for name, breakIt := range map[string]func(*outbox.Config){
		"interval":     func(c *outbox.Config) { c.Interval = 0 },
		"batch":        func(c *outbox.Config) { c.Batch = 0 },
		"timeout":      func(c *outbox.Config) { c.PublishTimeout = 0 },
		"short lease":  func(c *outbox.Config) { c.Lease = c.PublishTimeout },
		"retry policy": func(c *outbox.Config) { c.Retry = resilience.RetryPolicy{} },
	} {
		t.Run(name, func(t *testing.T) {
			cfg := outbox.DefaultConfig()
			breakIt(&cfg)
			_, err := outbox.NewRelay(store, pub, nil, cfg)
			assert.Error(t, err)
		})
	}
}
