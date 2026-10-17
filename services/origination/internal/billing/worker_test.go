package billing_test

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
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/resilience"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/billing"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
)

var (
	errDown     = fmt.Errorf("connection refused: %w", idempotency.ErrAmbiguous)
	errDeclined = fmt.Errorf("declined: %w", idempotency.ErrRejected)
)

type env struct {
	repo    *memRepo
	gw      *gateway
	h       *handler
	clock   *clock
	worker  *billing.Worker
	starter *billing.Starter
}

func newEnv(t *testing.T) *env {
	t.Helper()
	e := &env{repo: newMemRepo(), h: &handler{}, clock: &clock{now: t0}}
	e.gw = &gateway{repo: e.repo}
	w, err := billing.NewWorker(e.repo, e.gw, e.clock, billing.WorkerConfig{
		ClaimLease: 30 * time.Second, Retry: resilience.DefaultRetryPolicy(),
	}, e.h)
	require.NoError(t, err)
	e.worker = w
	e.starter = billing.NewStarter(e.repo, demoTerms, e.clock, resilience.DefaultRetryPolicy(), w.Notify)
	return e
}

func (e *env) start(t *testing.T, id string) {
	t.Helper()
	require.NoError(t, e.starter.Start(context.Background(), acceptance(id), offer(id)))
}

// untilDue moves the clock to the next due moment of the charge.
func (e *env) untilDue(id string) {
	c := e.repo.get(id)
	switch {
	case c.Status == domain.ChargePending:
		e.clock.set(c.NextAttemptAt)
	case c.Status == domain.ChargeConfirmed && !c.ActivationPublished():
		e.clock.set(c.ActivationNextAttemptAt)
	}
}

func TestAConfirmedChargeActivatesTheCoverageOnce(t *testing.T) {
	e := newEnv(t)
	e.start(t, "acc-1")

	e.worker.ProcessDue(context.Background())

	c := e.repo.get("acc-1")
	assert.Equal(t, domain.ChargeConfirmed, c.Status)
	assert.Equal(t, "ch-acc-1", c.ProviderReference)
	assert.Equal(t, "pol-acc-1", c.PolicyID)
	assert.False(t, c.ActivationPublishedAt.IsZero(), "published right after the confirmation")
	require.Len(t, e.gw.calls, 1)
	assert.Equal(t, "cobro-acc-1", e.gw.calls[0].IdempotencyKey)
	assert.Zero(t, e.gw.unstored, "the gateway is only called with the charge persisted and claimed (CA-1)")
	events := e.h.received()
	require.Len(t, events, 1)
	assert.Equal(t, c.ActivationEvent(), events[0])
	assert.NoError(t, events[0].Validate())

	e.clock.advance(time.Hour)
	e.worker.ProcessDue(context.Background())
	assert.Len(t, e.h.received(), 1, "never published again")
	assert.Equal(t, 1, e.gw.count(), "never charged again")
}

func TestNoActivationWhileTheGatewayIsDownAndTheSameKeyOnEveryRetry(t *testing.T) {
	e := newEnv(t)
	e.gw.set(errDown)
	e.start(t, "acc-1")

	var waits []time.Duration
	for i := 0; i < 5; i++ {
		before := e.clock.Now()
		e.worker.ProcessDue(context.Background())
		c := e.repo.get("acc-1")
		require.Equal(t, domain.ChargePending, c.Status)
		assert.Empty(t, e.h.received(), "no activation without a confirmed payment (CA-4)")
		waits = append(waits, c.NextAttemptAt.Sub(before))
		e.untilDue("acc-1")
	}

	assert.Equal(t, 5, e.repo.get("acc-1").Attempts)
	assert.Equal(t, []time.Duration{500 * time.Millisecond, time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second}, waits,
		"growing waits (CA-2)")
	for _, call := range e.gw.calls {
		assert.Equal(t, "cobro-acc-1", call.IdempotencyKey)
	}

	e.gw.set(nil)
	e.worker.ProcessDue(context.Background())
	assert.Equal(t, domain.ChargeConfirmed, e.repo.get("acc-1").Status)
	assert.Len(t, e.h.received(), 1)
}

