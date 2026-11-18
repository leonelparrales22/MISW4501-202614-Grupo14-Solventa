package endorsements_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/endorsements"
)

// fakeNotices keeps the e-mails of the decisions with their delivery.
type fakeNotices struct {
	mu   sync.Mutex
	rows map[string]domain.EndorsementNotice
}

func (f *fakeNotices) ListDueNotices(_ context.Context, now time.Time, _ int) ([]domain.EndorsementNotice, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []domain.EndorsementNotice{}
	for _, n := range f.rows {
		if n.Delivery.Status == domain.NotificationPending && !n.Delivery.NextAttemptAt.After(now) {
			out = append(out, n)
		}
	}
	return out, nil
}

func (f *fakeNotices) UpdateNotice(_ context.Context, id string, d domain.NoticeDelivery) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := f.rows[id]
	n.Delivery = d
	f.rows[id] = n
	return nil
}

type fakeNotifier struct {
	mu       sync.Mutex
	failures int
	sent     []domain.EndorsementNotice
}

func (f *fakeNotifier) NotifyEndorsementOutcome(_ context.Context, n domain.EndorsementNotice) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failures > 0 {
		f.failures--
		return errors.New("e-mail provider is unavailable")
	}
	f.sent = append(f.sent, n)
	return nil
}

func pendingNotice(id, to string) domain.EndorsementNotice {
	return domain.EndorsementNotice{RequestID: id, Outcome: domain.NoticeApplied, To: to,
		Delivery: domain.NoticeDelivery{Status: domain.NotificationPending, NextAttemptAt: now}}
}

func TestAFailedNoticeIsRetriedWithAGrowingWaitUntilItIsSent(t *testing.T) {
	clock := &fakeClock{now: now}
	notices := &fakeNotices{rows: map[string]domain.EndorsementNotice{"SOL-MOD-2026-0042": pendingNotice("SOL-MOD-2026-0042", "carolina@example.com")}}
	notifier := &fakeNotifier{failures: 2}
	w := endorsements.NewNoticeWorker(notices, notifier, clock, endorsements.NoticeConfig{RetryBase: time.Second, RetryMax: 4 * time.Second})

	assert.Equal(t, 1, w.ProcessDue(context.Background()))
	d := notices.rows["SOL-MOD-2026-0042"].Delivery
	assert.Equal(t, 1, d.Attempts)
	assert.Equal(t, now.Add(time.Second), d.NextAttemptAt)
	assert.Equal(t, "e-mail provider is unavailable", d.LastFailure)
	assert.Zero(t, w.ProcessDue(context.Background()), "not due before its next attempt")

	clock.now = now.Add(time.Second)
	w.ProcessDue(context.Background())
	assert.Equal(t, now.Add(3*time.Second), notices.rows["SOL-MOD-2026-0042"].Delivery.NextAttemptAt, "the wait doubles")

	clock.now = now.Add(3 * time.Second)
	w.ProcessDue(context.Background())
	d = notices.rows["SOL-MOD-2026-0042"].Delivery
	assert.Equal(t, domain.NotificationSent, d.Status)
	assert.Equal(t, now.Add(3*time.Second), d.SentAt)
	require.Len(t, notifier.sent, 1)
}

func TestANoticeGivesUpAfterItsAttemptsOrWithoutAnEmail(t *testing.T) {
	notices := &fakeNotices{rows: map[string]domain.EndorsementNotice{
		"SOL-MOD-2026-0001": pendingNotice("SOL-MOD-2026-0001", "carolina@example.com"),
		"SOL-MOD-2026-0002": pendingNotice("SOL-MOD-2026-0002", ""),
	}}
	notifier := &fakeNotifier{failures: 1}
	w := endorsements.NewNoticeWorker(notices, notifier, &fakeClock{now: now}, endorsements.NoticeConfig{MaxAttempts: 1})

	w.ProcessDue(context.Background())

	for _, id := range []string{"SOL-MOD-2026-0001", "SOL-MOD-2026-0002"} {
		assert.Equal(t, domain.NotificationFailed, notices.rows[id].Delivery.Status, id)
	}
	assert.Empty(t, notifier.sent)
}

func TestTheWorkerSendsWhenADecisionWakesIt(t *testing.T) {
	notices := &fakeNotices{rows: map[string]domain.EndorsementNotice{}}
	notifier := &fakeNotifier{}
	w := endorsements.NewNoticeWorker(notices, notifier, &fakeClock{now: now}, endorsements.NoticeConfig{PollInterval: time.Hour})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		w.Run(ctx)
		close(done)
	}()

	notices.mu.Lock()
	notices.rows["SOL-MOD-2026-0042"] = pendingNotice("SOL-MOD-2026-0042", "carolina@example.com")
	notices.mu.Unlock()
	w.Notify()
	w.Notify()

	assert.Eventually(t, func() bool {
		notifier.mu.Lock()
		defer notifier.mu.Unlock()
		return len(notifier.sent) == 1
	}, 2*time.Second, 10*time.Millisecond)
	cancel()
	<-done
}
