package assessors_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/assessors"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/ports"
)

// The automatic search of the claims pending assignment and the notices to
// the assigned providers (HU-W2.3-01, criteria 5 and 6).

// delivery is one e-mail the fake provider accepted.
type delivery struct{ to, subject, body, key string }

// fakeMailer fails the first failures calls and then accepts.
type fakeMailer struct {
	mu        sync.Mutex
	failures  int
	calls     int
	delivered []delivery
}

func (m *fakeMailer) Send(_ context.Context, to, subject, body, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls++
	if m.failures > 0 {
		m.failures--
		return errors.New("email provider unavailable")
	}
	m.delivered = append(m.delivered, delivery{to: to, subject: subject, body: body, key: key})
	return nil
}

func (m *fakeMailer) sent() []delivery {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]delivery(nil), m.delivered...)
}

// assigned is a scene with cl-1 assigned to Servicios Hogar Norte.
func assigned(t *testing.T) *scene {
	t.Helper()
	s := newScene(t, mockup()...)
	s.file(t, "cl-1", domain.IncidentWaterDamage, domain.StatusInEvaluation, fromApp(8))
	s.search(t, "cl-1")
	_, err := s.svc.Assign(context.Background(), "cl-1", "prv-servicios-hogar-norte", 1, analyst)
	require.NoError(t, err)
	return s
}

func TestTheNoticeReachesTheProviderOnce(t *testing.T) {
	s := assigned(t)
	mailer := &fakeMailer{}
	w := assessors.NewWorker(s.svc, s.store, mailer, s.clock, assessors.WorkerConfig{})

	assert.Equal(t, 1, w.SendDue(context.Background()))
	sent := mailer.sent()
	require.Len(t, sent, 1)
	assert.Equal(t, "prv-servicios-hogar-norte@example.com", sent[0].to)
	assert.Equal(t, "asignacion-cl-1", sent[0].key)
	assert.Contains(t, sent[0].subject, "Asignación de siniestro SIN-")

	n := s.view(t, "cl-1").Notice
	assert.Equal(t, ports.NoticeSent, n.Status)
	assert.Equal(t, 1, n.Attempts)
	assert.Equal(t, start, n.SentAt)

	s.clock.advance(time.Hour)
	assert.Zero(t, w.SendDue(context.Background()), "a notice sent is not sent again")
	assert.Len(t, mailer.sent(), 1)
}

func TestAFailedNoticeIsRetriedWithGrowingWaits(t *testing.T) {
	s := assigned(t)
	mailer := &fakeMailer{failures: 2}
	w := assessors.NewWorker(s.svc, s.store, mailer, s.clock, assessors.WorkerConfig{})

	assert.Equal(t, 1, w.SendDue(context.Background()))
	n := s.view(t, "cl-1").Notice
	assert.Equal(t, ports.NoticePending, n.Status)
	assert.Equal(t, 1, n.Attempts)
	assert.Equal(t, start.Add(5*time.Second), n.NextAttemptAt)
	assert.Equal(t, "email provider unavailable", n.LastError)

	assert.Zero(t, w.SendDue(context.Background()), "it waits")
	s.clock.advance(5 * time.Second)
	assert.Equal(t, 1, w.SendDue(context.Background()))
	assert.Equal(t, start.Add(35*time.Second), s.view(t, "cl-1").Notice.NextAttemptAt)

	s.clock.advance(30 * time.Second)
	assert.Equal(t, 1, w.SendDue(context.Background()))
	assert.Equal(t, ports.NoticeSent, s.view(t, "cl-1").Notice.Status)
	assert.Equal(t, 3, mailer.calls)
	sent := mailer.sent()
	require.Len(t, sent, 1)
	assert.Equal(t, "asignacion-cl-1", sent[0].key, "every attempt carries the same key")
}

