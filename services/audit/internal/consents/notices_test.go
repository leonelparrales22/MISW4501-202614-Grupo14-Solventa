package consents_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/idempotency"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/audit/internal/consents"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/audit/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/audit/internal/ports"
)

// The notice worker (HU-M1.3-02 criterion 3): one call per attempt, the same
// key on every attempt, confirmed or not confirmed at the end of the window.

// fakeNotifier answers with the errors queued, then confirms.
type fakeNotifier struct {
	mu    sync.Mutex
	errs  []error
	keys  []string
	calls atomic.Int32
	block chan struct{}
}

func (f *fakeNotifier) Notify(_ context.Context, rec ports.NoticeRecord) (idempotency.Outcome, error) {
	f.calls.Add(1)
	if f.block != nil {
		<-f.block
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.keys = append(f.keys, rec.Key)
	if len(f.errs) > 0 {
		err := f.errs[0]
		f.errs = f.errs[1:]
		return idempotency.Outcome{}, err
	}
	return idempotency.Outcome{Reference: "of-rev-1"}, nil
}

type workerFixture struct {
	*revocationFixture
	notifier *fakeNotifier
	worker   *consents.NoticeWorker
}

func newWorkerFixture(t *testing.T, errs ...error) *workerFixture {
	t.Helper()
	f := &workerFixture{revocationFixture: newRevocationFixture(t), notifier: &fakeNotifier{errs: errs}}
	w, err := consents.NewNoticeWorker(f.notices, f.notifier, consents.NoticeWorkerConfig{Now: f.clock.Now})
	require.NoError(t, err)
	f.worker = w
	_, _, err = f.svc.RevokeAsHolder(context.Background(), "1020304050", "sub-a", "cns-a1", "req-1", holderRequest(f.clock.Now()))
	require.NoError(t, err)
	return f
}

func (f *workerFixture) view(t *testing.T) domain.ConsentView {
	t.Helper()
	c, err := f.consents.Get(context.Background(), "cns-a1")
	require.NoError(t, err)
	byConsent, err := f.events.ListByConsents(context.Background(), []string{"cns-a1"})
	require.NoError(t, err)
	return domain.View(c, byConsent["cns-a1"], f.clock.Now(), nil)
}

const noticeA1 = "cns-a1/aliado-banco-1"

func TestTheWorkerConfirmsTheNoticeOnTheFirstAttempt(t *testing.T) {
	f := newWorkerFixture(t)

	claimed, err := f.worker.ProcessOnce(context.Background())

	require.NoError(t, err)
	assert.Equal(t, 1, claimed)
	assert.Equal(t, []string{"revocation-cns-a1-aliado-banco-1"}, f.notifier.keys)
	assert.Equal(t, idempotency.StatusConfirmed, f.notices.get(noticeA1).Status)
	v := f.view(t)
	assert.Equal(t, domain.StatusRevoked, v.Status)
	assert.Equal(t, domain.NoticeCompleted, v.Revocation.Summary)
	assert.Equal(t, domain.NoticeConfirmed, v.Revocation.Entities[0].Status)
	assert.Equal(t, "of-rev-1", f.notices.get(noticeA1).Reference)
}

func TestTheWorkerRetriesWithTheSameKeyUntilTheEntityConfirms(t *testing.T) {
	ambiguous := errors.Join(idempotency.ErrAmbiguous, errors.New("status 503"))
	f := newWorkerFixture(t, ambiguous, ambiguous, idempotency.ErrNotSent)
	ctx := context.Background()

	for round := 0; round < 4; round++ {
		_, err := f.worker.ProcessOnce(ctx)
		require.NoError(t, err)
		f.clock.advance(30 * time.Second)
	}

	assert.EqualValues(t, 4, f.notifier.calls.Load(), "three failures and the confirmation")
	for _, key := range f.notifier.keys {
		assert.Equal(t, "revocation-cns-a1-aliado-banco-1", key, "the same key on every attempt")
	}
	assert.Equal(t, idempotency.StatusConfirmed, f.notices.get(noticeA1).Status)
	assert.Equal(t, 3, f.notices.get(noticeA1).FailedAttempts)
	assert.Equal(t, domain.StatusRevoked, f.view(t).Status)
}

func TestTheWorkerMarksTheNoticeNotConfirmedWhenTheWindowEnds(t *testing.T) {
	var errs []error
	for i := 0; i < 50; i++ {
		errs = append(errs, idempotency.ErrAmbiguous)
	}
	f := newWorkerFixture(t, errs...)
	ctx := context.Background()

	for elapsed := time.Duration(0); elapsed <= domain.RevocationNoticeDeadline+time.Minute; elapsed += 20 * time.Second {
		_, err := f.worker.ProcessOnce(ctx)
		require.NoError(t, err)
		f.clock.advance(20 * time.Second)
	}

	assert.Equal(t, idempotency.StatusFailed, f.notices.get(noticeA1).Status)
	v := f.view(t)
	assert.Equal(t, domain.StatusPartiallyRevoked, v.Status, "the entity did not confirm in 5 minutes")
	assert.Equal(t, domain.NoticePartial, v.Revocation.Summary)
	assert.Contains(t, f.events.statuses("cns-a1"), "revocation_not_confirmed|aliado-banco-1")
	assert.Less(t, f.notifier.calls.Load(), int32(50), "the waits grow")
}

func TestTheWorkerFailsARejectedNoticeAtOnce(t *testing.T) {
	f := newWorkerFixture(t, errors.Join(idempotency.ErrRejected, errors.New("status 400")))

	_, err := f.worker.ProcessOnce(context.Background())

	require.NoError(t, err)
	assert.Equal(t, idempotency.StatusFailed, f.notices.get(noticeA1).Status)
	assert.Equal(t, domain.StatusPartiallyRevoked, f.view(t).Status)
}

func TestARetryAfterAPartialRevocationConfirmsAndNeverNotifiesTheConfirmedAgain(t *testing.T) {
	f := newWorkerFixture(t, idempotency.ErrRejected)
	ctx := context.Background()
	_, _, err := f.svc.RevokeAsHolder(ctx, "1020304050", "sub-a", "cns-a2", "req-2", holderRequest(f.clock.Now()))
	require.NoError(t, err)
	_, err = f.worker.ProcessOnce(ctx)
	require.NoError(t, err)
	require.Equal(t, idempotency.StatusFailed, f.notices.get(noticeA1).Status, "a1 was rejected")
	require.Equal(t, idempotency.StatusConfirmed, f.notices.get("cns-a2/aliado-banco-1").Status, "a2 confirmed")
	calls := f.notifier.calls.Load()

	retried, err := f.svc.RetryPending(ctx, "cns-a1", domain.Actor{Type: "operations", ID: "sub-ops"})
	require.NoError(t, err)
	assert.Equal(t, []string{"aliado-banco-1"}, retried)
	retried, err = f.svc.RetryPending(ctx, "cns-a2", domain.Actor{Type: "operations", ID: "sub-ops"})
	require.NoError(t, err)
	assert.Empty(t, retried, "a confirmed entity is never retried")
	_, err = f.worker.ProcessOnce(ctx)
	require.NoError(t, err)

	assert.Equal(t, calls+1, f.notifier.calls.Load(), "only the pending notice was sent")
	assert.Equal(t, idempotency.StatusConfirmed, f.notices.get(noticeA1).Status)
	v := f.view(t)
	assert.Equal(t, domain.StatusRevoked, v.Status, "from partial to revoked")
	assert.Equal(t, domain.NoticeCompleted, v.Revocation.Summary)
}

func TestTwoWorkersCallTheEntityOncePerAttempt(t *testing.T) {
	f := newWorkerFixture(t)
	f.notifier.block = make(chan struct{})
	other, err := consents.NewNoticeWorker(f.notices, f.notifier, consents.NoticeWorkerConfig{Now: f.clock.Now})
	require.NoError(t, err)

	var wg sync.WaitGroup
	results := make(chan int, 2)
	for _, w := range []*consents.NoticeWorker{f.worker, other} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			n, err := w.ProcessOnce(context.Background())
			assert.NoError(t, err)
			results <- n
		}()
	}
	require.Eventually(t, func() bool { return f.notifier.calls.Load() >= 1 }, time.Second, time.Millisecond)
	close(f.notifier.block)
	wg.Wait()
	close(results)

	total := 0
	for n := range results {
		total += n
	}
	assert.Equal(t, 1, total, "one claim")
	assert.EqualValues(t, 1, f.notifier.calls.Load(), "one call")
}

