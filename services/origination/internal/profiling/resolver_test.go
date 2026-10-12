package profiling_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/profiling"
)

var now = time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)

type fakeClock struct{}

func (fakeClock) Now() time.Time { return now }

type fakeSource struct {
	mu      sync.Mutex
	profile domain.RiskProfile
	err     error
	calls   int
}

func (s *fakeSource) FetchProfile(_ context.Context, id string) (domain.RiskProfile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	if s.err != nil {
		return domain.RiskProfile{}, s.err
	}
	p := s.profile
	p.CustomerID = id
	return p, nil
}

type fakeCache struct {
	mu     sync.Mutex
	items  map[string]domain.RiskProfile
	getErr error
	setErr error
	gets   int

	invalidated []string
}

func newFakeCache() *fakeCache { return &fakeCache{items: map[string]domain.RiskProfile{}} }

func (c *fakeCache) Get(_ context.Context, id string) (domain.RiskProfile, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.gets++
	if c.getErr != nil {
		return domain.RiskProfile{}, false, c.getErr
	}
	p, ok := c.items[id]
	return p, ok, nil
}

func (c *fakeCache) Set(_ context.Context, p domain.RiskProfile) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.setErr != nil {
		return c.setErr
	}
	c.items[p.CustomerID] = p
	return nil
}

func (c *fakeCache) Invalidate(_ context.Context, id string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.invalidated = append(c.invalidated, id)
	delete(c.items, id)
	return nil
}

func freshProfile() domain.RiskProfile {
	return domain.RiskProfile{Score: 0.31, Category: "standard", CalculatedAt: now.Add(-time.Hour)}
}

// partialProfile is a cached profile calculated without some source: it is not
// reused as is, but it backs a provisional answer when the sources fail.
func partialProfile() domain.RiskProfile {
	p := freshProfile()
	p.Partial = true
	return p
}

// newReusingResolver reuses a complete cached profile, as main does by default.
func newReusingResolver(src ports.RiskProfileSource, cache ports.RiskProfileCache) *profiling.Resolver {
	return profiling.NewResolver(src, cache, fakeClock{}, profiling.WithCachedProfileReuse())
}

func newResolver(src ports.RiskProfileSource, cache ports.RiskProfileCache) *profiling.Resolver {
	return profiling.NewResolver(src, cache, fakeClock{})
}

func TestResolveNormalUsesSourceAndRefreshesCache(t *testing.T) {
	src := &fakeSource{profile: freshProfile()}
	cache := newFakeCache()

	got, err := newResolver(src, cache).Resolve(context.Background(), "c1", true)

	require.NoError(t, err)
	assert.Equal(t, domain.ModeNormal, got.Mode)
	assert.Equal(t, domain.SourceOpenFinance, got.Source)
	assert.Empty(t, got.UnavailableSource)
	assert.Equal(t, 0.31, got.Profile.Score)
	assert.Contains(t, cache.items, "c1", "a fresh profile must warm the cache")
}

func TestResolveSourceDownWithCachedProfileIsProvisionalFromCache(t *testing.T) {
	cache := newFakeCache()
	cached := freshProfile()
	cached.CustomerID = "c1"
	cache.items["c1"] = cached
	src := &fakeSource{err: ports.ErrSourceUnavailable}

	got, err := newResolver(src, cache).Resolve(context.Background(), "c1", true)

	require.NoError(t, err)
	assert.Equal(t, domain.ModeProvisional, got.Mode)
	assert.Equal(t, domain.SourceCache, got.Source)
	assert.Equal(t, domain.ExternalSourceOpenFinance, got.UnavailableSource)
	assert.Equal(t, cached.CalculatedAt, got.Profile.CalculatedAt, "the profile date must reach the client")
}

func TestResolveSourceDownForNewCustomerFallsBackToBaseProfile(t *testing.T) {
	src := &fakeSource{err: ports.ErrSourceUnavailable}

	got, err := newResolver(src, newFakeCache()).Resolve(context.Background(), "new", true)

	require.NoError(t, err)
	assert.Equal(t, domain.ModeProvisional, got.Mode)
	assert.Equal(t, domain.SourceBaseActuarial, got.Source)
	assert.Equal(t, domain.ExternalSourceOpenFinance, got.UnavailableSource)
	assert.Equal(t, domain.BaseActuarialProfile("new"), got.Profile)
	assert.True(t, got.Profile.CalculatedAt.IsZero())
}

