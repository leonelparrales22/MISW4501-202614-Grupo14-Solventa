package push_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/idempotency"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/notifications/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/notifications/internal/push"
)

var paidAt = time.Date(2026, 9, 11, 21, 40, 0, 0, time.UTC)

// memStore is an in-memory DeliveryStore with the claim protocol.
type memStore struct {
	mu      sync.Mutex
	byID    map[string]domain.Delivery
	dueErr  error
	claimed int
}

func newMemStore(ds ...domain.Delivery) *memStore {
	s := &memStore{byID: map[string]domain.Delivery{}}
	for _, d := range ds {
		s.byID[d.ID] = d
	}
	return s
}

func (s *memStore) get(id string) domain.Delivery {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.byID[id]
}

func (s *memStore) Due(_ context.Context, now time.Time, limit int) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.dueErr != nil {
		return nil, s.dueErr
	}
	var ids []string
	for id, d := range s.byID {
		if claimable(d, now) {
			ids = append(ids, id)
		}
	}
	if len(ids) > limit {
		ids = ids[:limit]
	}
	return ids, nil
}

// claimable is the rule of the claim: pending and due, or held with an
// expired lease.
func claimable(d domain.Delivery, now time.Time) bool {
	pending := d.Status == idempotency.StatusPending && !d.NextAttemptAt.After(now)
	expired := d.Status == idempotency.StatusInProgress && d.ClaimedUntil.Before(now)
	return pending || expired
}

func (s *memStore) Claim(_ context.Context, id string, now time.Time, lease time.Duration) (domain.Delivery, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.byID[id]
	if !ok || !claimable(d, now) {
		return domain.Delivery{}, false, nil
	}
	d.Status, d.ClaimedUntil = idempotency.StatusInProgress, now.Add(lease)
	s.byID[id] = d
	s.claimed++
	return d, true, nil
}

func (s *memStore) update(rec domain.Delivery, change func(*domain.Delivery)) (domain.Delivery, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d := s.byID[rec.ID]
	if d.Status != idempotency.StatusInProgress || !d.ClaimedUntil.Equal(rec.ClaimedUntil) {
		return domain.Delivery{}, idempotency.ErrClaimLost
	}
	change(&d)
	s.byID[rec.ID] = d
	return d, nil
}

func (s *memStore) Confirm(_ context.Context, rec domain.Delivery, now time.Time, out idempotency.Outcome) (domain.Delivery, error) {
	return s.update(rec, func(d *domain.Delivery) {
		d.Status, d.Reference, d.SentAt = idempotency.StatusConfirmed, out.Reference, &now
	})
}

func (s *memStore) Reschedule(_ context.Context, rec domain.Delivery, _, next time.Time, cause string) (domain.Delivery, error) {
	return s.update(rec, func(d *domain.Delivery) {
		d.Status, d.NextAttemptAt, d.LastError = idempotency.StatusPending, next, cause
		d.FailedAttempts++
	})
}

func (s *memStore) Fail(_ context.Context, rec domain.Delivery, _ time.Time, _ idempotency.FailureReason, cause string) (domain.Delivery, error) {
	return s.update(rec, func(d *domain.Delivery) {
		d.Status, d.LastError = idempotency.StatusFailed, cause
		d.FailedAttempts++
	})
}

// fakeSender answers with the errors it is given, in order, then succeeds.
type fakeSender struct {
	mu    sync.Mutex
	errs  []error
	calls []string
}

func (f *fakeSender) Send(_ context.Context, d domain.Delivery) (idempotency.Outcome, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, d.Key)
	if len(f.errs) > 0 {
		err := f.errs[0]
		f.errs = f.errs[1:]
		return idempotency.Outcome{}, err
	}
	return idempotency.Outcome{Reference: fmt.Sprintf("push-%d", len(f.calls))}, nil
}

type clock struct{ now time.Time }

func (c *clock) Now() time.Time { return c.now }

func delivery(installation string, now time.Time) domain.Delivery {
	p := domain.ParametricPayout{PayoutID: "ppo-1", Subject: "sub-a", AmountMinor: 18_000_000, Currency: "COP",
		Trigger: domain.Trigger{Parameter: "flight_delay", EventReference: "AV-8421"}, PaidAt: paidAt}
	n := domain.NewPayoutNotification(p, now)
	return domain.NewDelivery(n, domain.Device{InstallationID: installation, Token: "tok-" + installation, Locale: "es"}, now)
}

func newWorker(t *testing.T, store *memStore, sender *fakeSender, c *clock) *push.Worker {
	t.Helper()
	w, err := push.NewWorker(store, sender, push.Config{Now: c.Now})
	require.NoError(t, err)
	return w
}

func TestNewWorkerNeedsAStoreAndASender(t *testing.T) {
	_, err := push.NewWorker(nil, &fakeSender{}, push.Config{})
	require.Error(t, err)
	_, err = push.NewWorker(newMemStore(), nil, push.Config{})
	require.Error(t, err)
}