func TestWithoutAMailerTheNoticesWait(t *testing.T) {
	s := assigned(t)
	w := assessors.NewWorker(s.svc, s.store, nil, s.clock, assessors.WorkerConfig{})
	assert.Zero(t, w.SendDue(context.Background()))
	assert.Equal(t, ports.NoticePending, s.view(t, "cl-1").Notice.Status)

	later := assessors.NewWorker(s.svc, s.store, &fakeMailer{}, s.clock, assessors.WorkerConfig{})
	assert.Equal(t, 1, later.SendDue(context.Background()), "the notice waited until a mailer was configured")
}

func TestATaskSendingANoticeHoldsIt(t *testing.T) {
	s := assigned(t)
	held, err := s.store.ClaimNoticesDue(context.Background(), start, time.Minute, 10)
	require.NoError(t, err)
	require.Len(t, held, 1)

	w := assessors.NewWorker(s.svc, s.store, &fakeMailer{}, s.clock, assessors.WorkerConfig{})
	assert.Zero(t, w.SendDue(context.Background()), "another task holds it")
	s.clock.advance(time.Minute)
	assert.Equal(t, 1, w.SendDue(context.Background()), "the hold expired: the task that held it stopped")
}

func TestTheWorkerSearchesAgainOnlyThePendingClaims(t *testing.T) {
	s := newScene(t, unavailable(provider("prv-cerca", 1)), provider("prv-vidrios", 2, domain.SpecialtyAutoGlass))
	s.file(t, "cl-pending", domain.IncidentWaterDamage, domain.StatusInEvaluation, fromApp(8))
	s.file(t, "cl-proposed", domain.IncidentGlass, domain.StatusInEvaluation, fromApp(8))
	s.file(t, "cl-ready", domain.IncidentWaterDamage, domain.StatusInEvaluation, fromApp(8))
	s.search(t, "cl-pending")
	s.search(t, "cl-proposed")
	w := assessors.NewWorker(s.svc, s.store, nil, s.clock, assessors.WorkerConfig{})

	assert.Equal(t, 1, w.RetryPending(context.Background()))
	assert.Equal(t, 2, s.view(t, "cl-pending").LastSearch.Seq)
	assert.Equal(t, 1, s.view(t, "cl-proposed").LastSearch.Seq)
	assert.Nil(t, s.view(t, "cl-ready").LastSearch, "nobody searched it yet")

	_, err := s.providers.SetAvailability(context.Background(), "prv-cerca", domain.AvailabilityAvailable, nil,
		analyst.Email, start)
	require.NoError(t, err)
	assert.Equal(t, 1, w.RetryPending(context.Background()))
	v := s.view(t, "cl-pending")
	assert.Equal(t, domain.AssignmentProposed, v.State)
	assert.Nil(t, v.Assignment, "the worker proposes, the analyst assigns")
	assert.Zero(t, w.RetryPending(context.Background()))
}

func TestAPendingClaimWhoseLocationChangedIsNotRetried(t *testing.T) {
	s := newScene(t)
	s.file(t, "cl-1", domain.IncidentWaterDamage, domain.StatusInEvaluation)
	s.locate(t, "cl-1", incident)
	s.search(t, "cl-1")
	ids, err := s.store.PendingAssignment(context.Background(), 10)
	require.NoError(t, err)
	assert.Equal(t, []string{"cl-1"}, ids)

	s.locate(t, "cl-1", north(1))
	ids, err = s.store.PendingAssignment(context.Background(), 10)
	require.NoError(t, err)
	assert.Empty(t, ids, "it is ready: the analyst searches from the new location")
}

// fakeRetrier fails for one claim and records the others.
type fakeRetrier struct {
	failFor string
	retried []string
}

func (r *fakeRetrier) Retry(_ context.Context, claimID string) (domain.AssignmentState, error) {
	if claimID == r.failFor {
		return "", errBoom
	}
	r.retried = append(r.retried, claimID)
	return domain.AssignmentPending, nil
}

// brokenStore cannot list anything.
type brokenStore struct{ ports.AssignmentStore }

func (brokenStore) PendingAssignment(context.Context, int) ([]string, error) { return nil, errBoom }

func (brokenStore) ClaimNoticesDue(context.Context, time.Time, time.Duration, int) ([]ports.ProviderNotice, error) {
	return nil, errBoom
}

