package assistance_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/assistance"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/ports"
)

// The notices of the dispatches to the providers (HU-W2.3-03, decision D4).

// mailer is the e-mail provider: it records what it delivered and fails the
// first failures sends.
type mailer struct {
	mu       sync.Mutex
	failures int
	sent     []string
	keys     []string
}

func (m *mailer) Send(_ context.Context, to, _, _, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.keys = append(m.keys, key)
	if m.failures > 0 {
		m.failures--
		return errors.New("the e-mail provider is down")
	}
	m.sent = append(m.sent, to)
	return nil
}

func (m *mailer) delivered() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.sent...)
}

// dispatched is the fixture with the request of the mockup dispatched.
func dispatched(t *testing.T) *inboxFixture {
	t.Helper()
	f := newInbox(t)
	f.towing(t)
	_, err := f.svc.Dispatch(context.Background(), withGPS, "prv-gruas-autopista-norte", analyst)
	require.NoError(t, err)
	return f
}

func noticeOf(t *testing.T, f *inboxFixture) ports.AssistanceNotice {
	t.Helper()
	view, err := f.svc.Case(context.Background(), withGPS)
	require.NoError(t, err)
	require.NotNil(t, view.Notice)
	return *view.Notice
}

func TestTheNoticeReachesTheProviderOnce(t *testing.T) {
	f := dispatched(t)
	m := &mailer{}
	w := assistance.NewNoticeWorker(f.inbox, m, f.clock, assistance.NoticeConfig{})

	assert.Equal(t, 1, w.SendDue(context.Background()))
	assert.Equal(t, []string{"despachos@gruasautopistanorte.example.com"}, m.delivered())
	assert.Equal(t, []string{domain.AssistanceNoticeKey(withGPS)}, m.keys)
	n := noticeOf(t, f)
	assert.Equal(t, ports.NoticeSent, n.Status)
	assert.Equal(t, 1, n.Attempts)
	assert.Equal(t, inboxNow, n.SentAt)

	f.clock.now = inboxNow.Add(time.Hour)
	assert.Zero(t, w.SendDue(context.Background()), "a sent notice is not sent again")
	assert.Len(t, m.delivered(), 1)
}

func TestAFailedNoticeIsRetriedWithGrowingWaits(t *testing.T) {
	f := dispatched(t)
	m := &mailer{failures: 2}
	w := assistance.NewNoticeWorker(f.inbox, m, f.clock, assistance.NoticeConfig{})
	ctx := context.Background()

	assert.Equal(t, 1, w.SendDue(ctx))
	n := noticeOf(t, f)
	assert.Equal(t, ports.NoticePending, n.Status)
	assert.Equal(t, 1, n.Attempts)
	assert.Equal(t, inboxNow.Add(5*time.Second), n.NextAttemptAt, "first wait: 5 s")
	assert.Equal(t, "the e-mail provider is down", n.LastError)

	f.clock.now = inboxNow.Add(4 * time.Second)
	assert.Zero(t, w.SendDue(ctx), "not due yet")

	f.clock.now = inboxNow.Add(5 * time.Second)
	assert.Equal(t, 1, w.SendDue(ctx))
	assert.Equal(t, f.clock.now.Add(30*time.Second), noticeOf(t, f).NextAttemptAt, "second wait: 30 s")

	f.clock.now = f.clock.now.Add(30 * time.Second)
	assert.Equal(t, 1, w.SendDue(ctx))
	n = noticeOf(t, f)
	assert.Equal(t, ports.NoticeSent, n.Status)
	assert.Equal(t, 3, n.Attempts)
	assert.Equal(t, []string{"despachos@gruasautopistanorte.example.com"}, m.delivered(), "delivered once")
	for _, k := range m.keys {
		assert.Equal(t, "asistencia-"+withGPS, k, "every attempt with the same key")
	}
}

