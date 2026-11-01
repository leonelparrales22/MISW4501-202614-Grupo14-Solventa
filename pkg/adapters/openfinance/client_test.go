package openfinance_test

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/adapters/openfinance"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/idempotency"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/resilience"
)

func testConfig(url string, timeout time.Duration) openfinance.Config {
	return openfinance.Config{
		BaseURL: url,
		Timeout: timeout,
		Breaker: resilience.Config{ConsecutiveFailures: 3, OpenTimeout: 300 * time.Millisecond, HalfOpenSuccesses: 3},
	}
}

func TestFetchProfileSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/profiles/cust 1", r.URL.Path)
		_, _ = w.Write([]byte(`{"customer_id":"cust 1","score":0.42,"category":"standard","updated_at":"2026-10-01T10:00:00Z"}`))
	}))
	defer srv.Close()

	c := openfinance.NewClient(testConfig(srv.URL+"/", time.Second))
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

	c := openfinance.NewClient(testConfig(srv.URL, time.Second))
	for i := 0; i < 5; i++ {
		_, err := c.FetchProfile(context.Background(), "")
		require.ErrorIs(t, err, openfinance.ErrInvalidCustomerID)
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
			c := openfinance.NewClient(testConfig(srv.URL, 40*time.Millisecond))

			_, err := c.FetchProfile(context.Background(), "c1")

			assert.ErrorIs(t, err, openfinance.ErrSourceUnavailable)
		})
	}
}

func TestFetchProfileUnreachableProvider(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close()

	c := openfinance.NewClient(testConfig(url, time.Second))
	_, err := c.FetchProfile(context.Background(), "c1")

	assert.ErrorIs(t, err, openfinance.ErrSourceUnavailable)
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
	c := openfinance.NewClient(testConfig(srv.URL, time.Second))

	for i := 0; i < 3; i++ {
		_, err := c.FetchProfile(context.Background(), "c1")
		require.ErrorIs(t, err, openfinance.ErrSourceUnavailable)
	}
	require.Equal(t, "open", c.BreakerState())

	before := hits.Load()
	_, err := c.FetchProfile(context.Background(), "c1")
	require.ErrorIs(t, err, openfinance.ErrSourceUnavailable)
	require.ErrorIs(t, err, resilience.ErrOpen)
	assert.Equal(t, before, hits.Load(), "open circuit must not reach the provider")

	healthy.Store(true)
	time.Sleep(400 * time.Millisecond)
	for i := 0; i < 3; i++ {
		_, err := c.FetchProfile(context.Background(), "c1")
		require.NoError(t, err)
	}
	assert.Equal(t, "closed", c.BreakerState())
}