func TestAClaimThatFailsDoesNotStopTheOthers(t *testing.T) {
	s := newScene(t)
	for _, id := range []string{"cl-1", "cl-2", "cl-3"} {
		s.file(t, id, domain.IncidentWaterDamage, domain.StatusInEvaluation, fromApp(8))
		s.search(t, id)
		s.clock.advance(time.Second)
	}
	retrier := &fakeRetrier{failFor: "cl-2"}
	w := assessors.NewWorker(retrier, s.store, nil, s.clock, assessors.WorkerConfig{})
	assert.Equal(t, 3, w.RetryPending(context.Background()))
	assert.Equal(t, []string{"cl-1", "cl-3"}, retrier.retried, "oldest searched first")

	broken := assessors.NewWorker(retrier, brokenStore{s.store}, &fakeMailer{}, s.clock, assessors.WorkerConfig{})
	assert.Zero(t, broken.RetryPending(context.Background()))
	assert.Zero(t, broken.SendDue(context.Background()))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	retrier.retried = nil
	assert.Equal(t, 3, w.RetryPending(ctx))
	assert.Empty(t, retrier.retried, "it stops when its context ends")
}

func TestRunSendsOnNotifyAndStopsWithItsContext(t *testing.T) {
	s := newScene(t, mockup()...)
	s.file(t, "cl-1", domain.IncidentWaterDamage, domain.StatusInEvaluation, fromApp(8))
	mailer := &fakeMailer{}
	w := assessors.NewWorker(s.svc, s.store, mailer, s.clock,
		assessors.WorkerConfig{RetryInterval: time.Hour, NoticePoll: time.Hour})
	assert.Equal(t, time.Hour, w.RetryInterval())

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		w.Run(ctx)
		close(done)
	}()

	s.search(t, "cl-1")
	svc := assessors.NewService(s.store, s.providers, s.clock, w.Notify)
	_, err := svc.Assign(context.Background(), "cl-1", "prv-servicios-hogar-norte", 1, analyst)
	require.NoError(t, err)
	assert.Eventually(t, func() bool { return len(mailer.sent()) == 1 }, 2*time.Second, 10*time.Millisecond,
		"the assignment wakes the worker")
	w.Notify()
	w.Notify() // never blocks

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not stop with its context")
	}
}

func TestRunSearchesAgainEveryInterval(t *testing.T) {
	s := newScene(t)
	s.file(t, "cl-1", domain.IncidentWaterDamage, domain.StatusInEvaluation, fromApp(8))
	s.search(t, "cl-1")
	retrier := &countingRetrier{}
	w := assessors.NewWorker(retrier, s.store, nil, s.clock,
		assessors.WorkerConfig{RetryInterval: 20 * time.Millisecond, NoticePoll: 10 * time.Millisecond})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)
	assert.Eventually(t, func() bool { return retrier.count() >= 3 }, 2*time.Second, 5*time.Millisecond)
}

type countingRetrier struct {
	mu sync.Mutex
	n  int
}

func (r *countingRetrier) Retry(context.Context, string) (domain.AssignmentState, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.n++
	return domain.AssignmentPending, nil
}

func (r *countingRetrier) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.n
}

func TestTheWaitsBetweenTheAttemptsOfANotice(t *testing.T) {
	s := assigned(t)
	mailer := &fakeMailer{failures: 6}
	w := assessors.NewWorker(s.svc, s.store, mailer, s.clock, assessors.WorkerConfig{})
	var waits []time.Duration
	for i := 0; i < 6; i++ {
		before := s.clock.Now()
		require.Equal(t, 1, w.SendDue(context.Background()))
		next := s.view(t, "cl-1").Notice.NextAttemptAt
		waits = append(waits, next.Sub(before))
		s.clock.advance(next.Sub(before))
	}
	assert.Equal(t, []time.Duration{5 * time.Second, 30 * time.Second, 2 * time.Minute, 10 * time.Minute,
		10 * time.Minute, 10 * time.Minute}, waits)
}
