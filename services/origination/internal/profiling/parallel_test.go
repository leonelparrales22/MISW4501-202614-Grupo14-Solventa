package profiling_test

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/profiling"
)

// Real-time tests use wide margins: a 120 ms budget must end "well before"
// 250 ms, never "at exactly" 120 ms.
const (
	testBudget = 120 * time.Millisecond
	slow       = 600 * time.Millisecond
	cutMargin  = 250 * time.Millisecond
)

// delayedSource answers after delay unless its context ends first, like an
// HTTP adapter whose request is canceled.
type delayedSource struct {
	delay    time.Duration
	score    float64
	category string
	err      error

	calls    atomic.Int32
	inFlight atomic.Int32
	canceled atomic.Int32
}

func (s *delayedSource) FetchProfile(ctx context.Context, id string) (domain.RiskProfile, error) {
	s.calls.Add(1)
	s.inFlight.Add(1)
	defer s.inFlight.Add(-1)
	if s.delay > 0 {
		timer := time.NewTimer(s.delay)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-ctx.Done():
			s.canceled.Add(1)
			return domain.RiskProfile{}, fmt.Errorf("%w: %w", ports.ErrSourceUnavailable, ctx.Err())
		}
	}
	if s.err != nil {
		return domain.RiskProfile{}, s.err
	}
	return domain.RiskProfile{CustomerID: id, Score: s.score, Category: s.category, CalculatedAt: now}, nil
}

func fast(score float64, category string) *delayedSource {
	return &delayedSource{score: score, category: category}
}

func slowSource() *delayedSource {
	return &delayedSource{delay: slow, score: 0.9, category: "high_risk"}
}

func named(of, od, kyc ports.RiskProfileSource) []profiling.NamedSource {
	return []profiling.NamedSource{
		{Name: domain.ExternalSourceOpenFinance, Source: of},
		{Name: domain.ExternalSourceOpenData, Source: od},
		{Name: domain.ExternalSourceKYC, Source: kyc},
	}
}

func equivalent() profiling.ParallelConfig {
	return profiling.ParallelConfig{Budget: testBudget, Deadline: 700 * time.Millisecond}
}

func openFinanceRequired() profiling.ParallelConfig {
	cfg := equivalent()
	cfg.Required = domain.ExternalSourceOpenFinance
	return cfg
}

func statuses(outcomes []domain.SourceOutcome) map[string]domain.SourceStatus {
	m := map[string]domain.SourceStatus{}
	for _, o := range outcomes {
		m[o.Source] = o.Status
	}
	return m
}

func TestParallelSourceCompleteProfileWhenEverySourceArrives(t *testing.T) {
	for name, cfg := range map[string]profiling.ParallelConfig{"required": openFinanceRequired(), "equivalent": equivalent()} {
		t.Run(name, func(t *testing.T) {
			src := profiling.NewParallelSource(named(fast(0.2, "preferred"), fast(0.4, "standard"), fast(0.6, "standard")), cfg)

			got, err := src.FetchProfile(context.Background(), "c1")

			require.NoError(t, err)
			assert.Equal(t, "c1", got.CustomerID)
			assert.InDelta(t, 0.4, got.Score, 1e-9, "provisional rule D-4: average of the scores")
			assert.Equal(t, "preferred", got.Category, "category of Open Finance")
			assert.Equal(t, now, got.CalculatedAt)
			assert.False(t, got.Partial)
			require.Len(t, got.Sources, 3)
			assert.Equal(t, []string{"open_finance", "open_data", "kyc"},
				[]string{got.Sources[0].Source, got.Sources[1].Source, got.Sources[2].Source}, "outcomes keep the priority order")
			for _, o := range got.Sources {
				assert.Equal(t, domain.SourceStatusOK, o.Status)
			}
			assert.Equal(t, domain.SourceExternal, got.FreshSource())
		})
	}
}

