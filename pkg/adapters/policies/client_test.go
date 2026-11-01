package policies_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/adapters/policies"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/resilience"
)

const twoPolicies = `{"policies":[
	{"policy_id":"AU-2025-118204","line":"auto","product":"Autos","insured_object":"KJT-482","status":"active",
	 "valid_from":"2025-11-01T05:00:00Z","valid_to":"2026-11-01T05:00:00Z","covered_incidents":["collision","theft"]},
	{"policy_id":"VJ-2025-000931","line":"travel","product":"Viaje","insured_object":"Viajes nacionales","status":"expired",
	 "valid_from":"2024-11-01T05:00:00Z","valid_to":"2025-11-01T05:00:00Z","covered_incidents":["flight_delay"]}]}`

func testConfig(url string, timeout time.Duration) policies.Config {
	return policies.Config{
		BaseURL: url,
		Timeout: timeout,
		Breaker: resilience.Config{ConsecutiveFailures: 3, OpenTimeout: 300 * time.Millisecond, HalfOpenSuccesses: 3},
	}
}

func TestHolderPoliciesSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, "/holders/sub 1/policies", r.URL.Path)
		_, _ = w.Write([]byte(twoPolicies))
	}))
	defer srv.Close()

	c := policies.NewClient(testConfig(srv.URL+"/", time.Second))
	got, err := c.HolderPolicies(context.Background(), "sub 1")

	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, policies.Policy{
		PolicyID: "AU-2025-118204", Line: "auto", Product: "Autos", InsuredObject: "KJT-482", Status: "active",
		ValidFrom:        time.Date(2025, 11, 1, 5, 0, 0, 0, time.UTC),
		ValidTo:          time.Date(2026, 11, 1, 5, 0, 0, 0, time.UTC),
		CoveredIncidents: []string{"collision", "theft"},
	}, got[0])
	assert.Equal(t, "expired", got[1].Status)
	assert.Equal(t, "closed", c.BreakerState())
}

func TestAnUnknownHolderHasNoPolicies(t *testing.T) {
	for name, handler := range map[string]http.HandlerFunc{
		"empty list": func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"policies":[]}`)) },
		"not found":  func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNotFound) },
	} {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(handler)
			defer srv.Close()
			c := policies.NewClient(testConfig(srv.URL, time.Second))

			got, err := c.HolderPolicies(context.Background(), "nobody")

			require.NoError(t, err)
			assert.NotNil(t, got)
			assert.Empty(t, got)
			assert.Equal(t, "closed", c.BreakerState())
		})
	}
}

func TestHolderPoliciesRejectsAnEmptyHolderWithoutTrippingTheBreaker(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	defer srv.Close()

	c := policies.NewClient(testConfig(srv.URL, time.Second))
	for i := 0; i < 5; i++ {
		_, err := c.HolderPolicies(context.Background(), "")
		require.ErrorIs(t, err, policies.ErrInvalidHolder)
	}
	assert.Zero(t, hits.Load())
	assert.Equal(t, "closed", c.BreakerState())
}

func TestHolderPoliciesMapsCoreFailuresToUnavailable(t *testing.T) {
	tests := map[string]http.HandlerFunc{
		"server error": func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusInternalServerError) },
		"bad request":  func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusBadRequest) },
		"bad json":     func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("{not json")) },
		"missing list": func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{}`)) },
		"wrong type":   func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"policies":"none"}`)) },
		"bad timestamp": func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"policies":[{"valid_from":"yesterday"}]}`))
		},
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
			c := policies.NewClient(testConfig(srv.URL, 40*time.Millisecond))

			_, err := c.HolderPolicies(context.Background(), "sub-1")

			assert.ErrorIs(t, err, policies.ErrSourceUnavailable)
		})
	}
}

func TestHolderPoliciesUnreachableCore(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close()

	_, err := policies.NewClient(testConfig(url, time.Second)).HolderPolicies(context.Background(), "sub-1")

	assert.ErrorIs(t, err, policies.ErrSourceUnavailable)
}

func TestOpenCircuitStopsCallingTheCoreAndRecovers(t *testing.T) {
	var hits atomic.Int32
	var healthy atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		if !healthy.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte(`{"policies":[]}`))
	}))
	defer srv.Close()
	c := policies.NewClient(testConfig(srv.URL, time.Second))

	for i := 0; i < 3; i++ {
		_, err := c.HolderPolicies(context.Background(), "sub-1")
		require.ErrorIs(t, err, policies.ErrSourceUnavailable)
	}
	require.Equal(t, "open", c.BreakerState())

	before := hits.Load()
	_, err := c.HolderPolicies(context.Background(), "sub-1")
	require.ErrorIs(t, err, policies.ErrSourceUnavailable)
	require.ErrorIs(t, err, resilience.ErrOpen)
	assert.Equal(t, before, hits.Load(), "an open circuit must not reach the core")

	healthy.Store(true)
	time.Sleep(400 * time.Millisecond)
	for i := 0; i < 3; i++ {
		_, err := c.HolderPolicies(context.Background(), "sub-1")
		require.NoError(t, err)
	}
	assert.Equal(t, "closed", c.BreakerState())
}

