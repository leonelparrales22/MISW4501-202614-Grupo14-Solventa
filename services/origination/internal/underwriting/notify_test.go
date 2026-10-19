package underwriting_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/underwriting"
)

type noticeEnv struct {
	*env
	notifier *recordingNotifier
	worker   *underwriting.ReviewNotificationWorker
}

func newNoticeEnv(t *testing.T) *noticeEnv {
	t.Helper()
	e := newEnv(t, true)
	n := &recordingNotifier{}
	return &noticeEnv{env: e, notifier: n,
		worker: underwriting.NewReviewNotificationWorker(e.reviews, n, e.clock, underwriting.NotificationConfig{})}
}

func (e *noticeEnv) notice(t *testing.T, id string) domain.ReviewNotification {
	t.Helper()
	c, err := e.reviews.Get(context.Background(), id)
	require.NoError(t, err)
	require.NotNil(t, c.Notification)
	return *c.Notification
}

// D-3 c: the customer gets the rejection, with its own idempotency key.
func TestWorkerSendsTheRejectionNotice(t *testing.T) {
	e := newNoticeEnv(t)
	offer := e.route(t, "acc-1", "demo-highrisk-1", domain.ReviewReasonHighRiskScore)
	c, err := e.svc.Decide(advisor, "acc-1", reject)
	require.NoError(t, err)

	assert.Equal(t, 1, e.worker.ProcessDue(context.Background()))

	sent := e.notifier.notices()
	require.Len(t, sent, 1)
	assert.Equal(t, domain.ReviewOutcomeNotice{
		To: offer.Applicant.Email, CustomerName: offer.Applicant.FullName, AcceptanceID: "acc-1",
		Outcome: domain.DecisionReject, DecidedAt: c.Decision.DecidedAt, IdempotencyKey: "review-outcome:acc-1",
	}, sent[0])
	n := e.notice(t, "acc-1")
	assert.Equal(t, domain.NotificationSent, n.Status)
	assert.Equal(t, t0, n.SentAt)
	assert.Zero(t, n.Attempts)
	assert.True(t, n.NextAttemptAt.IsZero())

	assert.Zero(t, e.worker.ProcessDue(context.Background()), "a sent notice is never sent again")
	assert.Len(t, e.notifier.notices(), 1)
}

func TestWorkerSendsTheAdjustedOfferInTheNotice(t *testing.T) {
	e := newNoticeEnv(t)
	e.route(t, "acc-1", "demo-review-1", domain.ReviewReasonProvisionalOffer)
	c, err := e.svc.Decide(advisor, "acc-1", adjust(10, "Observación interna"))
	require.NoError(t, err)

	e.worker.ProcessDue(context.Background())

	sent := e.notifier.notices()
	require.Len(t, sent, 1)
	assert.Equal(t, domain.DecisionAdjust, sent[0].Outcome)
	assert.Equal(t, c.Decision.AdjustedOffer, sent[0].AdjustedOffer)
}

// A failed send is retried with a growing wait, capped at 15 s, and the notice
// fails after 12 attempts; the decision does not change.
func TestWorkerRetriesWithAGrowingWaitAndGivesUpAfterTwelve(t *testing.T) {
	e := newNoticeEnv(t)
	e.route(t, "acc-1", "demo-highrisk-1", domain.ReviewReasonHighRiskScore)
	_, err := e.svc.Decide(advisor, "acc-1", reject)
	require.NoError(t, err)
	boom := errors.New("e-mail provider is unavailable")
	for i := 0; i < 12; i++ {
		e.notifier.errs = append(e.notifier.errs, boom)
	}

	waits := []time.Duration{}
	for attempt := 1; attempt <= 12; attempt++ {
		require.Equal(t, 1, e.worker.ProcessDue(context.Background()), "attempt %d", attempt)
		n := e.notice(t, "acc-1")
		assert.Equal(t, attempt, n.Attempts)
		assert.Equal(t, boom.Error(), n.LastFailure)
		assert.Equal(t, e.clock.Now(), n.LastFailureAt)
		if attempt == 12 {
			assert.Equal(t, domain.NotificationFailed, n.Status)
			assert.True(t, n.NextAttemptAt.IsZero())
			break
		}
		assert.Equal(t, domain.NotificationPending, n.Status)
		wait := n.NextAttemptAt.Sub(e.clock.Now())
		waits = append(waits, wait)
		assert.Zero(t, e.worker.ProcessDue(context.Background()), "not due before its wait")
		e.clock.advance(wait)
	}
	assert.Equal(t, []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second,
		15 * time.Second, 15 * time.Second, 15 * time.Second, 15 * time.Second, 15 * time.Second,
		15 * time.Second, 15 * time.Second}, waits)
	assert.Empty(t, e.notifier.notices())
	assert.Zero(t, e.worker.ProcessDue(context.Background()), "a failed notice is not retried")

	c, err := e.reviews.Get(context.Background(), "acc-1")
	require.NoError(t, err)
	assert.Equal(t, domain.ReviewDecided, c.Status)
	assert.Equal(t, domain.AcceptanceRejected, e.acceptance(t, "acc-1").Status)
}