func TestARejectedChargeEndsInAnExceptionWithoutActivation(t *testing.T) {
	e := newEnv(t)
	e.gw.set(errDeclined)
	e.start(t, "acc-1")

	e.worker.ProcessDue(context.Background())
	e.clock.advance(time.Hour)
	e.worker.ProcessDue(context.Background())

	c := e.repo.get("acc-1")
	assert.Equal(t, domain.ChargeFailed, c.Status)
	assert.Equal(t, domain.ChargePaymentRejected, c.FailureReason)
	assert.Equal(t, 1, e.gw.count(), "a rejection is not retried")
	assert.Empty(t, e.h.received())
}

func TestTheWindowEndsInAnExceptionWithoutActivation(t *testing.T) {
	e := newEnv(t)
	e.gw.set(errDown)
	e.start(t, "acc-1")

	for i := 0; i < 2000 && e.repo.get("acc-1").Status != domain.ChargeFailed; i++ {
		e.worker.ProcessDue(context.Background())
		e.untilDue("acc-1")
	}

	c := e.repo.get("acc-1")
	assert.Equal(t, domain.ChargeFailed, c.Status)
	assert.Equal(t, domain.ChargeRetryWindowExpired, c.FailureReason)
	assert.Equal(t, t0.Add(24*time.Hour), c.LastFailureAt, "the last attempt ran at the end of the 24-hour window")
	assert.Empty(t, e.h.received(), "CA-4")
}

func TestAFailingConsumerGetsTheSameEventAgainWithGrowingWaits(t *testing.T) {
	e := newEnv(t)
	e.h.fail = 3
	e.start(t, "acc-1")

	e.worker.ProcessDue(context.Background())
	c := e.repo.get("acc-1")
	require.Equal(t, domain.ChargeConfirmed, c.Status)
	assert.False(t, c.ActivationPublished())
	assert.Equal(t, 1, c.ActivationAttempts)
	assert.Equal(t, 500*time.Millisecond, c.ActivationNextAttemptAt.Sub(e.clock.Now()))

	var waits []time.Duration
	for i := 0; i < 3; i++ {
		e.untilDue("acc-1")
		before := e.clock.Now()
		e.worker.ProcessDue(context.Background())
		if c := e.repo.get("acc-1"); !c.ActivationPublished() {
			waits = append(waits, c.ActivationNextAttemptAt.Sub(before))
		}
	}

	assert.Equal(t, []time.Duration{time.Second, 2 * time.Second}, waits)
	assert.True(t, e.repo.get("acc-1").ActivationPublished())
	events := e.h.received()
	require.Len(t, events, 4)
	for _, ev := range events[1:] {
		assert.Equal(t, events[0], ev, "exactly the same event every time")
	}
	assert.Equal(t, 1, e.gw.count(), "the payment is not repeated")
}

func TestOnlyOneOfManyWorkersChargesAndPublishes(t *testing.T) {
	repo := newMemRepo()
	gw := &gateway{repo: repo}
	h := &handler{}
	c := &clock{now: t0}
	var workers []*billing.Worker
	for i := 0; i < 5; i++ {
		w, err := billing.NewWorker(repo, gw, c, billing.WorkerConfig{}, h)
		require.NoError(t, err)
		workers = append(workers, w)
	}
	s := billing.NewStarter(repo, demoTerms, c, resilience.DefaultRetryPolicy(), nil)
	for i := 0; i < 20; i++ {
		require.NoError(t, s.Start(context.Background(), acceptance(fmt.Sprintf("acc-%02d", i)), offer(fmt.Sprintf("acc-%02d", i))))
	}

	var wg sync.WaitGroup
	for _, w := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w.ProcessDue(context.Background())
		}()
	}
	wg.Wait()

	assert.Equal(t, 20, gw.count(), "one call per charge")
	assert.Len(t, h.received(), 20, "one activation per charge")
	for id, row := range repo.rows {
		assert.Equal(t, domain.ChargeConfirmed, row.Status, id)
		assert.True(t, row.ActivationPublished(), id)
	}
}

func TestRunChargesAsSoonAsAChargeIsStarted(t *testing.T) {
	repo := newMemRepo()
	h := &handler{}
	w, err := billing.NewWorker(repo, &gateway{}, realClock{}, billing.WorkerConfig{PollInterval: time.Hour}, h)
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)
	s := billing.NewStarter(repo, demoTerms, realClock{}, resilience.DefaultRetryPolicy(), w.Notify)

	require.NoError(t, s.Start(context.Background(), acceptance("acc-1"), offer("acc-1")))

	require.Eventually(t, func() bool { return len(h.received()) == 1 }, 5*time.Second, 10*time.Millisecond,
		"Notify wakes the worker without waiting for the poll")
	w.Notify()
	w.Notify() // never blocks
}