func TestParallelSourceReportsWhatEachSourceReturnedAndWhenItWasAsked(t *testing.T) {
	before := time.Now()
	src := profiling.NewParallelSource(named(fast(0.2, "preferred"), slowSource(), fast(0.6, "standard")), equivalent())

	got, err := src.FetchProfile(context.Background(), "c1")

	require.NoError(t, err)
	require.Len(t, got.Sources, 3)
	of, od, kyc := got.Sources[0], got.Sources[1], got.Sources[2]
	require.NotNil(t, of.Value)
	assert.Equal(t, domain.SourceValue{Score: 0.2, Category: "preferred"}, *of.Value, "the value Open Finance returned")
	assert.Equal(t, domain.SourceValue{Score: 0.6, Category: "standard"}, *kyc.Value)
	assert.Nil(t, od.Value, "a source that timed out returned nothing")
	for _, o := range got.Sources {
		assert.False(t, o.ConsultedAt.Before(before.UTC().Add(-time.Second)), "%s: the call time is recorded", o.Source)
		assert.Equal(t, time.UTC, o.ConsultedAt.Location())
	}
}

func TestParallelSourcePartialProfileWithArrivedSignals(t *testing.T) {
	src := profiling.NewParallelSource(named(fast(0.2, "preferred"), slowSource(), fast(0.6, "standard")), equivalent())

	got, err := src.FetchProfile(context.Background(), "c1")

	require.NoError(t, err)
	assert.True(t, got.Partial)
	assert.InDelta(t, 0.4, got.Score, 1e-9, "only the signals that arrived are combined")
	assert.Equal(t, map[string]domain.SourceStatus{"open_finance": "ok", "open_data": "timeout", "kyc": "ok"}, statuses(got.Sources))
}

func TestParallelSourceReturnsWithinBudgetWhenOneSourceIsSlow(t *testing.T) {
	src := profiling.NewParallelSource(named(fast(0.2, "preferred"), fast(0.4, "standard"), slowSource()), openFinanceRequired())

	start := time.Now()
	got, err := src.FetchProfile(context.Background(), "c1")
	elapsed := time.Since(start)

	require.NoError(t, err)
	assert.True(t, got.Partial)
	assert.GreaterOrEqual(t, elapsed, testBudget, "the slow source is waited for up to its budget")
	assert.Less(t, elapsed, cutMargin, "the client never waits for the slow source")
}

func TestParallelSourceCancelsEachSourceAtItsBudget(t *testing.T) {
	slowOD := slowSource()
	cfg := equivalent()
	cfg.Budget = 50 * time.Millisecond
	src := profiling.NewParallelSource(named(fast(0.2, "preferred"), slowOD, fast(0.6, "standard")), cfg)

	got, err := src.FetchProfile(context.Background(), "c1")

	require.NoError(t, err)
	assert.Equal(t, int32(1), slowOD.canceled.Load(), "the slow call is canceled, not abandoned")
	od := got.Sources[1]
	assert.Equal(t, domain.SourceStatusTimeout, od.Status)
	assert.GreaterOrEqual(t, od.Latency, 50*time.Millisecond)
	assert.Less(t, od.Latency, 200*time.Millisecond)
	assert.Less(t, got.Sources[0].Latency, 50*time.Millisecond, "each source reports its own latency")
}

func TestParallelSourceStopsAtTheTotalDeadline(t *testing.T) {
	cfg := profiling.ParallelConfig{Budget: time.Second, Deadline: 80 * time.Millisecond}
	src := profiling.NewParallelSource(named(slowSource(), slowSource(), slowSource()), cfg)

	start := time.Now()
	_, err := src.FetchProfile(context.Background(), "c1")
	elapsed := time.Since(start)

	var unavailable *ports.SourcesUnavailableError
	require.ErrorAs(t, err, &unavailable)
	assert.ErrorIs(t, err, ports.ErrSourceUnavailable)
	assert.GreaterOrEqual(t, elapsed, 80*time.Millisecond)
	assert.Less(t, elapsed, cutMargin, "the total deadline cuts even when the budget is longer")
	assert.Equal(t, map[string]domain.SourceStatus{"open_finance": "timeout", "open_data": "timeout", "kyc": "timeout"}, statuses(unavailable.Outcomes))
}