func TestResolveCachedProfileAgeLimit(t *testing.T) {
	tests := map[string]struct {
		age        time.Duration
		wantSource domain.ProfileSource
	}{
		"just inside 30 days": {age: 30*24*time.Hour - time.Minute, wantSource: domain.SourceCache},
		"exactly 30 days":     {age: 30 * 24 * time.Hour, wantSource: domain.SourceCache},
		"older than 30 days":  {age: 30*24*time.Hour + time.Minute, wantSource: domain.SourceBaseActuarial},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			cache := newFakeCache()
			p := freshProfile()
			p.CustomerID = "c1"
			p.CalculatedAt = now.Add(-tc.age)
			cache.items["c1"] = p

			got, err := newResolver(&fakeSource{err: ports.ErrSourceUnavailable}, cache).Resolve(context.Background(), "c1", true)

			require.NoError(t, err)
			assert.Equal(t, tc.wantSource, got.Source)
			assert.Equal(t, domain.ModeProvisional, got.Mode)
		})
	}
}

func TestResolveWithoutConsentTouchesNeitherSourceNorCache(t *testing.T) {
	src := &fakeSource{profile: freshProfile()}
	cache := newFakeCache()

	_, err := newResolver(src, cache).Resolve(context.Background(), "c1", false)

	require.ErrorIs(t, err, profiling.ErrConsentRequired)
	assert.Zero(t, src.calls)
	assert.Zero(t, cache.gets)
	assert.Empty(t, cache.items)
}

func TestResolveCacheReadErrorDegradesToBaseProfile(t *testing.T) {
	cache := newFakeCache()
	cache.getErr = errors.New("redis down")

	got, err := newResolver(&fakeSource{err: ports.ErrSourceUnavailable}, cache).Resolve(context.Background(), "c1", true)

	require.NoError(t, err)
	assert.Equal(t, domain.SourceBaseActuarial, got.Source)
}

func TestResolveCacheWriteErrorDoesNotChangeTheAnswer(t *testing.T) {
	cache := newFakeCache()
	cache.setErr = errors.New("redis down")

	got, err := newResolver(&fakeSource{profile: freshProfile()}, cache).Resolve(context.Background(), "c1", true)

	require.NoError(t, err)
	assert.Equal(t, domain.ModeNormal, got.Mode)
}

func TestResolveCanceledContextIsReportedNotDegraded(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := newResolver(&fakeSource{err: context.Canceled}, newFakeCache()).Resolve(ctx, "c1", true)

	require.ErrorIs(t, err, context.Canceled)
}

func TestEveryRequestGetsAnAnswerDuringAnOutage(t *testing.T) {
	cache := newFakeCache()
	warm := freshProfile()
	warm.CustomerID = "warm"
	cache.items["warm"] = warm
	r := newResolver(&fakeSource{err: ports.ErrSourceUnavailable}, cache)

	const requests = 200
	var wg sync.WaitGroup
	results := make(chan domain.ProfileResolution, requests)
	errs := make(chan error, requests)
	for i := 0; i < requests; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := "warm"
			if i%2 == 0 {
				id = "new"
			}
			res, err := r.Resolve(context.Background(), id, true)
			if err != nil {
				errs <- err
				return
			}
			results <- res
		}(i)
	}
	wg.Wait()
	close(results)
	close(errs)

	assert.Empty(t, errs)
	count := 0
	for res := range results {
		count++
		assert.Equal(t, domain.ModeProvisional, res.Mode)
	}
	assert.Equal(t, requests, count, "no request may be lost")
}

// --- HA-L01: per-source outcomes ----------------------------------------------

func outcomesFixture() []domain.SourceOutcome {
	return []domain.SourceOutcome{
		{Source: domain.ExternalSourceOpenFinance, Status: domain.SourceStatusTimeout, Latency: 120 * time.Millisecond},
		{Source: domain.ExternalSourceOpenData, Status: domain.SourceStatusOK, Latency: 40 * time.Millisecond},
		{Source: domain.ExternalSourceKYC, Status: domain.SourceStatusCircuitOpen},
	}
}