func TestZeroTimeoutFallsBackToDefault(t *testing.T) {
	assert.Equal(t, 700*time.Millisecond, openfinance.DefaultTimeout)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"customer_id":"c1","score":0.1,"category":"preferred","updated_at":"2026-10-01T10:00:00Z"}`))
	}))
	defer srv.Close()
	c := openfinance.NewClient(openfinance.Config{BaseURL: srv.URL, Breaker: resilience.DefaultConfig()})
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

	_, err := openfinance.NewClient(cfg).FetchProfile(context.Background(), "c1")

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
	_, err := openfinance.NewClient(cfg).FetchProfile(context.Background(), "c1")

	assert.ErrorIs(t, err, openfinance.ErrSourceUnavailable)
	assert.Less(t, time.Since(start), 300*time.Millisecond)
}

func TestCallerBudgetCountsAsAFailure(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		select {
		case <-time.After(500 * time.Millisecond):
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	cfg := testConfig(srv.URL, time.Second)
	cfg.Breaker.OpenTimeout = time.Minute
	c := openfinance.NewClient(cfg)

	for i := 0; i < 3; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		_, err := c.FetchProfile(ctx, "c1")
		cancel()
		require.ErrorIs(t, err, openfinance.ErrSourceUnavailable)
	}
	assert.Equal(t, "open", c.BreakerState(), "a call cut by the caller's budget is a failure of Open Finance (D-3 c)")

	_, err := c.FetchProfile(context.Background(), "c1")
	assert.ErrorIs(t, err, resilience.ErrOpen)
	assert.Equal(t, int32(3), hits.Load(), "an open circuit does not call Open Finance")
}

// The notice of a revoked consent to the ecosystem (HU-M1.3-02).

var effectiveAt = time.Date(2026, 10, 24, 20, 0, 1, 0, time.UTC)

func notice() openfinance.RevocationNotice {
	return openfinance.RevocationNotice{ConsentID: "cns-1", EntityCode: "aliado-banco-1", CustomerID: "cust-1", EffectiveAt: effectiveAt}
}

const revocationKey = "revocation-cns-1-aliado-banco-1"

func TestRevokeConsentSendsTheKeyAndOnlyTheMinimumData(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "/consent-revocations", r.URL.Path)
		assert.Equal(t, revocationKey, r.Header.Get("Idempotency-Key"))
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))
		var body map[string]any
		assert.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		assert.Equal(t, map[string]any{
			"consent_id": "cns-1", "entity_code": "aliado-banco-1", "customer_id": "cust-1", "effective_at": "2026-10-24T20:00:01Z",
		}, body, "no document nor name of the holder")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"revocation_reference":"of-rev-1","status":"confirmed","confirmed_at":"2026-10-24T20:00:02Z"}`))
	}))
	defer srv.Close()

	conf, err := openfinance.NewClient(testConfig(srv.URL, time.Second)).RevokeConsent(context.Background(), notice(), revocationKey)

	require.NoError(t, err)
	assert.Equal(t, "of-rev-1", conf.Reference)
	assert.Equal(t, "confirmed", conf.Status)
	assert.Equal(t, 2, conf.ConfirmedAt.Second())
}

func TestRevokeConsentTheSameKeyAnsweredWith200IsASuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"revocation_reference":"of-rev-1","status":"confirmed","confirmed_at":"2026-10-24T20:00:02Z"}`))
	}))
	defer srv.Close()

	conf, err := openfinance.NewClient(testConfig(srv.URL, time.Second)).RevokeConsent(context.Background(), notice(), revocationKey)

	require.NoError(t, err)
	assert.Equal(t, "of-rev-1", conf.Reference)
}

func TestRevokeConsentClientErrorsAreFinalAndNeverOpenTheCircuit(t *testing.T) {
	for _, code := range []int{http.StatusBadRequest, http.StatusNotFound, http.StatusUnprocessableEntity} {
		var hits atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			hits.Add(1)
			w.WriteHeader(code)
		}))
		c := openfinance.NewClient(testConfig(srv.URL, time.Second))
		for i := 0; i < 5; i++ {
			_, err := c.RevokeConsent(context.Background(), notice(), revocationKey)
			assert.ErrorIs(t, err, openfinance.ErrRevocationRejected, code)
			assert.ErrorIs(t, err, idempotency.ErrRejected, code)
			assert.Equal(t, idempotency.OutcomeRejected, idempotency.Classify(err))
		}
		assert.EqualValues(t, 5, hits.Load(), "every call reached the provider")
		assert.Equal(t, "closed", c.RevocationsBreakerState(), "a rejection is a healthy answer")
		srv.Close()
	}
}

func TestRevokeConsentServerErrorsAndNotNowAnswersAreAmbiguous(t *testing.T) {
	for _, code := range []int{http.StatusInternalServerError, http.StatusServiceUnavailable, http.StatusRequestTimeout,
		http.StatusConflict, http.StatusTooEarly, http.StatusTooManyRequests, http.StatusAccepted + 100} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(code) }))
		_, err := openfinance.NewClient(testConfig(srv.URL, time.Second)).RevokeConsent(context.Background(), notice(), revocationKey)
		assert.ErrorIs(t, err, openfinance.ErrRevocationAmbiguous, code)
		assert.Equal(t, idempotency.OutcomeAmbiguous, idempotency.Classify(err), code)
		srv.Close()
	}
}

func TestRevokeConsentTimeoutIsAmbiguous(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-release }))
	defer srv.Close()
	defer close(release)

	start := time.Now()
	_, err := openfinance.NewClient(testConfig(srv.URL, 50*time.Millisecond)).RevokeConsent(context.Background(), notice(), revocationKey)

	assert.ErrorIs(t, err, idempotency.ErrAmbiguous)
	assert.Less(t, time.Since(start), time.Second, "the hard timeout cut the call")
}

func TestRevokeConsentRefusedConnectionIsAmbiguous(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := ln.Addr().String()
	require.NoError(t, ln.Close())

	_, err = openfinance.NewClient(testConfig("http://"+addr, time.Second)).RevokeConsent(context.Background(), notice(), revocationKey)

	assert.ErrorIs(t, err, openfinance.ErrRevocationAmbiguous)
}

func TestRevokeConsentUnreadableSuccessIsAmbiguous(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"revocation_reference":`))
	}))
	defer srv.Close()

	_, err := openfinance.NewClient(testConfig(srv.URL, time.Second)).RevokeConsent(context.Background(), notice(), revocationKey)

	assert.ErrorIs(t, err, openfinance.ErrRevocationAmbiguous)
}

