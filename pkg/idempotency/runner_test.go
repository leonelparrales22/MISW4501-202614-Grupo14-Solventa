package idempotency_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/idempotency"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/resilience"
)

// payment is the record of the fake store: the attempt plus its data.
type payment struct {
	idempotency.Attempt
	Amount    int64
	Reference string
	Reason    idempotency.FailureReason
	Cause     string
}

func (p payment) AttemptState() idempotency.Attempt { return p.Attempt }

// casStore is an in-memory Store whose Claim is a compare-and-swap under a
// mutex, like the single conditional UPDATE of a real store.
type casStore struct {
	mu   sync.Mutex
	rows map[string]payment

	claimErr, writeErr error
}

func newStore(rows ...payment) *casStore {
	s := &casStore{rows: map[string]payment{}}
	for _, r := range rows {
		s.rows[r.ID] = r
	}
	return s
}

func (s *casStore) Claim(_ context.Context, id string, now time.Time, lease time.Duration) (payment, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.claimErr != nil {
		return payment{}, false, s.claimErr
	}
	p, ok := s.rows[id]
	due := ok && ((p.Status == idempotency.StatusPending && !p.NextAttemptAt.After(now)) ||
		(p.Status == idempotency.StatusInProgress && p.ClaimedUntil.Before(now)))
	if !due {
		return payment{}, false, nil
	}
	p.Status, p.ClaimedUntil = idempotency.StatusInProgress, now.Add(lease)
	s.rows[id] = p
	return p, true, nil
}

// held returns the row if rec still holds its claim.
func (s *casStore) held(rec payment) (payment, error) {
	if s.writeErr != nil {
		return payment{}, s.writeErr
	}
	p := s.rows[rec.ID]
	if p.Status != idempotency.StatusInProgress || !p.ClaimedUntil.Equal(rec.ClaimedUntil) {
		return payment{}, idempotency.ErrClaimLost
	}
	return p, nil
}

func (s *casStore) Confirm(_ context.Context, rec payment, _ time.Time, out idempotency.Outcome) (payment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, err := s.held(rec)
	if err != nil {
		return payment{}, err
	}
	p.Status, p.Reference, p.ClaimedUntil = idempotency.StatusConfirmed, out.Reference, time.Time{}
	s.rows[p.ID] = p
	return p, nil
}

func (s *casStore) Reschedule(_ context.Context, rec payment, _ time.Time, next time.Time, cause string) (payment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, err := s.held(rec)
	if err != nil {
		return payment{}, err
	}
	p.Status, p.NextAttemptAt, p.ClaimedUntil, p.Cause = idempotency.StatusPending, next, time.Time{}, cause
	p.FailedAttempts++
	s.rows[p.ID] = p
	return p, nil
}

func (s *casStore) Fail(_ context.Context, rec payment, _ time.Time, reason idempotency.FailureReason, cause string) (payment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, err := s.held(rec)
	if err != nil {
		return payment{}, err
	}
	p.Status, p.Reason, p.Cause, p.ClaimedUntil = idempotency.StatusFailed, reason, cause, time.Time{}
	p.FailedAttempts++
	s.rows[p.ID] = p
	return p, nil
}

func (s *casStore) get(id string) payment {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rows[id]
}

// clock is a settable clock.
type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

var t0 = time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)

func policy() resilience.RetryPolicy {
	return resilience.RetryPolicy{Base: 500 * time.Millisecond, Max: time.Minute, Window: 24 * time.Hour}
}

func pending(id string) payment {
	key, _ := idempotency.Key("cobro", id)
	return payment{Attempt: idempotency.Attempt{
		ID: id, Key: key, Status: idempotency.StatusPending, NextAttemptAt: t0, WindowEndsAt: t0.Add(24 * time.Hour),
	}, Amount: 8_950_000}
}

func newRunner(t *testing.T, s *casStore, c *clock) *idempotency.Runner[payment] {
	t.Helper()
	r, err := idempotency.NewRunner[payment](s, idempotency.RunnerConfig{Policy: policy(), Lease: 30 * time.Second, Now: c.Now})
	require.NoError(t, err)
	return r
}

func TestOnlyOneOfManyConcurrentWorkersCallsTheProvider(t *testing.T) {
	for rep := 0; rep < 10; rep++ {
		s := newStore(pending("acc-1"))
		r := newRunner(t, s, &clock{now: t0})
		var calls, confirmed atomic.Int32
		var wg sync.WaitGroup
		start := make(chan struct{})
		for i := 0; i < 20; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				out, err := r.Process(context.Background(), "acc-1", func(context.Context, payment) (idempotency.Outcome, error) {
					calls.Add(1)
					time.Sleep(time.Millisecond)
					return idempotency.Outcome{Reference: "ch-1"}, nil
				})
				assert.NoError(t, err)
				if out.Claimed && out.Status == idempotency.StatusConfirmed {
					confirmed.Add(1)
				}
			}()
		}
		close(start)
		wg.Wait()

		assert.EqualValues(t, 1, calls.Load(), "repetition %d: exactly one call to the provider", rep)
		assert.EqualValues(t, 1, confirmed.Load())
		assert.Equal(t, idempotency.StatusConfirmed, s.get("acc-1").Status)
		assert.Equal(t, "ch-1", s.get("acc-1").Reference)
	}
}