func TestResolveCopiesSourceOutcomesWhenDegraded(t *testing.T) {
	outcomes := outcomesFixture()
	src := &fakeSource{err: &ports.SourcesUnavailableError{Outcomes: outcomes}}

	t.Run("from cache", func(t *testing.T) {
		cache := newFakeCache()
		cached := freshProfile()
		cached.CustomerID = "c1"
		cache.items["c1"] = cached

		got, err := newResolver(src, cache).Resolve(context.Background(), "c1", true)

		require.NoError(t, err)
		assert.Equal(t, domain.SourceCache, got.Source)
		assert.Equal(t, domain.ExternalSourceOpenFinance, got.UnavailableSource)
		assert.Equal(t, outcomes, got.Sources, "E2b shows which source failed")
	})
	t.Run("from base profile", func(t *testing.T) {
		got, err := newResolver(src, newFakeCache()).Resolve(context.Background(), "new", true)

		require.NoError(t, err)
		assert.Equal(t, domain.SourceBaseActuarial, got.Source)
		assert.Equal(t, outcomes, got.Sources)
	})
}

func TestResolveSingleSourceFailureHasNoOutcomes(t *testing.T) {
	got, err := newResolver(&fakeSource{err: ports.ErrSourceUnavailable}, newFakeCache()).Resolve(context.Background(), "new", true)

	require.NoError(t, err)
	assert.Empty(t, got.Sources)
}

func TestResolveNormalTakesTheSourceFromTheProfile(t *testing.T) {
	p := freshProfile()
	p.Partial = true
	p.Sources = []domain.SourceOutcome{
		{Source: domain.ExternalSourceOpenFinance, Status: domain.SourceStatusOK},
		{Source: domain.ExternalSourceOpenData, Status: domain.SourceStatusTimeout},
		{Source: domain.ExternalSourceKYC, Status: domain.SourceStatusOK},
	}

	got, err := newResolver(&fakeSource{profile: p}, newFakeCache()).Resolve(context.Background(), "c1", true)

	require.NoError(t, err)
	assert.Equal(t, domain.ModeNormal, got.Mode)
	assert.Equal(t, domain.SourceExternal, got.Source)
	assert.True(t, got.Profile.Partial)
	assert.Equal(t, p.Sources, got.Sources)
}

// --- HU-W1.2-02: reuse of the cached profile ------------------------------------

func completeProfile(age time.Duration) domain.RiskProfile {
	p := freshProfile()
	p.CustomerID = "c1"
	p.CalculatedAt = now.Add(-age)
	return p
}

func TestResolveReusesACompleteCachedProfileWithoutQueryingTheSource(t *testing.T) {
	cache := newFakeCache()
	cache.items["c1"] = completeProfile(48 * time.Hour)
	src := &fakeSource{err: ports.ErrSourceUnavailable}

	got, err := newReusingResolver(src, cache).Resolve(context.Background(), "c1", true)

	require.NoError(t, err)
	assert.Equal(t, domain.ModeNormal, got.Mode)
	assert.Equal(t, domain.SourceCache, got.Source)
	assert.Empty(t, got.UnavailableSource)
	assert.Equal(t, cache.items["c1"], got.Profile)
	assert.Zero(t, src.calls, "no query to the external ecosystem")
}

func TestResolveCompleteCachedProfileAgeLimit(t *testing.T) {
	tests := map[string]struct {
		age         time.Duration
		wantSource  domain.ProfileSource
		wantQueried bool
	}{
		"just inside 30 days": {age: 30*24*time.Hour - time.Minute, wantSource: domain.SourceCache},
		"exactly 30 days":     {age: 30 * 24 * time.Hour, wantSource: domain.SourceCache},
		"older than 30 days":  {age: 30*24*time.Hour + time.Minute, wantSource: domain.SourceOpenFinance, wantQueried: true},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			cache := newFakeCache()
			cache.items["c1"] = completeProfile(tc.age)
			src := &fakeSource{profile: freshProfile()}

			got, err := newReusingResolver(src, cache).Resolve(context.Background(), "c1", true)

			require.NoError(t, err)
			assert.Equal(t, tc.wantSource, got.Source)
			assert.Equal(t, tc.wantQueried, src.calls > 0)
		})
	}
}