func TestSlowSourceContextIsCanceled(t *testing.T) {
	slowKYC := slowSource()
	src := profiling.NewParallelSource(named(fast(0.2, "preferred"), fast(0.4, "standard"), slowKYC), openFinanceRequired())

	_, err := src.FetchProfile(context.Background(), "c1")

	require.NoError(t, err)
	assert.Eventually(t, func() bool { return slowKYC.inFlight.Load() == 0 }, 100*time.Millisecond, 5*time.Millisecond,
		"the slow call must stop right after its budget")
	assert.Equal(t, int32(1), slowKYC.canceled.Load())
}

func TestNoGoroutinesLeftAfterDeadline(t *testing.T) {
	sources := []*delayedSource{slowSource(), slowSource(), slowSource()}
	src := profiling.NewParallelSource(named(sources[0], sources[1], sources[2]), equivalent())
	before := runtime.NumGoroutine()

	const rounds = 10
	for i := 0; i < rounds; i++ {
		_, err := src.FetchProfile(context.Background(), fmt.Sprintf("c%d", i))
		require.Error(t, err)
	}

	// Polled by hand: assert.Eventually runs its condition in an extra goroutine.
	deadline := time.Now().Add(time.Second)
	for runtime.NumGoroutine() > before && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	assert.LessOrEqual(t, runtime.NumGoroutine(), before, "every source goroutine ends once its call is canceled")
	for _, s := range sources {
		assert.Zero(t, s.inFlight.Load())
		assert.Equal(t, int32(rounds), s.canceled.Load())
	}
}

func TestAllSourcesFailDegradesToHAD01Chain(t *testing.T) {
	boom := &delayedSource{err: fmt.Errorf("%w: boom", ports.ErrSourceUnavailable)}
	src := profiling.NewParallelSource(named(boom, boom, boom), equivalent())

	_, err := src.FetchProfile(context.Background(), "new")

	var unavailable *ports.SourcesUnavailableError
	require.ErrorAs(t, err, &unavailable)
	assert.Equal(t, map[string]domain.SourceStatus{"open_finance": "error", "open_data": "error", "kyc": "error"}, statuses(unavailable.Outcomes))

	res, rerr := newResolver(src, newFakeCache()).Resolve(context.Background(), "new", true)
	require.NoError(t, rerr)
	assert.Equal(t, domain.ModeProvisional, res.Mode)
	assert.Equal(t, domain.SourceBaseActuarial, res.Source)
}

func TestFailedRequiredSourceDoesNotCancelTheHealthyOnes(t *testing.T) {
	isolated := &delayedSource{err: fmt.Errorf("%w: %w", ports.ErrSourceUnavailable, ports.ErrCircuitOpen)}
	// Open Data and KYC answer after the required source already failed.
	od := &delayedSource{delay: 30 * time.Millisecond, score: 0.4, category: "standard"}
	kyc := &delayedSource{delay: 30 * time.Millisecond, score: 0.6, category: "standard"}
	src := profiling.NewParallelSource(named(isolated, od, kyc), openFinanceRequired())

	start := time.Now()
	_, err := src.FetchProfile(context.Background(), "c1")
	elapsed := time.Since(start)

	var unavailable *ports.SourcesUnavailableError
	require.ErrorAs(t, err, &unavailable, "without the required source the caller degrades")
	assert.Equal(t, map[string]domain.SourceStatus{"open_finance": "circuit_open", "open_data": "ok", "kyc": "ok"}, statuses(unavailable.Outcomes),
		"the healthy sources report their real result")
	assert.Zero(t, od.canceled.Load()+kyc.canceled.Load(), "a failed required source does not cancel the others")
	assert.Less(t, elapsed, testBudget, "the answer waits for the others only within their budget")
}

func TestFailedRequiredSourceWaitsAtMostTheBudget(t *testing.T) {
	isolated := &delayedSource{err: fmt.Errorf("%w: %w", ports.ErrSourceUnavailable, ports.ErrCircuitOpen)}
	od, kyc := slowSource(), slowSource()
	src := profiling.NewParallelSource(named(isolated, od, kyc), openFinanceRequired())

	start := time.Now()
	_, err := src.FetchProfile(context.Background(), "c1")
	elapsed := time.Since(start)

	var unavailable *ports.SourcesUnavailableError
	require.ErrorAs(t, err, &unavailable)
	assert.Less(t, elapsed, cutMargin, "the provisional answer is never later than the common budget")
	assert.Equal(t, domain.SourceStatusCircuitOpen, unavailable.Outcomes[0].Status)
	assert.Equal(t, domain.SourceStatusTimeout, unavailable.Outcomes[1].Status, "cut at its own budget")
	assert.Equal(t, domain.SourceStatusTimeout, unavailable.Outcomes[2].Status, "cut at its own budget")
	assert.GreaterOrEqual(t, unavailable.Outcomes[1].Latency, testBudget)
	assert.Equal(t, int32(1), od.canceled.Load())
	assert.Equal(t, int32(1), kyc.canceled.Load())
	assert.Eventually(t, func() bool { return od.inFlight.Load() == 0 && kyc.inFlight.Load() == 0 }, 100*time.Millisecond, 5*time.Millisecond)
}