func TestAnAmbiguousAnswerIsRetriedLaterWithTheSameKey(t *testing.T) {
	s := newStore(pending("acc-1"))
	c := &clock{now: t0}
	r := newRunner(t, s, c)
	var keys []string
	exec := func(err error) idempotency.Exec[payment] {
		return func(_ context.Context, p payment) (idempotency.Outcome, error) {
			keys = append(keys, p.Key)
			return idempotency.Outcome{Reference: "ch-9"}, err
		}
	}

	rep, err := r.Process(context.Background(), "acc-1", exec(fmt.Errorf("timeout: %w", idempotency.ErrAmbiguous)))

	require.NoError(t, err)
	assert.True(t, rep.Claimed)
	assert.Equal(t, idempotency.OutcomeAmbiguous, rep.Outcome)
	assert.Equal(t, idempotency.StatusPending, rep.Status)
	assert.Equal(t, 1, rep.Record.FailedAttempts)
	assert.Equal(t, t0.Add(500*time.Millisecond), rep.Record.NextAttemptAt, "the first wait is the base")

	// Not due yet: nobody calls the provider.
	rep, err = r.Process(context.Background(), "acc-1", exec(nil))
	require.NoError(t, err)
	assert.False(t, rep.Claimed)

	c.advance(500 * time.Millisecond)
	rep, err = r.Process(context.Background(), "acc-1", exec(nil))
	require.NoError(t, err)
	assert.Equal(t, idempotency.StatusConfirmed, rep.Status)
	assert.Equal(t, []string{"cobro-acc-1", "cobro-acc-1"}, keys, "every attempt carries the same key")
}

func TestTheWaitGrowsWithEachFailure(t *testing.T) {
	s := newStore(pending("acc-1"))
	c := &clock{now: t0}
	r := newRunner(t, s, c)
	down := func(context.Context, payment) (idempotency.Outcome, error) {
		return idempotency.Outcome{}, idempotency.ErrAmbiguous
	}
	var waits []time.Duration
	for i := 0; i < 4; i++ {
		rep, err := r.Process(context.Background(), "acc-1", down)
		require.NoError(t, err)
		waits = append(waits, rep.Record.NextAttemptAt.Sub(c.Now()))
		c.now = rep.Record.NextAttemptAt
	}
	assert.Equal(t, []time.Duration{500 * time.Millisecond, time.Second, 2 * time.Second, 4 * time.Second}, waits)
}

func TestAnExplicitRejectionFailsTheAttemptForGood(t *testing.T) {
	s := newStore(pending("acc-1"))
	c := &clock{now: t0}
	r := newRunner(t, s, c)
	var calls int

	rep, err := r.Process(context.Background(), "acc-1", func(context.Context, payment) (idempotency.Outcome, error) {
		calls++
		return idempotency.Outcome{}, fmt.Errorf("declined: %w", idempotency.ErrRejected)
	})

	require.NoError(t, err)
	assert.Equal(t, idempotency.OutcomeRejected, rep.Outcome)
	assert.Equal(t, idempotency.StatusFailed, rep.Status)
	assert.Equal(t, idempotency.ReasonRejected, rep.Reason)
	assert.Equal(t, idempotency.ReasonRejected, s.get("acc-1").Reason)
	assert.Contains(t, s.get("acc-1").Cause, "declined")

	c.advance(time.Hour)
	rep, err = r.Process(context.Background(), "acc-1", func(context.Context, payment) (idempotency.Outcome, error) {
		calls++
		return idempotency.Outcome{}, nil
	})
	require.NoError(t, err)
	assert.False(t, rep.Claimed, "a failed attempt is never retried on its own")
	assert.Equal(t, 1, calls)
}

func TestTheWindowEndsInAFailureInsteadOfALimbo(t *testing.T) {
	p := pending("acc-1")
	p.WindowEndsAt = t0.Add(10 * time.Second)
	s := newStore(p)
	c := &clock{now: t0}
	r := newRunner(t, s, c)
	down := func(context.Context, payment) (idempotency.Outcome, error) {
		return idempotency.Outcome{}, fmt.Errorf("refused: %w", idempotency.ErrNotSent)
	}

	var last idempotency.Report[payment]
	for i := 0; i < 100 && s.get("acc-1").Status != idempotency.StatusFailed; i++ {
		var err error
		last, err = r.Process(context.Background(), "acc-1", down)
		require.NoError(t, err)
		require.True(t, last.Claimed)
		assert.Equal(t, idempotency.OutcomeNotSent, last.Outcome)
		if last.Status == idempotency.StatusPending {
			assert.False(t, last.Record.NextAttemptAt.After(p.WindowEndsAt), "no attempt after the window")
			c.now = last.Record.NextAttemptAt
		}
	}

	assert.Equal(t, idempotency.StatusFailed, last.Status)
	assert.Equal(t, idempotency.ReasonWindowExpired, last.Reason)
	assert.Equal(t, p.WindowEndsAt, c.Now(), "the last attempt ran at the end of the window")
}