func TestADueDeliveryIsSentOnceWithItsKey(t *testing.T) {
	c := &clock{now: paidAt.Add(time.Second)}
	d := delivery("inst-1", c.now)
	store, sender := newMemStore(d), &fakeSender{}
	w := newWorker(t, store, sender, c)

	n, err := w.ProcessOnce(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	again, err := w.ProcessOnce(context.Background())
	require.NoError(t, err)
	assert.Zero(t, again, "a confirmed delivery is not sent again")

	assert.Equal(t, []string{"ntf-ppo-1:inst-1"}, sender.calls)
	got := store.get(d.ID)
	assert.Equal(t, idempotency.StatusConfirmed, got.Status)
	assert.Equal(t, "push-1", got.Reference)
}

// Criterion 7: a provider failure is retried with the same key until it
// accepts the push.
func TestAProviderFailureIsRetriedWithTheSameKey(t *testing.T) {
	c := &clock{now: paidAt.Add(time.Second)}
	d := delivery("inst-1", c.now)
	store := newMemStore(d)
	sender := &fakeSender{errs: []error{fmt.Errorf("503: %w", idempotency.ErrAmbiguous), fmt.Errorf("cut: %w", idempotency.ErrAmbiguous)}}
	w := newWorker(t, store, sender, c)

	_, err := w.ProcessOnce(context.Background())
	require.NoError(t, err)
	assert.Equal(t, idempotency.StatusPending, store.get(d.ID).Status)
	assert.Equal(t, 1, store.get(d.ID).FailedAttempts)

	for i := 0; i < 2; i++ {
		c.now = c.now.Add(time.Minute)
		_, err = w.ProcessOnce(context.Background())
		require.NoError(t, err)
	}

	assert.Equal(t, idempotency.StatusConfirmed, store.get(d.ID).Status)
	assert.Equal(t, []string{d.Key, d.Key, d.Key}, sender.calls)
	assert.Less(t, c.now.Sub(paidAt), 5*time.Minute)
}

func TestARejectedPushFailsWithoutRetrying(t *testing.T) {
	c := &clock{now: paidAt.Add(time.Second)}
	d := delivery("inst-1", c.now)
	store := newMemStore(d)
	sender := &fakeSender{errs: []error{fmt.Errorf("unregistered: %w", idempotency.ErrRejected)}}
	w := newWorker(t, store, sender, c)

	_, err := w.ProcessOnce(context.Background())
	require.NoError(t, err)
	c.now = c.now.Add(time.Hour)
	_, err = w.ProcessOnce(context.Background())
	require.NoError(t, err)

	assert.Equal(t, idempotency.StatusFailed, store.get(d.ID).Status)
	assert.Len(t, sender.calls, 1)
}

func TestThePushIsGivenUpWhenItsWindowEnds(t *testing.T) {
	c := &clock{now: paidAt.Add(time.Second)}
	d := delivery("inst-1", c.now)
	store := newMemStore(d)
	sender := &fakeSender{errs: []error{idempotency.ErrAmbiguous, idempotency.ErrAmbiguous}}
	w := newWorker(t, store, sender, c)

	_, err := w.ProcessOnce(context.Background())
	require.NoError(t, err)
	c.now = c.now.Add(25 * time.Hour)
	_, err = w.ProcessOnce(context.Background())
	require.NoError(t, err)

	assert.Equal(t, idempotency.StatusFailed, store.get(d.ID).Status)
}

func TestSeveralDevicesEachGetTheirPush(t *testing.T) {
	c := &clock{now: paidAt.Add(time.Second)}
	store, sender := newMemStore(delivery("inst-1", c.now), delivery("inst-2", c.now)), &fakeSender{}
	w := newWorker(t, store, sender, c)

	n, err := w.ProcessOnce(context.Background())
	require.NoError(t, err)

	assert.Equal(t, 2, n)
	assert.ElementsMatch(t, []string{"ntf-ppo-1:inst-1", "ntf-ppo-1:inst-2"}, sender.calls)
}

func TestAStoreFailureIsReported(t *testing.T) {
	store := newMemStore()
	store.dueErr = errors.New("database down")
	w := newWorker(t, store, &fakeSender{}, &clock{now: paidAt})

	_, err := w.ProcessOnce(context.Background())

	require.Error(t, err)
}

func TestRunSendsWhenWokenAndStopsWithItsContext(t *testing.T) {
	store, sender := newMemStore(), &fakeSender{}
	w, err := push.NewWorker(store, sender, push.Config{PollInterval: time.Hour})
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { w.Run(ctx); close(done) }()

	d := delivery("inst-1", time.Now().Add(-time.Second))
	store.mu.Lock()
	store.byID[d.ID] = d
	store.mu.Unlock()
	w.Wake()
	w.Wake() // a second wake while one is pending never blocks

	require.Eventually(t, func() bool { return store.get(d.ID).Status == idempotency.StatusConfirmed },
		5*time.Second, 10*time.Millisecond)
	cancel()
	<-done
}
