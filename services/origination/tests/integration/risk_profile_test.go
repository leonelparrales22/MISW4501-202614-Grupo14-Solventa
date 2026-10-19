package integration_test

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	redisadapter "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/redis"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/profiling"
)

// newActuarialEnv wires the service as main does: the actuarial weights, the
// reuse of a complete cached profile and a Redis cache on miniredis, which the
// test can also read.
func newActuarialEnv(t *testing.T, openFinanceRequired bool) (*parallelEnv, *redisadapter.ProfileCache) {
	t.Helper()
	mr := miniredis.RunT(t)
	rc := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rc.Close() })
	cache := redisadapter.NewProfileCache(rc, 0, 0)
	e := buildParallelEnv(t, openFinanceRequired, cache, productionBreaker(), func(c *profiling.ParallelConfig, o *[]profiling.Option) {
		c.Weights = profiling.DefaultWeights()
		*o = append(*o, profiling.WithCachedProfileReuse())
	})
	return e, cache
}

func (e *parallelEnv) hits() int32 { return e.of.hits.Load() + e.od.hits.Load() + e.kyc.hits.Load() }

func TestScoreUsesTheActuarialWeights(t *testing.T) {
	e, _ := newActuarialEnv(t, true)

	res := e.resolve(t, "c1")

	require.Equal(t, http.StatusOK, res.status)
	assert.Equal(t, "normal", res.body["mode"])
	assert.InDelta(t, 0.5*0.2+0.3*0.4+0.2*0.6, res.body["profile"].(map[string]any)["score"], 1e-9)
	assert.Equal(t, "standard", res.body["profile"].(map[string]any)["category"])
}

func TestCompleteProfileIsReusedWithoutQueryingTheEcosystem(t *testing.T) {
	e, _ := newActuarialEnv(t, true)
	require.Equal(t, "normal", e.resolve(t, "c1").body["mode"])
	hits := e.hits()

	res := e.resolve(t, "c1")

	assert.Equal(t, "normal", res.body["mode"])
	assert.Equal(t, "cache", res.body["source"])
	assert.Equal(t, false, res.body["partial"])
	assert.Nil(t, res.body["notice"])
	assert.Equal(t, hits, e.hits(), "criterion 1: no query may reach the external ecosystem")
	assert.NotNil(t, res.body["profile_date"])
}

func TestCompleteProfileIsReusedEvenWhenEverySourceIsDown(t *testing.T) {
	e, _ := newActuarialEnv(t, true)
	require.Equal(t, "normal", e.resolve(t, "c1").body["mode"])
	e.of.down.Store(true)
	e.od.down.Store(true)
	e.kyc.down.Store(true)

	res := e.resolve(t, "c1")

	assert.Equal(t, "normal", res.body["mode"])
	assert.Equal(t, "cache", res.body["source"])
}

func TestPartialProfileIsNotReusedAndTheCustomerRecoversWhenTheSourceDoes(t *testing.T) {
	e, cache := newActuarialEnv(t, true)
	e.od.down.Store(true)

	first := e.resolve(t, "c1")
	require.Equal(t, true, first.body["partial"], "Open Data is missing")
	cached, found, err := cache.Get(context.Background(), "c1")
	require.NoError(t, err)
	require.True(t, found)
	assert.True(t, cached.Partial, "the partial flag survives the cache")

	e.od.down.Store(false)
	hits := e.hits()
	second := e.resolve(t, "c1")

	assert.Greater(t, e.hits(), hits, "a partial profile is not reused: the sources are queried again")
	assert.Equal(t, false, second.body["partial"])
	assert.Equal(t, "external_sources", second.body["source"])
	assert.InDelta(t, 0.5*0.2+0.3*0.4+0.2*0.6, second.body["profile"].(map[string]any)["score"], 1e-9)
}

func TestPartialCachedProfileBacksAProvisionalAnswerWhenOpenFinanceFails(t *testing.T) {
	e, _ := newActuarialEnv(t, true)
	e.od.down.Store(true)
	require.Equal(t, true, e.resolve(t, "c1").body["partial"])
	e.of.down.Store(true)

	res := e.resolve(t, "c1")

	require.Equal(t, http.StatusOK, res.status)
	assert.Equal(t, "provisional", res.body["mode"])
	assert.Equal(t, "cache", res.body["source"])
	assert.Equal(t, "open_finance", res.body["unavailable_source"])
}

func TestRequoteInvalidatesTheProfileAndTheSourcesAreQueriedAgain(t *testing.T) {
	e, _ := newActuarialEnv(t, true)
	require.Equal(t, "external_sources", e.resolve(t, "c1").body["source"])
	require.Equal(t, "cache", e.resolve(t, "c1").body["source"])

	require.NoError(t, e.resolver.Invalidate(context.Background(), "c1"))
	hits := e.hits()
	res := e.resolve(t, "c1")

	assert.Equal(t, "external_sources", res.body["source"])
	assert.Equal(t, hits+3, e.hits(), "the three sources are queried again")
}

func TestSimultaneousRequestsForOneCustomerQueryEachSourceOnce(t *testing.T) {
	e, _ := newActuarialEnv(t, true)
	for _, p := range []*latencyProvider{e.of, e.od, e.kyc} {
		p.setDelay(60 * time.Millisecond)
	}

	const callers = 20
	var wg sync.WaitGroup
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res := e.resolve(t, "c1")
			assert.Equal(t, http.StatusOK, res.status)
			assert.Equal(t, "normal", res.body["mode"])
		}()
	}
	wg.Wait()

	// A caller arriving after the first calculation ended reads the cache.
	for _, p := range []*latencyProvider{e.of, e.od, e.kyc} {
		assert.Equal(t, int32(1), p.hits.Load(), fmt.Sprintf("%d callers share one query", callers))
	}
}

func TestWithoutConsentNeitherTheSourcesNorTheCacheAreTouched(t *testing.T) {
	e, cache := newActuarialEnv(t, true)
	require.Equal(t, "normal", e.resolve(t, "c1").body["mode"])
	hits := e.hits()

	payloadRes := e.resolveWithConsent(t, "c1", false)

	assert.Equal(t, http.StatusUnprocessableEntity, payloadRes.status)
	assert.Equal(t, hits, e.hits())
	_, found, err := cache.Get(context.Background(), "c1")
	require.NoError(t, err)
	assert.True(t, found, "the cached profile stays; it just is not used")
}