func TestRunWakesUpForANewRevocationAndStopsWithTheContext(t *testing.T) {
	f := newWorkerFixture(t)
	w, err := consents.NewNoticeWorker(f.notices, f.notifier, consents.NoticeWorkerConfig{Now: f.clock.Now, PollInterval: time.Hour})
	require.NoError(t, err)
	f.svc.OnNewNotices(w.Wake)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		w.Run(ctx)
		close(done)
	}()

	require.Eventually(t, func() bool { return f.notifier.calls.Load() == 1 }, 2*time.Second, 5*time.Millisecond, "the first round runs at once")
	_, _, err = f.svc.RevokeAsHolder(context.Background(), "1020304050", "sub-a", "cns-a2", "req-2", holderRequest(f.clock.Now()))
	require.NoError(t, err)
	require.Eventually(t, func() bool { return f.notifier.calls.Load() == 2 }, 2*time.Second, 5*time.Millisecond,
		"woken up by the revocation, without waiting an hour")
	w.Wake()
	w.Wake() // never blocks

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not stop with the context")
	}
}

func TestProcessOnceReportsAStoreFailure(t *testing.T) {
	f := newWorkerFixture(t)
	f.notices.dueErr = errors.New("db down")

	_, err := f.worker.ProcessOnce(context.Background())

	assert.ErrorContains(t, err, "db down")
}

