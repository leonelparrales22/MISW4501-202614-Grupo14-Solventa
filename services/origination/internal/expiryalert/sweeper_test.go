package expiryalert_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/events"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/memory"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/expiryalert"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
)

// HU-M3.2-01: the sweep of the alerts 30, 15 and 5 days before a policy
// expires.

var cot = time.FixedZone("COT", -5*3600)

// sweepNow is 10:00 in Bogotá on 14/11/2026.
var sweepNow = time.Date(2026, 11, 14, 10, 0, 0, 0, cot)

// endIn is the end of the coverage at the last second of the day, in Bogotá,
// days from sweepNow.
func endIn(days int) time.Time {
	return time.Date(2026, 11, 14+days, 23, 59, 59, 0, cot).UTC()
}

type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *clock) set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = t
}

type ownPolicies struct {
	policies      []domain.Policy
	err           error
	after, before time.Time
}

func (o *ownPolicies) ListExpiringUnrenewed(_ context.Context, after, before time.Time) ([]domain.Policy, error) {
	o.after, o.before = after, before
	return o.policies, o.err
}

type holders struct {
	subjects []string
	err      error
}

func (h holders) LinkedSubjects(context.Context) ([]string, error) { return h.subjects, h.err }

type portfolio struct {
	mu       sync.Mutex
	policies map[string][]domain.WalletPolicy
	failing  map[string]bool
}

func (p *portfolio) HolderPolicies(_ context.Context, holderID string) ([]domain.WalletPolicy, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.failing[holderID] {
		return nil, ports.ErrPolicyPortfolioUnavailable
	}
	return append([]domain.WalletPolicy(nil), p.policies[holderID]...), nil
}

func (p *portfolio) Holder(context.Context, string) (domain.PolicyHolder, error) {
	return domain.PolicyHolder{}, errors.New("the sweep never asks for the holder")
}

func (p *portfolio) setEnd(holderID, policyID string, end time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for i := range p.policies[holderID] {
		if p.policies[holderID][i].ID == policyID {
			p.policies[holderID][i].CoverageEnd = end
		}
	}
}

type failingRecorder struct{}

func (failingRecorder) RecordExpiryAlert(context.Context, domain.PolicyExpiryApproaching) (bool, error) {
	return false, errors.New("database down")
}

func corePolicy(id string, line domain.PolicyLine, end time.Time) domain.WalletPolicy {
	return domain.WalletPolicy{ID: id, Source: domain.SourcePolicyCore, Line: line, Product: "Autos",
		InsuredObject: "KJT-482", Status: domain.PolicyActive, CoverageEnd: end}
}

func lifePolicy(id, customer string, end time.Time) domain.Policy {
	return domain.Policy{ID: id, CustomerID: customer, Premium: 100, Currency: "COP", CoverageEnd: end,
		CoverageStart: end.AddDate(-1, 0, 0)}
}

type world struct {
	clock     *clock
	own       *ownPolicies
	portfolio *portfolio
	outbox    *memory.Outbox
	sweeper   *expiryalert.Sweeper
}

func newWorld(t *testing.T) *world {
	t.Helper()
	w := &world{
		clock: &clock{now: sweepNow}, own: &ownPolicies{},
		portfolio: &portfolio{policies: map[string][]domain.WalletPolicy{}, failing: map[string]bool{}},
		outbox:    memory.NewOutbox(),
	}
	var err error
	w.sweeper, err = expiryalert.New(expiryalert.Config{
		Own: w.own, Portfolio: w.portfolio, Holders: holders{subjects: []string{"sub-a", "sub-b"}},
		Recorder: memory.NewExpiryAlertStore(w.outbox), Clock: w.clock,
	})
	require.NoError(t, err)
	return w
}

// alerts decodes the events written to the outbox, the oldest first.
func (w *world) alerts(t *testing.T) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, m := range w.outbox.Pending() {
		env, err := events.Decode(m.Body)
		require.NoError(t, err)
		var p map[string]any
		require.NoError(t, env.DecodePayload(&p))
		p["event_id"] = env.EventID
		out = append(out, p)
	}
	return out
}

