// Integration tests of HA-L01: the real Open Finance, Open Data and KYC
// clients (shared connection pool, one circuit breaker each), the parallel
// profiling, the resolver with the Redis cache and the HTTP handler, against
// three controllable providers. They check the per-source budget, the total
// cut, the cancellation of the slow source and the partial profile.
package integration_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/adapters/kyc"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/adapters/opendata"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/adapters/openfinance"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/resilience"
	httpadapter "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/http"
	kycadapter "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/kyc"
	odadapter "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/opendata"
	ofadapter "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/openfinance"
	redisadapter "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/redis"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/profiling"
)

const (
	sourceBudget      = 120 * time.Millisecond // CA-2, production value
	profilingDeadline = 700 * time.Millisecond // CA-2, production value
	slowDelay         = 2 * time.Second        // far above the budget
	answerLimit       = 300 * time.Millisecond // "the client does not wait": budget + wide margin
)

// latencyProvider is a controllable external source that counts in-flight
// requests and open connections, like the connection counter of EXP-L01.
type latencyProvider struct {
	score     float64
	delay     atomic.Int64
	down      atomic.Bool
	hits      atomic.Int32
	inFlight  atomic.Int32
	openConns atomic.Int32
	srv       *httptest.Server
}

func newLatencyProvider(t *testing.T, score float64) *latencyProvider {
	t.Helper()
	p := &latencyProvider{score: score}
	p.srv = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.hits.Add(1)
		p.inFlight.Add(1)
		defer p.inFlight.Add(-1)
		if p.down.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		if d := time.Duration(p.delay.Load()); d > 0 {
			timer := time.NewTimer(d)
			defer timer.Stop()
			select {
			case <-timer.C:
			case <-r.Context().Done():
				return
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"customer_id": "ignored", "score": p.score, "category": "standard",
			"updated_at": time.Now().UTC().Format(time.RFC3339),
		})
	}))
	p.srv.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		switch state {
		case http.StateNew:
			p.openConns.Add(1)
		case http.StateClosed, http.StateHijacked:
			p.openConns.Add(-1)
		}
	}
	p.srv.Start()
	t.Cleanup(p.srv.Close)
	return p
}

func (p *latencyProvider) setDelay(d time.Duration) { p.delay.Store(int64(d)) }

type parallelEnv struct {
	api         http.Handler
	httpClient  *http.Client
	of, od, kyc *latencyProvider
}

// newParallelEnv wires the service as main does, with the production budgets,
// a short circuit-breaker window and the Redis cache on miniredis.
func newParallelEnv(t *testing.T, openFinanceRequired bool) *parallelEnv {
	t.Helper()
	mr := miniredis.RunT(t)
	rc := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rc.Close() })
	return newParallelEnvWithCache(t, openFinanceRequired, redisadapter.NewProfileCache(rc, 0, 0))
}

func newParallelEnvWithCache(t *testing.T, openFinanceRequired bool, cache ports.RiskProfileCache) *parallelEnv {
	t.Helper()
	e := &parallelEnv{
		of:  newLatencyProvider(t, 0.2),
		od:  newLatencyProvider(t, 0.4),
		kyc: newLatencyProvider(t, 0.6),
		httpClient: &http.Client{Transport: &http.Transport{
			MaxConnsPerHost: 200, MaxIdleConnsPerHost: 200, MaxIdleConns: 600,
		}},
	}
	t.Cleanup(e.httpClient.CloseIdleConnections)
	breaker := resilience.Config{
		ConsecutiveFailures: 3, OpenTimeout: openWindow, HalfOpenSuccesses: 3,
		IgnoreCallerCancellation: true, // D-3 (b)
	}
	required := ""
	if openFinanceRequired {
		required = domain.ExternalSourceOpenFinance
	}
	source := profiling.NewParallelSource([]profiling.NamedSource{
		{Name: domain.ExternalSourceOpenFinance, Source: ofadapter.NewSource(openfinance.NewClient(openfinance.Config{
			BaseURL: e.of.srv.URL, Breaker: breaker, HTTPClient: e.httpClient,
		}))},
		{Name: domain.ExternalSourceOpenData, Source: odadapter.NewSource(opendata.NewClient(opendata.Config{
			BaseURL: e.od.srv.URL, Breaker: breaker, HTTPClient: e.httpClient,
		}))},
		{Name: domain.ExternalSourceKYC, Source: kycadapter.NewSource(kyc.NewClient(kyc.Config{
			BaseURL: e.kyc.srv.URL, Breaker: breaker, HTTPClient: e.httpClient,
		}))},
	}, profiling.ParallelConfig{Budget: sourceBudget, Deadline: profilingDeadline, Required: required})

	resolver := profiling.NewResolver(source, cache, realClock{})
	e.api = httpadapter.NewRouter(resolver)
	return e
}

// memoryCache is an in-process cache double. It keeps the goroutine count of
// the leak test free from the Redis connection pool.
type memoryCache struct {
	mu    sync.Mutex
	items map[string]domain.RiskProfile
}

func (c *memoryCache) Get(_ context.Context, id string) (domain.RiskProfile, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	p, ok := c.items[id]
	return p, ok, nil
}

func (c *memoryCache) Set(_ context.Context, p domain.RiskProfile) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.items[p.CustomerID] = p
	return nil
}

type timedResult struct {
	result
	elapsed time.Duration
}

func (e *parallelEnv) resolve(t *testing.T, customerID string) timedResult {
	t.Helper()
	payload, _ := json.Marshal(map[string]any{"customer_id": customerID, "consent_active": true})
	req := httptest.NewRequest(http.MethodPost, "/v1/risk-profiles/resolve", bytes.NewReader(payload))
	rec := httptest.NewRecorder()
	start := time.Now()
	e.api.ServeHTTP(rec, req)
	elapsed := time.Since(start)
	var body map[string]any
	assert.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body)) // assert: also called from goroutines
	return timedResult{result: result{status: rec.Code, body: body}, elapsed: elapsed}
}

// sourceStates returns name -> {status, latency_ms} from the response.
func sourceStates(t *testing.T, body map[string]any) map[string]map[string]any {
	t.Helper()
	raw, ok := body["sources"].([]any)
	require.True(t, ok, "sources must be an array")
	out := map[string]map[string]any{}
	for _, item := range raw {
		s := item.(map[string]any)
		out[s["name"].(string)] = s
	}
	return out
}

func TestAllSourcesHealthyGiveACompleteProfile(t *testing.T) {
	e := newParallelEnv(t, true)

	res := e.resolve(t, "c1")

	require.Equal(t, http.StatusOK, res.status)
	assert.Equal(t, "normal", res.body["mode"])
	assert.Equal(t, "external_sources", res.body["source"])
	assert.Equal(t, false, res.body["partial"])
	assert.InDelta(t, 0.4, res.body["profile"].(map[string]any)["score"], 1e-9)
	for name, s := range sourceStates(t, res.body) {
		assert.Equal(t, "ok", s["status"], name)
		assert.Less(t, s["latency_ms"], float64(sourceBudget.Milliseconds()), name)
	}
}

func TestSlowSourceIsCutAtItsBudget(t *testing.T) {
	e := newParallelEnv(t, true)
	e.od.setDelay(400 * time.Millisecond)

	res := e.resolve(t, "c1")

	require.Equal(t, http.StatusOK, res.status)
	assert.GreaterOrEqual(t, res.elapsed, sourceBudget)
	assert.Less(t, res.elapsed, answerLimit, "CA-2: the slow call is cut at 120 ms")
	od := sourceStates(t, res.body)["open_data"]
	assert.Equal(t, "timeout", od["status"])
	assert.GreaterOrEqual(t, od["latency_ms"], float64(sourceBudget.Milliseconds()))
	assert.Less(t, od["latency_ms"], float64(answerLimit.Milliseconds()))
}

func TestSlowProviderGivesAPartialProfileAndTheClientDoesNotWait(t *testing.T) {
	e := newParallelEnv(t, true)
	e.kyc.setDelay(slowDelay)

	for i := 0; i < 5; i++ {
		res := e.resolve(t, fmt.Sprintf("c%d", i))

		require.Equal(t, http.StatusOK, res.status)
		assert.Less(t, res.elapsed, answerLimit, "CA-3: the client never waits for the slow source")
		assert.Equal(t, "normal", res.body["mode"])
		assert.Equal(t, true, res.body["partial"], "CA-3: marked as partial")
		assert.InDelta(t, 0.3, res.body["profile"].(map[string]any)["score"], 1e-9, "calculated with the signals that arrived")
		assert.Equal(t, "timeout", sourceStates(t, res.body)["kyc"]["status"])
	}
}

func TestNoGoroutinesOrConnectionsLeftAfterDeadline(t *testing.T) {
	e := newParallelEnvWithCache(t, true, &memoryCache{items: map[string]domain.RiskProfile{}})
	e.resolve(t, "warm-up")
	e.httpClient.CloseIdleConnections()
	baseline := settledGoroutines(runtime.NumGoroutine())
	e.od.setDelay(slowDelay)
	e.kyc.setDelay(slowDelay)

	const requests = 20
	var wg sync.WaitGroup
	for i := 0; i < requests; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			res := e.resolve(t, fmt.Sprintf("c%d", i))
			assert.Less(t, res.elapsed, answerLimit)
			assert.Equal(t, true, res.body["partial"])
		}(i)
	}
	wg.Wait()

	// The canceled calls end at the providers and close their connections:
	// no work is abandoned and no connection stays open.
	waitFor(t, func() bool { return e.od.inFlight.Load() == 0 && e.kyc.inFlight.Load() == 0 }, "requests still running at the slow providers")
	waitFor(t, func() bool { return e.od.openConns.Load() == 0 && e.kyc.openConns.Load() == 0 }, "connections to the slow providers still open")
	assert.Equal(t, int32(requests+1), e.od.hits.Load())

	e.httpClient.CloseIdleConnections()
	waitFor(t, func() bool { return runtime.NumGoroutine() <= baseline }, "goroutines left behind by the fan-out")
}