func TestRevokeConsentOpenCircuitDoesNotSendAndDoesNotTouchTheProfiles(t *testing.T) {
	var revocations, profiles atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/consent-revocations" {
			revocations.Add(1)
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		profiles.Add(1)
		_, _ = w.Write([]byte(`{"customer_id":"c1","score":0.5,"category":"standard","updated_at":"2026-10-01T10:00:00Z"}`))
	}))
	defer srv.Close()
	c := openfinance.NewClient(testConfig(srv.URL, time.Second))

	for i := 0; i < 3; i++ {
		_, err := c.RevokeConsent(context.Background(), notice(), revocationKey)
		require.ErrorIs(t, err, openfinance.ErrRevocationAmbiguous)
	}
	_, err := c.RevokeConsent(context.Background(), notice(), revocationKey)

	assert.ErrorIs(t, err, openfinance.ErrRevocationNotSent)
	assert.ErrorIs(t, err, idempotency.ErrNotSent)
	assert.Equal(t, idempotency.OutcomeNotSent, idempotency.Classify(err))
	assert.EqualValues(t, 3, revocations.Load(), "the open circuit did not send the fourth notice")
	assert.Equal(t, "open", c.RevocationsBreakerState())
	_, err = c.FetchProfile(context.Background(), "c1")
	assert.NoError(t, err, "the profiles have their own circuit")
	assert.Equal(t, "closed", c.BreakerState())
}

func TestRevokeConsentInvalidNoticeNeverReachesTheProvider(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	defer srv.Close()
	c := openfinance.NewClient(testConfig(srv.URL, time.Second))

	cases := map[string]func(*openfinance.RevocationNotice, *string){
		"no key":            func(_ *openfinance.RevocationNotice, k *string) { *k = "" },
		"no consent":        func(n *openfinance.RevocationNotice, _ *string) { n.ConsentID = "" },
		"no entity":         func(n *openfinance.RevocationNotice, _ *string) { n.EntityCode = "" },
		"no customer":       func(n *openfinance.RevocationNotice, _ *string) { n.CustomerID = "" },
		"no effective time": func(n *openfinance.RevocationNotice, _ *string) { n.EffectiveAt = time.Time{} },
	}
	for name, mutate := range cases {
		n, key := notice(), revocationKey
		mutate(&n, &key)
		_, err := c.RevokeConsent(context.Background(), n, key)
		assert.ErrorIs(t, err, openfinance.ErrInvalidRevocation, name)
	}
	assert.Zero(t, hits.Load())
	assert.Equal(t, "closed", c.RevocationsBreakerState())
}