func TestTheSweepAlertsThePoliciesOfBothSourcesThatCrossedAMilestone(t *testing.T) {
	w := newWorld(t)
	w.own.policies = []domain.Policy{lifePolicy("pol-acc-1", "sub-a", endIn(5))}
	w.portfolio.policies["sub-a"] = []domain.WalletPolicy{
		corePolicy("AU-2025-118204", domain.LineAuto, endIn(16)),
		corePolicy("HG-2025-880231", domain.LineHome, endIn(125)),
	}
	w.portfolio.policies["sub-b"] = []domain.WalletPolicy{corePolicy("VJ-2026-000931", domain.LineTravel, endIn(8))}

	n, err := w.sweeper.Sweep(context.Background())

	require.NoError(t, err)
	assert.Equal(t, 3, n)
	got := w.alerts(t)
	require.Len(t, got, 3)
	assert.Equal(t, "policy-expiry-approaching-pol-acc-1-2026-11-19-5", got[0]["event_id"])
	assert.Equal(t, "origination", got[0]["source"])
	assert.Equal(t, "sub-a", got[0]["subject"], "the holder of an own policy is its customer")
	assert.Equal(t, "policy-expiry-approaching-AU-2025-118204-2026-11-30-30", got[1]["event_id"])
	assert.Equal(t, float64(16), got[1]["days_left"])
	assert.Equal(t, "policy-expiry-approaching-VJ-2026-000931-2026-11-22-15", got[2]["event_id"])
	assert.Equal(t, "sub-b", got[2]["subject"])
	assert.Equal(t, sweepNow.UTC(), w.own.after, "only the coverages that have not ended")
	assert.Equal(t, time.Date(2026, 12, 15, 5, 0, 0, 0, time.UTC), w.own.before, "up to the end of the 30th day in Bogotá")
}