func TestResolveQueriesTheSourcesAgainWhenTheCachedProfileIsPartial(t *testing.T) {
	cache := newFakeCache()
	cached := partialProfile()
	cached.CustomerID = "c1"
	cache.items["c1"] = cached
	fresh := freshProfile()
	src := &fakeSource{profile: fresh}

	got, err := newReusingResolver(src, cache).Resolve(context.Background(), "c1", true)

	require.NoError(t, err)
	assert.Equal(t, 1, src.calls, "a partial profile is not reused: the customer stays degraded until the sources answer")
	assert.Equal(t, domain.ModeNormal, got.Mode)
	assert.Equal(t, domain.SourceOpenFinance, got.Source)
	assert.False(t, cache.items["c1"].Partial, "the complete profile replaces the partial one")
}

func TestResolveCacheReadErrorQueriesTheSource(t *testing.T) {
	cache := newFakeCache()
	cache.getErr = errors.New("redis down")
	src := &fakeSource{profile: freshProfile()}

	got, err := newReusingResolver(src, cache).Resolve(context.Background(), "c1", true)

	require.NoError(t, err)
	assert.Equal(t, domain.ModeNormal, got.Mode)
	assert.Equal(t, 1, src.calls)
}

func TestInvalidateMakesTheNextResolutionQueryTheSource(t *testing.T) {
	cache := newFakeCache()
	cache.items["c1"] = completeProfile(time.Hour)
	src := &fakeSource{profile: freshProfile()}
	r := newReusingResolver(src, cache)

	require.NoError(t, r.Invalidate(context.Background(), "c1"))
	got, err := r.Resolve(context.Background(), "c1", true)

	require.NoError(t, err)
	assert.Equal(t, []string{"c1"}, cache.invalidated)
	assert.Equal(t, 1, src.calls, "after a requote the sources are queried again")
	assert.Equal(t, domain.SourceOpenFinance, got.Source)
}

// blockingSource answers only when released, so concurrent callers pile up.
type blockingSource struct {
	release chan struct{}
	calls   atomic.Int32
}

func (s *blockingSource) FetchProfile(_ context.Context, id string) (domain.RiskProfile, error) {
	s.calls.Add(1)
	<-s.release
	p := freshProfile()
	p.CustomerID = id
	return p, nil
}

func TestResolveConcurrentRequestsForTheSameCustomerShareOneCalculation(t *testing.T) {
	src := &blockingSource{release: make(chan struct{})}
	r := newReusingResolver(src, newFakeCache())

	const callers = 20
	var wg sync.WaitGroup
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, err := r.Resolve(context.Background(), "c1", true)
			assert.NoError(t, err)
			assert.Equal(t, domain.ModeNormal, got.Mode)
		}()
	}
	// The callers join the in-flight calculation before it is released.
	require.Eventually(t, func() bool { return src.calls.Load() >= 1 }, time.Second, time.Millisecond)
	time.Sleep(50 * time.Millisecond)
	close(src.release)
	wg.Wait()

	assert.Equal(t, int32(1), src.calls.Load(), "one query to the sources, not one per caller")
}

func TestResolveACallerThatGivesUpDoesNotFailTheOthers(t *testing.T) {
	src := &blockingSource{release: make(chan struct{})}
	r := newReusingResolver(src, newFakeCache())
	quitting, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() {
		_, err := r.Resolve(quitting, "c1", true)
		done <- err
	}()
	require.Eventually(t, func() bool { return src.calls.Load() == 1 }, time.Second, time.Millisecond)
	stayed := make(chan domain.ProfileResolution, 1)
	go func() {
		got, _ := r.Resolve(context.Background(), "c1", true)
		stayed <- got
	}()
	time.Sleep(50 * time.Millisecond)

	cancel()
	assert.ErrorIs(t, <-done, context.Canceled)
	close(src.release)

	assert.Equal(t, domain.ModeNormal, (<-stayed).Mode)
}

func TestResolveWithoutReuseAlwaysQueriesTheSourceFirst(t *testing.T) {
	cache := newFakeCache()
	cache.items["c1"] = completeProfile(time.Hour)
	src := &fakeSource{err: ports.ErrSourceUnavailable}

	got, err := newResolver(src, cache).Resolve(context.Background(), "c1", true)

	require.NoError(t, err)
	assert.Equal(t, 1, src.calls, "HA-D01 / HA-L01 behavior: the cache only backs a provisional answer")
	assert.Equal(t, domain.ModeProvisional, got.Mode)
	assert.Equal(t, domain.SourceCache, got.Source)
}
