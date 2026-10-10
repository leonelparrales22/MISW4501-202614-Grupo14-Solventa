package profiling_test

import (
	"context"
	"errors"
	"sync"
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

func freshProfile() domain.RiskProfile {
	return domain.RiskProfile{Score: 0.31, Category: "standard", CalculatedAt: now.Add(-time.Hour)}
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
