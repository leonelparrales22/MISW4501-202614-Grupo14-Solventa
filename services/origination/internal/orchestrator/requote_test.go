package orchestrator_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/security"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/memory"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/orchestrator"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/rating"
)

type recordingNotifier struct {
	mu      sync.Mutex
	notices map[string]domain.RequoteNotice // by idempotency key, like the provider
	calls   int
	err     error
}

func (n *recordingNotifier) Notify(_ context.Context, in domain.RequoteNotice) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.calls++
	if n.err != nil {
		return n.err
	}
	if n.notices == nil {
		n.notices = map[string]domain.RequoteNotice{}
	}
	n.notices[in.IdempotencyKey] = in
	return nil
}

type brokenAcceptances struct{ ports.AcceptanceRepository }

func (brokenAcceptances) GetByOffer(context.Context, string) (domain.Acceptance, error) {
	return domain.Acceptance{}, errors.New("db down")
}

type requoteEnv struct {
	*offerEnv
	acceptances *memory.AcceptanceStore
	notifier    *recordingNotifier
	worker      *orchestrator.RequoteWorker
}

func newRequoteEnv() *requoteEnv {
	e := &requoteEnv{offerEnv: newOfferEnv(), acceptances: memory.NewAcceptanceStore(), notifier: &recordingNotifier{}}
	e.profiles.set(baseProfile(), nil)
	e.worker = e.workerWith(e.acceptances)
	return e
}

func (e *requoteEnv) workerWith(a ports.AcceptanceRepository) *orchestrator.RequoteWorker {
	return orchestrator.NewRequoteWorker(orchestrator.RequoteDeps{
		Store: e.store, Acceptances: a, Consents: e.consent, Profiles: e.profiles, Rater: rating.New(),
		Notifier: e.notifier, Clock: e.clock,
	}, orchestrator.RequoteConfig{})
}

func (e *requoteEnv) process() int {
	n := e.worker.ProcessOnce(context.Background())
	e.worker.Wait()
	return n
}

func TestRequoteLeavesTheOfferAloneWhileTheSourceIsStillDown(t *testing.T) {
	e := newRequoteEnv()
	o := generated(t, e.offerEnv)

	assert.Zero(t, e.process())

	got, _ := e.store.Get(context.Background(), o.ID)
	assert.Empty(t, got.SupersededBy)
	assert.Zero(t, e.notifier.calls)
}

func TestRequoteReplacesTheOfferWhenTheSourceIsBackAndTellsTheCustomerOnce(t *testing.T) {
	e := newRequoteEnv()
	o := generated(t, e.offerEnv)
	_, _ = e.store.Claim(context.Background(), o.ID, "sub-a")
	e.profiles.set(freshProfile(), nil)
	e.clock.advance(time.Hour)

	assert.Equal(t, 1, e.process())
	assert.Zero(t, e.process(), "the replaced offer is not examined again")

	old, _ := e.store.Get(context.Background(), o.ID)
	require.NotEmpty(t, old.SupersededBy)
	repl, _ := e.store.Get(context.Background(), old.SupersededBy)
	assert.Equal(t, domain.ModeNormal, repl.Mode)
	assert.Equal(t, o.ID, repl.Supersedes)
	assert.Equal(t, "sub-a", repl.OwnerID)
	assert.Equal(t, e.clock.Now().Add(24*time.Hour), repl.ExpiresAt)
	assert.Equal(t, domain.OfferSuperseded, old.Status(e.clock.Now()))
	require.Len(t, e.notifier.notices, 1)
	n := e.notifier.notices["offer-requoted:"+o.ID]
	assert.Equal(t, "ana@example.com", n.To)
	assert.Equal(t, repl.ID, n.NewOfferID)
	assert.Equal(t, o.Premium, n.OldPremium)
	assert.Equal(t, repl.Premium, n.NewPremium)
	acc := old.AsOffer()
	assert.True(t, acc.IsExpired(e.clock.Now()), "accepting the old one answers offer_expired")
}

func TestRequoteRespectsAProvisionalOfferAlreadyAccepted(t *testing.T) {
	e := newRequoteEnv()
	o := generated(t, e.offerEnv)
	_, _, err := e.acceptances.Create(context.Background(), domain.Acceptance{ID: "acc-1", OfferID: o.ID, Status: domain.AcceptanceRegistered})
	require.NoError(t, err)
	e.profiles.set(freshProfile(), nil)

	assert.Zero(t, e.process())

	got, _ := e.store.Get(context.Background(), o.ID)
	assert.Empty(t, got.SupersededBy)
	assert.Zero(t, e.notifier.calls)
}