func TestWorkerSendsAfterTwoFailures(t *testing.T) {
	e := newNoticeEnv(t)
	e.route(t, "acc-1", "demo-highrisk-1", domain.ReviewReasonHighRiskScore)
	_, err := e.svc.Decide(advisor, "acc-1", reject)
	require.NoError(t, err)
	e.notifier.errs = []error{errors.New("down"), errors.New("down")}

	for i := 0; i < 3; i++ {
		e.worker.ProcessDue(context.Background())
		e.clock.advance(time.Minute)
	}

	n := e.notice(t, "acc-1")
	assert.Equal(t, domain.NotificationSent, n.Status)
	assert.Equal(t, 2, n.Attempts, "the failed sends before it went out")
	assert.Len(t, e.notifier.notices(), 1)
}

func TestWorkerFailsANoticeWithoutEmailAtOnce(t *testing.T) {
	e := newNoticeEnv(t)
	ctx := context.Background()
	require.NoError(t, e.reviews.Enqueue(ctx, domain.ReviewRequest{AcceptanceID: "acc-9", ReceivedAt: t0}))
	_, err := e.reviews.RecordDecision(ctx, domain.HumanDecision{ID: "d", AcceptanceID: "acc-9", Type: domain.DecisionReject})
	require.NoError(t, err)
	require.NoError(t, e.reviews.UpdateNotification(ctx, "acc-9", domain.ReviewNotification{Status: domain.NotificationPending, NextAttemptAt: t0}))

	e.worker.ProcessDue(ctx)

	n := e.notice(t, "acc-9")
	assert.Equal(t, domain.NotificationFailed, n.Status)
	assert.Equal(t, 1, n.Attempts)
	assert.Contains(t, n.LastFailure, "no e-mail")
	assert.Empty(t, e.notifier.notices())
}

func TestWorkerNeverNotifiesAnAcceptDecision(t *testing.T) {
	e := newNoticeEnv(t)
	ctx := context.Background()
	require.NoError(t, e.reviews.Enqueue(ctx, domain.ReviewRequest{AcceptanceID: "acc-9", ReceivedAt: t0,
		Applicant: domain.Applicant{Email: "ana@example.com"}}))
	_, err := e.reviews.RecordDecision(ctx, domain.HumanDecision{ID: "d", AcceptanceID: "acc-9", Type: domain.DecisionAccept})
	require.NoError(t, err)
	require.NoError(t, e.reviews.UpdateNotification(ctx, "acc-9", domain.ReviewNotification{Status: domain.NotificationPending, NextAttemptAt: t0}))

	e.worker.ProcessDue(ctx)

	assert.Empty(t, e.notifier.notices())
	assert.Equal(t, 1, e.notice(t, "acc-9").Attempts)
}

func TestWorkerKeepsTheNoticeDueWhenItCannotRecordIt(t *testing.T) {
	e := newNoticeEnv(t)
	e.route(t, "acc-1", "demo-highrisk-1", domain.ReviewReasonHighRiskScore)
	_, err := e.svc.Decide(advisor, "acc-1", reject)
	require.NoError(t, err)
	e.reviews.updateErrs = []error{errors.New("store down")}

	e.worker.ProcessDue(context.Background())
	assert.Equal(t, domain.NotificationPending, e.notice(t, "acc-1").Status)

	e.worker.ProcessDue(context.Background())
	assert.Equal(t, domain.NotificationSent, e.notice(t, "acc-1").Status)
	sent := e.notifier.notices()
	require.Len(t, sent, 2, "sent again with the same key, the provider drops the copy")
	assert.Equal(t, sent[0].IdempotencyKey, sent[1].IdempotencyKey)
}

func TestWorkerSurvivesAListFailure(t *testing.T) {
	e := newNoticeEnv(t)
	e.reviews.listDueErr = errors.New("down")
	assert.Zero(t, e.worker.ProcessDue(context.Background()))
}

func TestWorkerRunsOnNotifyUntilTheContextEnds(t *testing.T) {
	e := newNoticeEnv(t)
	worker := underwriting.NewReviewNotificationWorker(e.reviews, e.notifier, e.clock,
		underwriting.NotificationConfig{PollInterval: time.Hour, RetryBase: time.Millisecond, RetryMax: time.Millisecond, MaxAttempts: 3,
			AttemptTimeout: time.Second})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		worker.Run(ctx)
		close(done)
	}()

	e.route(t, "acc-1", "demo-highrisk-1", domain.ReviewReasonHighRiskScore)
	_, err := e.svc.Decide(advisor, "acc-1", reject)
	require.NoError(t, err)
	worker.Notify()
	worker.Notify() // never blocks

	require.Eventually(t, func() bool { return len(e.notifier.notices()) == 1 }, 2*time.Second, 5*time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not stop")
	}
}