func TestNewWorkerRequiresAConsumer(t *testing.T) {
	_, err := billing.NewWorker(newMemRepo(), &gateway{}, &clock{now: t0}, billing.WorkerConfig{})
	assert.Error(t, err, "D-16 b: a confirmed payment must always reach the contractual document")

	_, err = billing.NewWorker(newMemRepo(), &gateway{}, &clock{now: t0},
		billing.WorkerConfig{Retry: resilience.RetryPolicy{Base: time.Second}}, &handler{})
	assert.Error(t, err, "invalid retry policy")
}

func TestStoreFailuresAreLoggedAndRetriedLater(t *testing.T) {
	logs := captureLogs(t)
	e := newEnv(t)
	e.start(t, "acc-1")

	e.repo.listErr = errors.New("database down")
	assert.Zero(t, e.worker.ProcessDue(context.Background()))
	assert.Contains(t, logs.String(), "could not list the due premium charges")
	assert.Contains(t, logs.String(), "could not list the due coverage activations")

	e.repo.listErr = nil
	e.repo.writeErr = errors.New("database down")
	e.worker.ProcessDue(context.Background())
	assert.Contains(t, logs.String(), "could not record the premium charge attempt")
	assert.Equal(t, domain.ChargeInProgress, e.repo.get("acc-1").Status, "held until its lease expires")

	e.repo.writeErr = nil
	e.clock.advance(31 * time.Second)
	e.worker.ProcessDue(context.Background())
	assert.Equal(t, domain.ChargeConfirmed, e.repo.get("acc-1").Status, "claimed again after the lease (D-8)")
	assert.Equal(t, 2, e.gw.count(), "the second call reuses the key, so the gateway charges once")
	assert.Len(t, e.h.received(), 1)
}

func TestAClaimThatFailsIsLoggedAsSuchAndRetriedOnTheNextPoll(t *testing.T) {
	logs := captureLogs(t)
	e := newEnv(t)
	e.start(t, "acc-1")

	e.repo.claimErr = errors.New("database down")
	e.worker.ProcessDue(context.Background())

	assert.Contains(t, logs.String(), "could not claim the premium charge")
	assert.NotContains(t, logs.String(), "could not record the premium charge attempt")
	assert.Zero(t, e.gw.count(), "nothing is called without the claim")
	assert.Equal(t, domain.ChargePending, e.repo.get("acc-1").Status)

	e.repo.claimErr = nil
	e.worker.ProcessDue(context.Background())
	assert.Equal(t, domain.ChargeConfirmed, e.repo.get("acc-1").Status)
}

func TestAPublicationThatCannotBeRecordedIsRetriedWithTheSameEvent(t *testing.T) {
	logs := captureLogs(t)
	e := newEnv(t)
	e.h.fail = 1
	e.start(t, "acc-1")
	e.worker.ProcessDue(context.Background()) // confirmed; the consumer fails once

	e.repo.writeErr = errors.New("database down")
	e.untilDue("acc-1")
	e.worker.ProcessDue(context.Background())
	assert.Contains(t, logs.String(), "could not record the published coverage activation")

	e.repo.writeErr = nil
	e.clock.advance(31 * time.Second)
	e.worker.ProcessDue(context.Background())
	assert.True(t, e.repo.get("acc-1").ActivationPublished())
	events := e.h.received()
	require.Len(t, events, 3)
	assert.Equal(t, events[0], events[2])
}

func TestTheLogsCarryNoPersonalData(t *testing.T) {
	logs := captureLogs(t)
	e := newEnv(t)
	e.gw.set(nil, errDown)
	e.h.fail = 1
	e.start(t, "acc-1")
	for i := 0; i < 4; i++ {
		e.worker.ProcessDue(context.Background())
		e.untilDue("acc-1")
	}
	require.True(t, e.repo.get("acc-1").ActivationPublished())

	out := logs.String()
	assert.Contains(t, out, `"acceptance_id":"acc-1"`)
	assert.Contains(t, out, `"policy_id":"pol-acc-1"`)
	for _, personal := range []string{"Ana", "ana@example.com", "demo-document"} {
		assert.NotContains(t, out, personal)
	}
}

type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }
