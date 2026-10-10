// Package integration_test wires the real Open Finance client, circuit
// breaker, Redis cache adapter, resolver and HTTP handler against a
// controllable provider, and checks the HA-D01 acceptance criteria.
package integration_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/adapters/openfinance"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/resilience"
	httpadapter "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/http"
	ofadapter "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/openfinance"
	redisadapter "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/redis"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/profiling"
)

const (
	providerTimeout = 100 * time.Millisecond // stands in for the 700 ms production cut
	openWindow      = 150 * time.Millisecond // stands in for the 5 s production window
)

type providerMode int32

const (
	providerHealthy providerMode = iota
	providerDown
	providerSlow
)

// provider is a controllable Open Finance double.
type provider struct {
	mode atomic.Int32
	hits atomic.Int32
	srv  *httptest.Server
}

func newProvider(t *testing.T) *provider {
	t.Helper()
	p := &provider{}
	p.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.hits.Add(1)
		switch providerMode(p.mode.Load()) {
		case providerDown:
			w.WriteHeader(http.StatusServiceUnavailable)
		case providerSlow:
			select {
			case <-time.After(5 * providerTimeout):
			case <-r.Context().Done():
			}
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{
				"customer_id": "ignored", "score": 0.27, "category": "standard",
				"updated_at": time.Now().UTC().Format(time.RFC3339),
			})
		}
	}))
	t.Cleanup(p.srv.Close)
	return p
}

func (p *provider) set(m providerMode) { p.mode.Store(int32(m)) }

type env struct {
	api      http.Handler
	provider *provider
	cache    *redisadapter.ProfileCache
}

func newEnv(t *testing.T) *env {
	t.Helper()
	prov := newProvider(t)
	mr := miniredis.RunT(t)
	rc := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rc.Close() })
	cache := redisadapter.NewProfileCache(rc, 0, 0)

	client := openfinance.NewClient(openfinance.Config{
		BaseURL: prov.srv.URL,
		Timeout: providerTimeout,
		Breaker: resilience.Config{ConsecutiveFailures: 3, OpenTimeout: openWindow, HalfOpenSuccesses: 3},
	})
	resolver := profiling.NewResolver(ofadapter.NewSource(client), cache, realClock{})
	return &env{api: httpadapter.NewRouter(resolver), provider: prov, cache: cache}
}

type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

type result struct {
	status int
	body   map[string]any
}

func (e *env) resolve(customerID string, consent bool) result {
	payload, _ := json.Marshal(map[string]any{"customer_id": customerID, "consent_active": consent})
	req := httptest.NewRequest(http.MethodPost, "/v1/risk-profiles/resolve", bytes.NewReader(payload))
	rec := httptest.NewRecorder()
	e.api.ServeHTTP(rec, req)
	var body map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	return result{status: rec.Code, body: body}
}

func TestHealthyProviderGivesNormalOfferAndWarmsTheCache(t *testing.T) {
	e := newEnv(t)

	res := e.resolve("c1", true)

	require.Equal(t, http.StatusOK, res.status)
	assert.Equal(t, "normal", res.body["mode"])
	assert.Equal(t, "open_finance", res.body["source"])
	cached, found, err := e.cache.Get(context.Background(), "c1")
	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, 0.27, cached.Score)
}

func TestOutageWithCachedProfileGivesProvisionalOfferFromCache(t *testing.T) {
	e := newEnv(t)
	require.Equal(t, "normal", e.resolve("c1", true).body["mode"])

	e.provider.set(providerDown)
	res := e.resolve("c1", true)

	require.Equal(t, http.StatusOK, res.status)
	assert.Equal(t, "provisional", res.body["mode"])
	assert.Equal(t, "cache", res.body["source"])
	assert.Equal(t, "open_finance", res.body["unavailable_source"])
	assert.NotNil(t, res.body["profile_date"], "E3b shows the date of the applied profile")
	assert.Equal(t, httpadapter.NoticeLastProfileInUse, res.body["notice"])
}