func TestAMilestoneAlreadyAlertedIsNotRepeated(t *testing.T) {
	w := newWorld(t)
	w.portfolio.policies["sub-a"] = []domain.WalletPolicy{corePolicy("AU-2025-118204", domain.LineAuto, endIn(16))}
	ctx := context.Background()
	_, err := w.sweeper.Sweep(ctx)
	require.NoError(t, err)

	w.clock.set(sweepNow.Add(6 * time.Hour))
	n, err := w.sweeper.Sweep(ctx)
	require.NoError(t, err)
	assert.Zero(t, n, "same day, same milestone")

	w.clock.set(sweepNow.AddDate(0, 0, 1)) // 15 days left
	n, err = w.sweeper.Sweep(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	w.clock.set(sweepNow.AddDate(0, 0, 2)) // 14 days left, still the 15-day milestone
	n, err = w.sweeper.Sweep(ctx)
	require.NoError(t, err)
	assert.Zero(t, n)

	ids := []string{}
	for _, a := range w.alerts(t) {
		ids = append(ids, a["event_id"].(string))
	}
	assert.Equal(t, []string{"policy-expiry-approaching-AU-2025-118204-2026-11-30-30",
		"policy-expiry-approaching-AU-2025-118204-2026-11-30-15"}, ids)
}

func TestAPolicyFirstSeenLateGetsOnlyItsLastMilestone(t *testing.T) {
	w := newWorld(t)
	w.portfolio.policies["sub-a"] = []domain.WalletPolicy{corePolicy("AU-2025-118204", domain.LineAuto, endIn(4))}

	n, err := w.sweeper.Sweep(context.Background())

	require.NoError(t, err)
	assert.Equal(t, 1, n, "never several at once")
	assert.Equal(t, float64(5), w.alerts(t)[0]["milestone_days"])
}

func TestARenewalInThePolicyCoreStartsTheMilestonesOfTheNewPeriod(t *testing.T) {
	w := newWorld(t)
	w.portfolio.policies["sub-a"] = []domain.WalletPolicy{corePolicy("AU-2025-118204", domain.LineAuto, endIn(4))}
	ctx := context.Background()
	_, err := w.sweeper.Sweep(ctx)
	require.NoError(t, err)

	w.portfolio.setEnd("sub-a", "AU-2025-118204", endIn(4).AddDate(1, 0, 0))
	n, err := w.sweeper.Sweep(ctx)

	require.NoError(t, err)
	assert.Zero(t, n, "renewed before it expired: no more alerts for it")
	w.clock.set(endIn(4).AddDate(1, 0, -20))
	n, err = w.sweeper.Sweep(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, n, "the new period is alerted when it gets close")
}

func TestAHolderThePolicyCoreFailsForIsSkipped(t *testing.T) {
	w := newWorld(t)
	w.portfolio.failing["sub-a"] = true
	w.portfolio.policies["sub-b"] = []domain.WalletPolicy{corePolicy("VJ-2026-000931", domain.LineTravel, endIn(8))}

	n, err := w.sweeper.Sweep(context.Background())

	require.NoError(t, err, "a failure for one holder does not fail the sweep")
	assert.Equal(t, 1, n)
}

func TestAFailingSourceDoesNotStopTheOther(t *testing.T) {
	w := newWorld(t)
	w.own.err = errors.New("database down")
	w.portfolio.policies["sub-b"] = []domain.WalletPolicy{corePolicy("VJ-2026-000931", domain.LineTravel, endIn(8))}

	n, err := w.sweeper.Sweep(context.Background())

	assert.Error(t, err)
	assert.Equal(t, 1, n)
}

func TestTheHoldersThatCannotBeListedAreAnError(t *testing.T) {
	s, err := expiryalert.New(expiryalert.Config{Portfolio: &portfolio{}, Holders: holders{err: errors.New("down")},
		Recorder: memory.NewExpiryAlertStore(memory.NewOutbox()), Clock: &clock{now: sweepNow}})
	require.NoError(t, err)

	_, err = s.Sweep(context.Background())

	assert.Error(t, err)
}

func TestAnAlertThatCannotBeRecordedIsAnErrorAndTheRestGoesOn(t *testing.T) {
	own := &ownPolicies{policies: []domain.Policy{lifePolicy("pol-1", "sub-a", endIn(5)), lifePolicy("pol-2", "sub-b", endIn(40))}}
	s, err := expiryalert.New(expiryalert.Config{Own: own, Recorder: failingRecorder{}, Clock: &clock{now: sweepNow}})
	require.NoError(t, err)

	n, err := s.Sweep(context.Background())

	assert.Error(t, err)
	assert.Zero(t, n)
}

func TestTheSweeperNeedsASourceARecorderAndAClock(t *testing.T) {
	rec := memory.NewExpiryAlertStore(memory.NewOutbox())
	for name, cfg := range map[string]expiryalert.Config{
		"no recorder":          {Own: &ownPolicies{}, Clock: &clock{}},
		"no clock":             {Own: &ownPolicies{}, Recorder: rec},
		"no source":            {Recorder: rec, Clock: &clock{}},
		"core without holders": {Portfolio: &portfolio{}, Recorder: rec, Clock: &clock{}},
	} {
		_, err := expiryalert.New(cfg)
		assert.Error(t, err, name)
	}
}

func TestRunSweepsAtOnceAndStopsWithItsContext(t *testing.T) {
	w := newWorld(t)
	w.portfolio.policies["sub-a"] = []domain.WalletPolicy{corePolicy("AU-2025-118204", domain.LineAuto, endIn(16))}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})

	go func() {
		w.sweeper.Run(ctx, 0)
		close(done)
	}()

	require.Eventually(t, func() bool { return len(w.outbox.Pending()) == 1 }, 2*time.Second, 10*time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not stop with its context")
	}
}