func TestRunLogsAStoreFailureAndKeepsGoing(t *testing.T) {
	f := newWorkerFixture(t)
	f.notices.dueErr = errors.New("db down")
	logs := captureSlog(t)
	w, err := consents.NewNoticeWorker(f.notices, f.notifier, consents.NoticeWorkerConfig{Now: f.clock.Now, PollInterval: 5 * time.Millisecond})
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	w.Run(ctx)

	assert.Contains(t, logs.String(), "could not process the revocation notices")
}

func TestNewNoticeWorkerNeedsAStoreAndANotifier(t *testing.T) {
	_, err := consents.NewNoticeWorker(nil, &fakeNotifier{}, consents.NoticeWorkerConfig{})
	assert.Error(t, err)
	_, err = consents.NewNoticeWorker(newMemNotices(&syncEvents{}), nil, consents.NoticeWorkerConfig{})
	assert.Error(t, err)
	_, err = consents.NewNoticeWorker(newMemNotices(&syncEvents{}), &fakeNotifier{}, consents.NoticeWorkerConfig{RetryBase: time.Minute, RetryMax: time.Second})
	assert.Error(t, err, "an invalid retry policy")
	w, err := consents.NewNoticeWorker(newMemNotices(&syncEvents{}), &fakeNotifier{}, consents.NoticeWorkerConfig{})
	require.NoError(t, err)
	n, err := w.ProcessOnce(context.Background())
	require.NoError(t, err)
	assert.Zero(t, n)
}

func TestTheWorkerLogsNoHolderData(t *testing.T) {
	f := newWorkerFixture(t, idempotency.ErrAmbiguous)
	logs := captureSlog(t)

	_, err := f.worker.ProcessOnce(context.Background())
	require.NoError(t, err)
	f.clock.advance(time.Minute)
	_, err = f.worker.ProcessOnce(context.Background())
	require.NoError(t, err)

	assert.Contains(t, logs.String(), "revocation notice rescheduled")
	assert.Contains(t, logs.String(), "revocation notice confirmed")
	assert.NotContains(t, logs.String(), "1020304050", "never the document")
	assert.NotContains(t, logs.String(), "Ana Pérez", "never the name")
}