func TestCircuitOpenOptionalSourceGivesPartialProfile(t *testing.T) {
	isolated := &delayedSource{err: fmt.Errorf("%w: %w", ports.ErrSourceUnavailable, ports.ErrCircuitOpen)}
	src := profiling.NewParallelSource(named(fast(0.2, "preferred"), isolated, fast(0.4, "standard")), openFinanceRequired())

	got, err := src.FetchProfile(context.Background(), "c1")

	require.NoError(t, err)
	assert.True(t, got.Partial)
	assert.Equal(t, domain.SourceStatusCircuitOpen, got.Sources[1].Status)
}

// --- Decision D-2: OPEN_FINANCE_REQUIRED --------------------------------------

func TestOpenFinanceMissingDegradesToProvisional(t *testing.T) {
	src := profiling.NewParallelSource(named(slowSource(), fast(0.4, "standard"), fast(0.6, "standard")), openFinanceRequired())

	t.Run("with a cached profile", func(t *testing.T) {
		cache := newFakeCache()
		cached := freshProfile()
		cached.CustomerID = "c1"
		cache.items["c1"] = cached

		start := time.Now()
		res, err := newResolver(src, cache).Resolve(context.Background(), "c1", true)

		require.NoError(t, err)
		assert.Less(t, time.Since(start), cutMargin)
		assert.Equal(t, domain.ModeProvisional, res.Mode)
		assert.Equal(t, domain.SourceCache, res.Source)
		assert.Equal(t, domain.ExternalSourceOpenFinance, res.UnavailableSource)
	})
	t.Run("without a cached profile", func(t *testing.T) {
		res, err := newResolver(src, newFakeCache()).Resolve(context.Background(), "new", true)

		require.NoError(t, err)
		assert.Equal(t, domain.ModeProvisional, res.Mode)
		assert.Equal(t, domain.SourceBaseActuarial, res.Source)
	})
}

func TestPartialProfileWhenOptionalSourceMissing(t *testing.T) {
	expiredKYC := slowSource()
	src := profiling.NewParallelSource(named(fast(0.2, "preferred"), fast(0.4, "standard"), expiredKYC), openFinanceRequired())

	res, err := newResolver(src, newFakeCache()).Resolve(context.Background(), "c1", true)

	require.NoError(t, err)
	assert.Equal(t, domain.ModeNormal, res.Mode, "a missing optional source gives a normal partial profile")
	assert.True(t, res.Profile.Partial)
	assert.InDelta(t, 0.3, res.Profile.Score, 1e-9)
	assert.Equal(t, domain.SourceStatusTimeout, res.Profile.Sources[2].Status)
}

func TestEquivalentSourcesNeedOnlyOneSignal(t *testing.T) {
	src := profiling.NewParallelSource(named(slowSource(), fast(0.4, "standard"), slowSource()), equivalent())

	got, err := src.FetchProfile(context.Background(), "c1")

	require.NoError(t, err)
	assert.True(t, got.Partial)
	assert.Equal(t, 0.4, got.Score)
	assert.Equal(t, "standard", got.Category, "category of the highest-priority source present")
}

func TestRequiredSourceArrivingLateStillWaitsForTheOthers(t *testing.T) {
	ofSlowButInBudget := &delayedSource{delay: 40 * time.Millisecond, score: 0.2, category: "preferred"}
	src := profiling.NewParallelSource(named(ofSlowButInBudget, fast(0.4, "standard"), fast(0.6, "standard")), openFinanceRequired())

	got, err := src.FetchProfile(context.Background(), "c1")

	require.NoError(t, err)
	assert.False(t, got.Partial)
	assert.Equal(t, "preferred", got.Category)
}