func TestOpenFinanceRequiredDegradesWhenItIsSlow(t *testing.T) {
	e := newParallelEnv(t, true)
	require.Equal(t, "normal", e.resolve(t, "c1").body["mode"], "warms the profile cache")
	e.of.setDelay(400 * time.Millisecond)

	res := e.resolve(t, "c1")

	require.Equal(t, http.StatusOK, res.status)
	assert.Less(t, res.elapsed, answerLimit)
	assert.Equal(t, "provisional", res.body["mode"], "D-2 (OPEN_FINANCE_REQUIRED=true)")
	assert.Equal(t, "cache", res.body["source"])
	assert.Equal(t, "open_finance", res.body["unavailable_source"])
	assert.Equal(t, false, res.body["partial"])
	states := sourceStates(t, res.body)
	assert.Equal(t, "timeout", states["open_finance"]["status"], "E2b shows which source failed")

	fresh := e.resolve(t, "new-customer")
	assert.Equal(t, "base_actuarial", fresh.body["source"])
}

func TestOpenFinanceNotRequiredGivesAPartialProfile(t *testing.T) {
	e := newParallelEnv(t, false)
	e.of.setDelay(400 * time.Millisecond)

	res := e.resolve(t, "c1")

	require.Equal(t, http.StatusOK, res.status)
	assert.Less(t, res.elapsed, answerLimit)
	assert.Equal(t, "normal", res.body["mode"], "D-2 (OPEN_FINANCE_REQUIRED=false)")
	assert.Equal(t, "external_sources", res.body["source"])
	assert.Equal(t, true, res.body["partial"])
	assert.InDelta(t, 0.5, res.body["profile"].(map[string]any)["score"], 1e-9)
}

func TestAllSourcesDownDegradeToTheHAD01Chain(t *testing.T) {
	e := newParallelEnv(t, false)
	e.of.down.Store(true)
	e.od.down.Store(true)
	e.kyc.down.Store(true)

	res := e.resolve(t, "brand-new")

	require.Equal(t, http.StatusOK, res.status)
	assert.Equal(t, "provisional", res.body["mode"])
	assert.Equal(t, "base_actuarial", res.body["source"])
	assert.Equal(t, httpadapter.NoticePersonalizationUnavailable, res.body["notice"])
	for name, s := range sourceStates(t, res.body) {
		assert.Equal(t, "error", s["status"], name)
	}
}

func TestBudgetDoesNotOpenTheCircuitButProviderErrorsDo(t *testing.T) {
	e := newParallelEnv(t, true)

	e.od.setDelay(400 * time.Millisecond)
	for i := 0; i < 5; i++ {
		assert.Equal(t, "timeout", sourceStates(t, e.resolve(t, "c1").body)["open_data"]["status"])
	}
	assert.Equal(t, "timeout", sourceStates(t, e.resolve(t, "c1").body)["open_data"]["status"],
		"D-3 (b): cuts by the budget never isolate the source")

	e.od.setDelay(0)
	e.od.down.Store(true)
	for i := 0; i < 3; i++ {
		assert.Equal(t, "error", sourceStates(t, e.resolve(t, "c1").body)["open_data"]["status"])
	}
	before := e.od.hits.Load()
	res := e.resolve(t, "c1")
	assert.Equal(t, "circuit_open", sourceStates(t, res.body)["open_data"]["status"])
	assert.Equal(t, before, e.od.hits.Load(), "an open circuit is reported without calling the provider")
	assert.Equal(t, true, res.body["partial"])
}

func TestGetReturnsTheSameAnswerAsPost(t *testing.T) {
	e := newParallelEnv(t, true)

	posted := e.resolve(t, "c1")
	rec := httptest.NewRecorder()
	e.api.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/risk-profiles/c1?consent_active=true", nil))

	require.Equal(t, http.StatusOK, rec.Code)
	var got map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	assert.Equal(t, posted.body["mode"], got["mode"])
	assert.Equal(t, posted.body["source"], got["source"])
	assert.Equal(t, posted.body["profile"], got["profile"])
	assert.Equal(t, posted.body["partial"], got["partial"])
	assert.Len(t, got["sources"], 3)
}

// waitFor polls cond for up to one second. It does not use assert.Eventually,
// which runs the condition in an extra goroutine.
func waitFor(t *testing.T, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for !cond() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	assert.True(t, cond(), msg)
}

// settledGoroutines waits for short-lived goroutines (closing connections) to
// end and returns the stable count.
func settledGoroutines(current int) int {
	for i := 0; i < 20; i++ {
		time.Sleep(10 * time.Millisecond)
		n := runtime.NumGoroutine()
		if n >= current {
			return current
		}
		current = n
	}
	return current
}