func TestOutageForNewCustomerGivesProvisionalOfferFromBaseProfile(t *testing.T) {
	e := newEnv(t)
	e.provider.set(providerDown)

	res := e.resolve("brand-new", true)

	require.Equal(t, http.StatusOK, res.status)
	assert.Equal(t, "provisional", res.body["mode"])
	assert.Equal(t, "base_actuarial", res.body["source"])
	assert.Nil(t, res.body["profile_date"])
	assert.Equal(t, httpadapter.NoticePersonalizationUnavailable, res.body["notice"])
}

func TestCachedProfileOlderThan30DaysIsNotUsed(t *testing.T) {
	e := newEnv(t)
	old := domain.RiskProfile{CustomerID: "c1", Score: 0.2, Category: "preferred", CalculatedAt: time.Now().Add(-31 * 24 * time.Hour)}
	require.NoError(t, e.cache.Set(context.Background(), old))
	e.provider.set(providerDown)

	res := e.resolve("c1", true)

	assert.Equal(t, "base_actuarial", res.body["source"])
}

func TestSlowProviderIsCutAtTheTimeoutAndIsolated(t *testing.T) {
	e := newEnv(t)
	require.Equal(t, "normal", e.resolve("c1", true).body["mode"])
	e.provider.set(providerSlow)

	for i := 0; i < 3; i++ {
		start := time.Now()
		res := e.resolve("c1", true)
		assert.Equal(t, http.StatusOK, res.status)
		assert.Equal(t, "provisional", res.body["mode"])
		assert.Less(t, time.Since(start), 3*providerTimeout, "the slow provider must be cut near the timeout")
	}

	before := e.provider.hits.Load()
	res := e.resolve("c1", true)
	assert.Equal(t, "provisional", res.body["mode"])
	assert.Equal(t, before, e.provider.hits.Load(), "the open circuit must isolate the provider")
}

func TestRecoveryReturnsToNormalOnlyThroughHalfOpenProbes(t *testing.T) {
	e := newEnv(t)
	e.provider.set(providerDown)
	for i := 0; i < 3; i++ {
		require.Equal(t, "provisional", e.resolve("c1", true).body["mode"])
	}

	e.provider.set(providerHealthy)
	assert.Equal(t, "provisional", e.resolve("c1", true).body["mode"], "circuit still open inside the window")

	time.Sleep(openWindow + 50*time.Millisecond)
	for i := 0; i < 3; i++ {
		assert.Equal(t, "normal", e.resolve("c1", true).body["mode"], "half-open probe %d", i+1)
	}
	assert.Equal(t, "normal", e.resolve("c1", true).body["mode"])
}

func TestEveryRequestGetsAnAnswerDuringAnOutage(t *testing.T) {
	e := newEnv(t)
	for i := 0; i < 5; i++ {
		require.Equal(t, http.StatusOK, e.resolve(fmt.Sprintf("warm-%d", i), true).status)
	}
	e.provider.set(providerDown)

	const requests = 200
	var wg sync.WaitGroup
	var ok, provisional atomic.Int32
	for i := 0; i < requests; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := fmt.Sprintf("warm-%d", i%5)
			if i%2 == 0 {
				id = fmt.Sprintf("new-%d", i)
			}
			res := e.resolve(id, true)
			if res.status == http.StatusOK {
				ok.Add(1)
			}
			if res.body["mode"] == "provisional" {
				provisional.Add(1)
			}
		}(i)
	}
	wg.Wait()

	assert.Equal(t, int32(requests), ok.Load(), "100%% of the requests must be answered")
	assert.Equal(t, int32(requests), provisional.Load(), "every answer during the outage is provisional")
}

func TestWithoutConsentNothingExternalIsTouched(t *testing.T) {
	e := newEnv(t)

	res := e.resolve("c1", false)

	assert.Equal(t, http.StatusUnprocessableEntity, res.status)
	assert.Equal(t, "consent_required", res.body["error"])
	assert.Zero(t, e.provider.hits.Load())
	_, found, err := e.cache.Get(context.Background(), "c1")
	require.NoError(t, err)
	assert.False(t, found)
}