func TestTheWaitsOfTheNoticesStopGrowingAtTenMinutes(t *testing.T) {
	f := dispatched(t)
	m := &mailer{failures: 10}
	w := assistance.NewNoticeWorker(f.inbox, m, f.clock, assistance.NoticeConfig{})
	var waits []time.Duration
	for range 6 {
		require.Equal(t, 1, w.SendDue(context.Background()))
		next := noticeOf(t, f).NextAttemptAt
		waits = append(waits, next.Sub(f.clock.now))
		f.clock.now = next
	}
	assert.Equal(t, []time.Duration{5 * time.Second, 30 * time.Second, 2 * time.Minute, 10 * time.Minute,
		10 * time.Minute, 10 * time.Minute}, waits)
}

func TestWithoutAMailerTheNoticesWait(t *testing.T) {
	f := dispatched(t)
	w := assistance.NewNoticeWorker(f.inbox, nil, f.clock, assistance.NoticeConfig{})
	assert.Zero(t, w.SendDue(context.Background()))
	n := noticeOf(t, f)
	assert.Equal(t, ports.NoticePending, n.Status, "the dispatch is recorded and its notice waits")
	assert.Zero(t, n.Attempts)
}

// noticeStore fails the reads or the writes of the notices it is told to.
type noticeStore struct {
	ports.AssistanceInboxStore
	due, sent, failed error
	notices           []ports.AssistanceNotice
}

func (s *noticeStore) ClaimNoticesDue(context.Context, time.Time, time.Duration, int) ([]ports.AssistanceNotice,
	error) {
	return s.notices, s.due
}

func (s *noticeStore) NoticeSent(context.Context, string, time.Time) error { return s.sent }

func (s *noticeStore) NoticeFailed(context.Context, string, time.Time, string) error { return s.failed }

func TestTheBatchOfNoticesGoesOnWhenOneFails(t *testing.T) {
	store := &noticeStore{notices: []ports.AssistanceNotice{{RequestID: "a", To: "a@example.com"},
		{RequestID: "b", To: "b@example.com"}}, sent: errors.New("down"), failed: errors.New("down")}
	m := &mailer{failures: 1}
	w := assistance.NewNoticeWorker(store, m, &clock{now: inboxNow}, assistance.NoticeConfig{})
	assert.Equal(t, 2, w.SendDue(context.Background()), "a failure is logged and the next one is tried")
	assert.Equal(t, []string{"b@example.com"}, m.delivered())

	w = assistance.NewNoticeWorker(&noticeStore{due: errors.New("down")}, m, &clock{now: inboxNow},
		assistance.NoticeConfig{})
	assert.Zero(t, w.SendDue(context.Background()))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	w = assistance.NewNoticeWorker(store, &mailer{}, &clock{now: inboxNow}, assistance.NoticeConfig{})
	assert.Equal(t, 2, w.SendDue(ctx), "a cancelled context sends nothing more")
}

func TestRunSendsOnNotifyAndOnItsPollUntilCancelled(t *testing.T) {
	f := newInbox(t)
	m := &mailer{}
	w := assistance.NewNoticeWorker(f.inbox, m, f.clock, assistance.NoticeConfig{Poll: time.Hour,
		Lease: time.Minute, Backoff: []time.Duration{time.Second}})
	svc := assistance.NewInboxService(f.inbox, f.providers, f.clock, w.Notify)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		w.Run(ctx)
		close(done)
	}()

	f.request(t, withGPS, domain.LineAuto, &autopista, inboxNow)
	_, err := svc.Attend(ctx, withGPS, analyst)
	require.NoError(t, err)
	_, err = svc.RecordNeed(ctx, withGPS, "towing", analyst)
	require.NoError(t, err)
	_, err = svc.Dispatch(ctx, withGPS, "prv-gruas-autopista-norte", analyst)
	require.NoError(t, err)
	w.Notify() // a second wake never blocks
	assert.Eventually(t, func() bool { return len(m.delivered()) == 1 }, 5*time.Second, 10*time.Millisecond,
		"the dispatch woke the worker before its poll of an hour")

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not stop when its context was cancelled")
	}

	w = assistance.NewNoticeWorker(f.inbox, m, f.clock, assistance.NoticeConfig{Poll: 10 * time.Millisecond})
	ctx, cancel = context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	w.Run(ctx) // polls a few times and returns
}