// --- Configuration and rule ---------------------------------------------------

func TestParallelSourceDefaultBudgetIs120ms(t *testing.T) {
	src := profiling.NewParallelSource(named(fast(0.2, "preferred"), fast(0.4, "standard"), slowSource()), profiling.ParallelConfig{})

	start := time.Now()
	got, err := src.FetchProfile(context.Background(), "c1")
	elapsed := time.Since(start)

	require.NoError(t, err)
	assert.True(t, got.Partial)
	assert.GreaterOrEqual(t, elapsed, profiling.DefaultSourceBudget)
	assert.Less(t, elapsed, cutMargin)
	assert.Equal(t, 120*time.Millisecond, profiling.DefaultSourceBudget)
	assert.Equal(t, 700*time.Millisecond, profiling.DefaultProfilingDeadline)
}

func TestParallelSourceDefaultDeadlineCutsALongBudget(t *testing.T) {
	verySlow := &delayedSource{delay: 2 * time.Second}
	src := profiling.NewParallelSource(named(verySlow, verySlow, verySlow), profiling.ParallelConfig{Budget: 5 * time.Second})

	start := time.Now()
	_, err := src.FetchProfile(context.Background(), "c1")
	elapsed := time.Since(start)

	require.Error(t, err)
	assert.GreaterOrEqual(t, elapsed, profiling.DefaultProfilingDeadline)
	assert.Less(t, elapsed, profiling.DefaultProfilingDeadline+cutMargin)
}

func TestSourceErrorIsReportedAsError(t *testing.T) {
	failing := &delayedSource{err: errors.New("bad gateway")}
	src := profiling.NewParallelSource(named(fast(0.2, "preferred"), failing, fast(0.4, "standard")), openFinanceRequired())

	got, err := src.FetchProfile(context.Background(), "c1")

	require.NoError(t, err)
	assert.Equal(t, domain.SourceStatusError, got.Sources[1].Status)
}

func TestCustomCombineRuleReplacesTheProvisionalOne(t *testing.T) {
	cfg := equivalent()
	cfg.Combine = func(id string, arrived []domain.RiskProfile) domain.RiskProfile {
		return domain.RiskProfile{CustomerID: id, Score: float64(len(arrived)), Category: "custom"}
	}
	src := profiling.NewParallelSource(named(fast(0.2, "preferred"), fast(0.4, "standard"), fast(0.6, "standard")), cfg)

	got, err := src.FetchProfile(context.Background(), "c1")

	require.NoError(t, err)
	assert.Equal(t, 3.0, got.Score)
	assert.Equal(t, "custom", got.Category)
	assert.Len(t, got.Sources, 3, "outcomes are added whatever the rule")
}

func TestCombineProvisional(t *testing.T) {
	older := now.Add(-time.Hour)
	got := profiling.CombineProvisional("c1", []domain.RiskProfile{
		{Score: 0.1, Category: "preferred", CalculatedAt: older},
		{Score: 0.5, Category: "standard", CalculatedAt: now},
	})

	assert.Equal(t, "c1", got.CustomerID)
	assert.InDelta(t, 0.3, got.Score, 1e-9)
	assert.Equal(t, "preferred", got.Category)
	assert.Equal(t, older, got.CalculatedAt)
}

func TestParallelSourceWithoutSourcesIsUnavailable(t *testing.T) {
	_, err := profiling.NewParallelSource(nil, equivalent()).FetchProfile(context.Background(), "c1")

	assert.ErrorIs(t, err, ports.ErrSourceUnavailable)
}

func TestCanceledCallerEndsTheFanOut(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	src := profiling.NewParallelSource(named(slowSource(), slowSource(), slowSource()), equivalent())
	time.AfterFunc(20*time.Millisecond, cancel)

	start := time.Now()
	_, err := newResolver(src, newFakeCache()).Resolve(ctx, "c1", true)

	require.ErrorIs(t, err, context.Canceled, "a canceled request is reported, not degraded")
	assert.Less(t, time.Since(start), 100*time.Millisecond)
}