func TestAnAttemptWithAnExpiredLeaseCanBeClaimedAgain(t *testing.T) {
	s := newStore(pending("acc-1"))
	c := &clock{now: t0}
	r := newRunner(t, s, c)

	// A worker claims and dies while calling (the call outlives the lease).
	stale, claimed, err := s.Claim(context.Background(), "acc-1", t0, 30*time.Second)
	require.NoError(t, err)
	require.True(t, claimed)

	rep, err := r.Process(context.Background(), "acc-1", func(context.Context, payment) (idempotency.Outcome, error) {
		return idempotency.Outcome{}, nil
	})
	require.NoError(t, err)
	assert.False(t, rep.Claimed, "the lease is still valid")

	c.advance(31 * time.Second)
	rep, err = r.Process(context.Background(), "acc-1", func(_ context.Context, p payment) (idempotency.Outcome, error) {
		assert.Equal(t, "cobro-acc-1", p.Key)
		return idempotency.Outcome{Reference: "ch-1"}, nil
	})
	require.NoError(t, err)
	assert.True(t, rep.Claimed)
	assert.Equal(t, idempotency.StatusConfirmed, rep.Status)

	// The stale worker comes back: its result is not recorded.
	_, err = s.Confirm(context.Background(), stale, c.Now(), idempotency.Outcome{Reference: "ch-other"})
	assert.ErrorIs(t, err, idempotency.ErrClaimLost)
	assert.Equal(t, "ch-1", s.get("acc-1").Reference)
}

func TestTheCallContextEndsWithTheLease(t *testing.T) {
	s := newStore(pending("acc-1"))
	r := newRunner(t, s, &clock{now: t0})

	_, err := r.Process(context.Background(), "acc-1", func(ctx context.Context, _ payment) (idempotency.Outcome, error) {
		deadline, ok := ctx.Deadline()
		assert.True(t, ok)
		assert.WithinDuration(t, time.Now().Add(30*time.Second), deadline, 5*time.Second)
		return idempotency.Outcome{}, nil
	})
	require.NoError(t, err)
}

func TestStoreErrorsAreReported(t *testing.T) {
	boom := errors.New("database down")

	s := newStore(pending("acc-1"))
	s.claimErr = boom
	_, err := newRunner(t, s, &clock{now: t0}).Process(context.Background(), "acc-1", nil)
	assert.ErrorIs(t, err, boom)

	for name, callErr := range map[string]error{
		"confirm":    nil,
		"reject":     idempotency.ErrRejected,
		"reschedule": idempotency.ErrAmbiguous,
	} {
		t.Run(name, func(t *testing.T) {
			s := newStore(pending("acc-1"))
			s.writeErr = boom
			rep, err := newRunner(t, s, &clock{now: t0}).Process(context.Background(), "acc-1",
				func(context.Context, payment) (idempotency.Outcome, error) { return idempotency.Outcome{}, callErr })
			assert.ErrorIs(t, err, boom)
			assert.True(t, rep.Claimed)
			assert.Equal(t, idempotency.StatusInProgress, rep.Status, "the claim stays until its lease expires")
		})
	}
}

func TestClassify(t *testing.T) {
	assert.Equal(t, idempotency.OutcomeConfirmed, idempotency.Classify(nil))
	assert.Equal(t, idempotency.OutcomeRejected, idempotency.Classify(fmt.Errorf("x: %w", idempotency.ErrRejected)))
	assert.Equal(t, idempotency.OutcomeNotSent, idempotency.Classify(fmt.Errorf("x: %w", idempotency.ErrNotSent)))
	assert.Equal(t, idempotency.OutcomeAmbiguous, idempotency.Classify(idempotency.ErrAmbiguous))
	assert.Equal(t, idempotency.OutcomeAmbiguous, idempotency.Classify(errors.New("unknown")), "an unknown error is retried")
}

func TestNewRunnerRejectsABadSetup(t *testing.T) {
	s := newStore()
	_, err := idempotency.NewRunner[payment](nil, idempotency.RunnerConfig{Policy: policy(), Lease: time.Second})
	assert.Error(t, err, "no store")
	_, err = idempotency.NewRunner[payment](s, idempotency.RunnerConfig{Policy: resilience.RetryPolicy{}, Lease: time.Second})
	assert.Error(t, err, "invalid policy")
	_, err = idempotency.NewRunner[payment](s, idempotency.RunnerConfig{Policy: policy()})
	assert.Error(t, err, "no lease")
	r, err := idempotency.NewRunner[payment](s, idempotency.RunnerConfig{Policy: policy(), Lease: time.Second})
	require.NoError(t, err, "nil clock uses time.Now")
	assert.NotNil(t, r)
}
