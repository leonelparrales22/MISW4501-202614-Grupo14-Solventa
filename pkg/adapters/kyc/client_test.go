package kyc_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/adapters/kyc"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/resilience"
)

func testConfig(url string, timeout time.Duration) kyc.Config {
	return kyc.Config{
		BaseURL: url,
		Timeout: timeout,
		Breaker: resilience.Config{ConsecutiveFailures: 3, OpenTimeout: 60 * time.Millisecond, HalfOpenSuccesses: 3},
	}
}

func TestFetchProfileSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/profiles/cust 1", r.URL.Path)
		_, _ = w.Write([]byte(`{"customer_id":"cust 1","score":0.42,"category":"standard","updated_at":"2026-10-01T10:00:00Z"}`))
	}))
	defer srv.Close()

	c := kyc.NewClient(testConfig(srv.URL+"/", time.Second))
	p, err := c.FetchProfile(context.Background(), "cust 1")

	require.NoError(t, err)
	assert.Equal(t, 0.42, p.Score)
	assert.Equal(t, "standard", p.Category)
	assert.Equal(t, 2026, p.UpdatedAt.Year())
	assert.Equal(t, "closed", c.BreakerState())
}

func TestFetchProfileRejectsEmptyCustomerWithoutTrippingBreaker(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	defer srv.Close()

	c := kyc.NewClient(testConfig(srv.URL, time.Second))
	for i := 0; i < 5; i++ {
		_, err := c.FetchProfile(context.Background(), "")
		require.ErrorIs(t, err, kyc.ErrInvalidCustomerID)
	}
	assert.Zero(t, hits.Load())
	assert.Equal(t, "closed", c.BreakerState())
}

func TestFetchProfileMapsProviderFailuresToUnavailable(t *testing.T) {
	tests := map[string]http.HandlerFunc{
		"server error": func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusInternalServerError) },
		"not found":    func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNotFound) },
		"bad json":     func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("{not json")) },
		"too slow": func(w http.ResponseWriter, r *http.Request) {
			select {
			case <-time.After(500 * time.Millisecond):
			case <-r.Context().Done():
			}
		},
	}
	for name, handler := range tests {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(handler)
			defer srv.Close()
			c := kyc.NewClient(testConfig(srv.URL, 40*time.Millisecond))

			_, err := c.FetchProfile(context.Background(), "c1")

			assert.ErrorIs(t, err, kyc.ErrSourceUnavailable)
		})
	}
}

func TestFetchProfileUnreachableProvider(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close()

	c := kyc.NewClient(testConfig(url, time.Second))
	_, err := c.FetchProfile(context.Background(), "c1")

	assert.ErrorIs(t, err, kyc.ErrSourceUnavailable)
}

func TestOpenCircuitStopsCallingTheProviderAndRecovers(t *testing.T) {
	var hits atomic.Int32
	var healthy atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		if !healthy.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte(`{"customer_id":"c1","score":0.3,"category":"standard","updated_at":"2026-10-01T10:00:00Z"}`))
	}))
	defer srv.Close()
	c := kyc.NewClient(testConfig(srv.URL, time.Second))

	for i := 0; i < 3; i++ {
		_, err := c.FetchProfile(context.Background(), "c1")
		require.ErrorIs(t, err, kyc.ErrSourceUnavailable)
	}
	require.Equal(t, "open", c.BreakerState())

	before := hits.Load()
	_, err := c.FetchProfile(context.Background(), "c1")
	require.ErrorIs(t, err, kyc.ErrSourceUnavailable)
	require.ErrorIs(t, err, resilience.ErrOpen)
	assert.Equal(t, before, hits.Load(), "open circuit must not reach the provider")

	healthy.Store(true)
	time.Sleep(80 * time.Millisecond)
	for i := 0; i < 3; i++ {
		_, err := c.FetchProfile(context.Background(), "c1")
		require.NoError(t, err)
	}
	assert.Equal(t, "closed", c.BreakerState())
}

func TestZeroTimeoutFallsBackToDefault(t *testing.T) {
	assert.Equal(t, 700*time.Millisecond, kyc.DefaultTimeout)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"customer_id":"c1","score":0.1,"category":"preferred","updated_at":"2026-10-01T10:00:00Z"}`))
	}))
	defer srv.Close()
	c := kyc.NewClient(kyc.Config{BaseURL: srv.URL, Breaker: resilience.DefaultConfig()})
	_, err := c.FetchProfile(context.Background(), "c1")
	require.NoError(t, err)
}

// --- HA-L01: shared connection pool and per-dependency budget -----------------

type countingTransport struct {
	calls atomic.Int32
	next  http.RoundTripper
}

func (t *countingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	t.calls.Add(1)
	return t.next.RoundTrip(r)
}

func TestInjectedHTTPClientCarriesTheCalls(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"customer_id":"c1","score":0.1,"category":"preferred","updated_at":"2026-10-01T10:00:00Z"}`))
	}))
	defer srv.Close()
	transport := &countingTransport{next: http.DefaultTransport}
	cfg := testConfig(srv.URL, time.Second)
	cfg.HTTPClient = &http.Client{Transport: transport}

	_, err := kyc.NewClient(cfg).FetchProfile(context.Background(), "c1")

	require.NoError(t, err)
	assert.Equal(t, int32(1), transport.calls.Load())
}

func TestInjectedHTTPClientStillHonorsTheTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(500 * time.Millisecond):
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	cfg := testConfig(srv.URL, 40*time.Millisecond)
	cfg.HTTPClient = &http.Client{} // no client-level timeout

	start := time.Now()
	_, err := kyc.NewClient(cfg).FetchProfile(context.Background(), "c1")

	assert.ErrorIs(t, err, kyc.ErrSourceUnavailable)
	assert.Less(t, time.Since(start), 300*time.Millisecond)
}

func TestCallerBudgetDoesNotTripTheBreakerWhenIgnored(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(500 * time.Millisecond):
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	cfg := testConfig(srv.URL, time.Second)
	cfg.Breaker.IgnoreCallerCancellation = true
	c := kyc.NewClient(cfg)

	for i := 0; i < 5; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		_, err := c.FetchProfile(ctx, "c1")
		cancel()
		require.ErrorIs(t, err, kyc.ErrSourceUnavailable)
	}
	assert.Equal(t, "closed", c.BreakerState(), "the caller's budget is not a failure of the provider")
}

func TestClientTimesOutAtConfiguredBudget(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(time.Second):
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	c := kyc.NewClient(testConfig(srv.URL, 50*time.Millisecond))

	start := time.Now()
	_, err := c.FetchProfile(context.Background(), "c1")
	elapsed := time.Since(start)

	assert.ErrorIs(t, err, kyc.ErrSourceUnavailable)
	assert.GreaterOrEqual(t, elapsed, 50*time.Millisecond)
	assert.Less(t, elapsed, 400*time.Millisecond, "the call must be cut near its budget")
}