func TestRequoteTreatsAnUnreadableAcceptanceAsAccepted(t *testing.T) {
	e := newRequoteEnv()
	o := generated(t, e.offerEnv)
	e.profiles.set(freshProfile(), nil)
	w := e.workerWith(brokenAcceptances{})

	assert.Zero(t, w.ProcessOnce(context.Background()))

	got, _ := e.store.Get(context.Background(), o.ID)
	assert.Empty(t, got.SupersededBy)
}

func TestRequoteSkipsCustomersWithoutAVerifiedConsent(t *testing.T) {
	for name, set := range map[string]func(*scriptedConsent){
		"revoked":      func(c *scriptedConsent) { c.status = domain.ConsentStatus{} },
		"unverifiable": func(c *scriptedConsent) { c.err = ports.ErrConsentUnverifiable },
	} {
		t.Run(name, func(t *testing.T) {
			e := newRequoteEnv()
			generated(t, e.offerEnv)
			e.profiles.set(freshProfile(), nil)
			before := e.profiles.calls
			set(e.consent)

			assert.Zero(t, e.process())
			assert.Equal(t, before, e.profiles.calls, "no source is queried")
		})
	}
}

func TestRequoteDoesNotTouchExpiredOffers(t *testing.T) {
	e := newRequoteEnv()
	generated(t, e.offerEnv)
	e.profiles.set(freshProfile(), nil)
	e.clock.advance(24 * time.Hour)

	assert.Zero(t, e.process())
}

func TestRequoteConcurrentWorkersCreateASingleReplacementAndOneNotice(t *testing.T) {
	e := newRequoteEnv()
	o := generated(t, e.offerEnv)
	e.profiles.set(freshProfile(), nil)
	workers := []*orchestrator.RequoteWorker{e.workerWith(e.acceptances), e.workerWith(e.acceptances), e.workerWith(e.acceptances)}

	var wg sync.WaitGroup
	var mu sync.Mutex
	total := 0
	for _, w := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			n := w.ProcessOnce(context.Background())
			w.Wait()
			mu.Lock()
			total += n
			mu.Unlock()
		}()
	}
	wg.Wait()

	assert.Equal(t, 1, total)
	assert.Len(t, e.notifier.notices, 1)
	assert.Equal(t, 1, e.notifier.calls, "only the winner notifies")
	old, _ := e.store.Get(context.Background(), o.ID)
	assert.NotEmpty(t, old.SupersededBy)
}

func TestRequoteNoticeFailureDoesNotUndoTheReplacement(t *testing.T) {
	e := newRequoteEnv()
	o := generated(t, e.offerEnv)
	e.profiles.set(freshProfile(), nil)
	e.notifier.err = errors.New("smtp down")

	assert.Equal(t, 1, e.process())

	old, _ := e.store.Get(context.Background(), o.ID)
	assert.NotEmpty(t, old.SupersededBy)
}

func TestRequoteWithoutANotifierStillReplaces(t *testing.T) {
	e := newRequoteEnv()
	o := generated(t, e.offerEnv)
	e.profiles.set(freshProfile(), nil)
	w := orchestrator.NewRequoteWorker(orchestrator.RequoteDeps{
		Store: e.store, Acceptances: e.acceptances, Consents: e.consent, Profiles: e.profiles, Rater: rating.New(), Clock: e.clock,
	}, orchestrator.RequoteConfig{})

	assert.Equal(t, 1, w.ProcessOnce(context.Background()))
	old, _ := e.store.Get(context.Background(), o.ID)
	assert.NotEmpty(t, old.SupersededBy)
}

func TestRequoteRespectsTheBatchSize(t *testing.T) {
	e := newRequoteEnv()
	for i := 0; i < 3; i++ {
		generated(t, e.offerEnv)
	}
	e.profiles.set(freshProfile(), nil)
	w := orchestrator.NewRequoteWorker(orchestrator.RequoteDeps{
		Store: e.store, Acceptances: e.acceptances, Consents: e.consent, Profiles: e.profiles, Rater: rating.New(), Clock: e.clock,
	}, orchestrator.RequoteConfig{Batch: 2})

	assert.Equal(t, 2, w.ProcessOnce(context.Background()))
	assert.Equal(t, 1, w.ProcessOnce(context.Background()))
}

func TestRequoteRunStopsWithTheContext(t *testing.T) {
	e := newRequoteEnv()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { e.worker.Run(ctx); close(done) }()
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not stop")
	}
}

func TestAReplacedOfferIsReadableAsSuperseded(t *testing.T) {
	e := newRequoteEnv()
	o := generated(t, e.offerEnv)
	e.profiles.set(freshProfile(), nil)
	e.process()

	v, err := e.svc.Get(as(security.RoleInsured, "sub-a"), o.ID)

	require.NoError(t, err)
	assert.Equal(t, domain.OfferSuperseded, v.Status)
	assert.NotEmpty(t, v.Personalized.SupersededBy)
	assert.False(t, v.CanAccept)
}