func TestZeroTimeoutFallsBackToDefault(t *testing.T) {
	assert.Equal(t, 700*time.Millisecond, policies.DefaultTimeout)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"policies":[]}`))
	}))
	defer srv.Close()

	_, err := policies.NewClient(policies.Config{BaseURL: srv.URL, Breaker: resilience.DefaultConfig()}).
		HolderPolicies(context.Background(), "sub-1")

	require.NoError(t, err)
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
	_, err := policies.NewClient(cfg).HolderPolicies(context.Background(), "sub-1")

	assert.ErrorIs(t, err, policies.ErrSourceUnavailable)
	assert.Less(t, time.Since(start), 300*time.Millisecond)
}

func TestCallerCancellationCountsAsAFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(500 * time.Millisecond):
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	cfg := testConfig(srv.URL, time.Second)
	cfg.Breaker.OpenTimeout = time.Minute
	c := policies.NewClient(cfg)

	for i := 0; i < 3; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		_, err := c.HolderPolicies(ctx, "sub-1")
		cancel()
		require.ErrorIs(t, err, policies.ErrSourceUnavailable)
	}
	assert.Equal(t, "open", c.BreakerState())
}

const homePolicy = `{"policy_id":"HG-2025-880231","line":"home","product":"Hogar","insured_object":"Calle 1","status":"active",
	"valid_from":"2025-11-01T05:00:00Z","valid_to":"2026-11-01T05:00:00Z","covered_incidents":["water_damage"],
	"coverages":[{"incident":"water_damage","insured_value_minor":32000000000,"currency":"COP","deductible_basis_points":500}]}`

func TestHolderPolicyReturnsTheCoverages(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/holders/sub 1/policies/HG-2025-880231", r.URL.Path)
		_, _ = w.Write([]byte(homePolicy))
	}))
	defer srv.Close()

	got, err := policies.NewClient(testConfig(srv.URL, time.Second)).HolderPolicy(context.Background(), "sub 1", "HG-2025-880231")

	require.NoError(t, err)
	assert.Equal(t, "HG-2025-880231", got.PolicyID)
	coverage, ok := got.Coverage("water_damage")
	require.True(t, ok)
	assert.Equal(t, policies.Coverage{Incident: "water_damage", InsuredValueMinor: 32_000_000_000, Currency: "COP",
		DeductibleBasisPoints: 500}, coverage)
	_, ok = got.Coverage("collision")
	assert.False(t, ok, "an incident the policy does not cover")
}

func TestHolderPolicyNotFoundIsAnAnswer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	c := policies.NewClient(testConfig(srv.URL, time.Second))

	for i := 0; i < 5; i++ {
		_, err := c.HolderPolicy(context.Background(), "sub-1", "AU-2026-999999")
		require.ErrorIs(t, err, policies.ErrPolicyNotFound)
		assert.NotErrorIs(t, err, policies.ErrSourceUnavailable)
	}
	assert.Equal(t, "closed", c.BreakerState(), "a 404 does not count against the circuit")
}

func TestHolderPolicyValidatesItsArguments(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
	defer srv.Close()
	c := policies.NewClient(testConfig(srv.URL, time.Second))

	_, err := c.HolderPolicy(context.Background(), "", "HG-1")
	assert.ErrorIs(t, err, policies.ErrInvalidHolder)
	_, err = c.HolderPolicy(context.Background(), "sub-1", "")
	assert.ErrorIs(t, err, policies.ErrInvalidPolicy)
	_, err = c.Holder(context.Background(), "")
	assert.ErrorIs(t, err, policies.ErrInvalidHolder)
	assert.Zero(t, calls.Load(), "invalid arguments never reach the core")
}

func TestHolderReturnsTheProfile(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/holders/sub-1", r.URL.Path)
		_, _ = w.Write([]byte(`{"holder_id":"sub-1","full_name":"Carolina Pérez Ruiz","document_type":"CC",
			"document_id":"1020345678","email":"carolina.perez@correo.example","phone":"+57 301 234 5678",
			"payout_account":{"bank_code":"1007","bank_name":"Banco Aliado","account_type":"savings",
			"account_number":"0123-4567-12"}}`))
	}))
	defer srv.Close()

	got, err := policies.NewClient(testConfig(srv.URL, time.Second)).Holder(context.Background(), "sub-1")

	require.NoError(t, err)
	assert.Equal(t, policies.Holder{HolderID: "sub-1", FullName: "Carolina Pérez Ruiz", DocumentType: "CC",
		DocumentID: "1020345678", Email: "carolina.perez@correo.example", Phone: "+57 301 234 5678",
		PayoutAccount: policies.PayoutAccount{BankCode: "1007", BankName: "Banco Aliado", AccountType: "savings",
			AccountNumber: "0123-4567-12"}}, got)
}

func TestHolderTellsAnUnknownHolderFromAFailure(t *testing.T) {
	for name, handler := range map[string]http.HandlerFunc{
		"not found": func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNotFound) },
		"5xx":       func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusBadGateway) },
		"bad body":  func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{not json`)) },
	} {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(handler)
			defer srv.Close()

			_, err := policies.NewClient(testConfig(srv.URL, time.Second)).Holder(context.Background(), "sub-1")

			if name == "not found" {
				assert.ErrorIs(t, err, policies.ErrHolderNotFound)
				return
			}
			assert.ErrorIs(t, err, policies.ErrSourceUnavailable)
		})
	}
}
