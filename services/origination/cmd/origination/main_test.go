package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestLoadConfigDefaultsMatchTheValidatedDesign(t *testing.T) {
	cfg, err := loadConfig(env(map[string]string{"REDIS_ADDR": "redis:6379", "OPEN_FINANCE_URL": "http://of:9000"}))

	require.NoError(t, err)
	assert.Equal(t, "8080", cfg.port)
	assert.Equal(t, 700*time.Millisecond, cfg.openFinanceTimeout)
	assert.Equal(t, uint32(3), cfg.breaker.ConsecutiveFailures)
	assert.Equal(t, 5*time.Second, cfg.breaker.OpenTimeout)
	assert.Equal(t, uint32(3), cfg.breaker.HalfOpenSuccesses)
	assert.Equal(t, 30*24*time.Hour, cfg.profileCacheTTL)
}

func TestLoadConfigOverrides(t *testing.T) {
	cfg, err := loadConfig(env(map[string]string{
		"PORT": "9090", "REDIS_ADDR": "r:1", "OPEN_FINANCE_URL": "http://of",
		"OPEN_FINANCE_TIMEOUT_MS": "300", "BREAKER_CONSECUTIVE_FAILURES": "5",
		"BREAKER_OPEN_TIMEOUT_MS": "1000", "BREAKER_HALF_OPEN_SUCCESSES": "2", "PROFILE_CACHE_TTL_HOURS": "24",
	}))

	require.NoError(t, err)
	assert.Equal(t, "9090", cfg.port)
	assert.Equal(t, 300*time.Millisecond, cfg.openFinanceTimeout)
	assert.Equal(t, uint32(5), cfg.breaker.ConsecutiveFailures)
	assert.Equal(t, time.Second, cfg.breaker.OpenTimeout)
	assert.Equal(t, uint32(2), cfg.breaker.HalfOpenSuccesses)
	assert.Equal(t, 24*time.Hour, cfg.profileCacheTTL)
}

func TestLoadConfigRejectsMissingOrInvalidValues(t *testing.T) {
	base := map[string]string{"REDIS_ADDR": "r:1", "OPEN_FINANCE_URL": "http://of"}
	tests := map[string]map[string]string{
		"missing redis":      {"OPEN_FINANCE_URL": "http://of"},
		"missing of url":     {"REDIS_ADDR": "r:1"},
		"non numeric":        {"OPEN_FINANCE_TIMEOUT_MS": "fast"},
		"zero failures":      {"BREAKER_CONSECUTIVE_FAILURES": "0"},
		"negative open time": {"BREAKER_OPEN_TIMEOUT_MS": "-1"},
		"bad successes":      {"BREAKER_HALF_OPEN_SUCCESSES": "x"},
		"bad ttl":            {"PROFILE_CACHE_TTL_HOURS": "x"},
	}
	for name, extra := range tests {
		t.Run(name, func(t *testing.T) {
			m := map[string]string{}
			if name != "missing redis" && name != "missing of url" {
				for k, v := range base {
					m[k] = v
				}
			}
			for k, v := range extra {
				m[k] = v
			}
			_, err := loadConfig(env(m))
			assert.Error(t, err)
		})
	}
}

func TestSystemClockReturnsCurrentTime(t *testing.T) {
	assert.WithinDuration(t, time.Now(), systemClock{}.Now(), time.Second)
}

// --- HA-L01: parallel profiling configuration -----------------------------------

func requiredEnv() map[string]string {
	return map[string]string{"REDIS_ADDR": "r:1", "OPEN_FINANCE_URL": "http://of"}
}

func TestLoadConfigBudgets(t *testing.T) {
	cfg, err := loadConfig(env(requiredEnv()))

	require.NoError(t, err)
	assert.Equal(t, 120*time.Millisecond, cfg.sourceBudget, "CA-2: 120 ms per dependency")
	assert.Equal(t, 700*time.Millisecond, cfg.profilingDeadline, "CA-2: 700 ms over the whole fan-out")
	assert.Equal(t, 200, cfg.maxConnsPerHost, "EXP-L01 pool")
	assert.True(t, cfg.openFinanceRequired, "D-2: Open Finance required by default")
	assert.Empty(t, cfg.openDataURL)
	assert.Empty(t, cfg.kycURL)
	assert.True(t, cfg.breaker.IgnoreCallerCancellation, "D-3 (b): the budget never opens a circuit")
}

func TestLoadConfigParallelOverrides(t *testing.T) {
	m := requiredEnv()
	m["OPEN_DATA_URL"] = "http://od"
	m["KYC_URL"] = "http://kyc"
	m["SOURCE_BUDGET_MS"] = "150"
	m["PROFILING_DEADLINE_MS"] = "500"
	m["HTTP_MAX_CONNS_PER_HOST"] = "50"
	m["OPEN_FINANCE_REQUIRED"] = "false"

	cfg, err := loadConfig(env(m))

	require.NoError(t, err)
	assert.Equal(t, "http://od", cfg.openDataURL)
	assert.Equal(t, "http://kyc", cfg.kycURL)
	assert.Equal(t, 150*time.Millisecond, cfg.sourceBudget)
	assert.Equal(t, 500*time.Millisecond, cfg.profilingDeadline)
	assert.Equal(t, 50, cfg.maxConnsPerHost)
	assert.False(t, cfg.openFinanceRequired)
}

func TestLoadConfigRejectsInvalidParallelValues(t *testing.T) {
	tests := map[string]map[string]string{
		"zero budget":          {"SOURCE_BUDGET_MS": "0"},
		"non numeric deadline": {"PROFILING_DEADLINE_MS": "soon"},
		"negative pool":        {"HTTP_MAX_CONNS_PER_HOST": "-1"},
		"bad required flag":    {"OPEN_FINANCE_REQUIRED": "maybe"},
	}
	for name, extra := range tests {
		t.Run(name, func(t *testing.T) {
			m := requiredEnv()
			for k, v := range extra {
				m[k] = v
			}
			_, err := loadConfig(env(m))
			assert.Error(t, err)
		})
	}
}

func TestNewHTTPClientUsesThePoolSize(t *testing.T) {
	transport, ok := newHTTPClient(200).Transport.(*http.Transport)

	require.True(t, ok)
	assert.Equal(t, 200, transport.MaxConnsPerHost)
	assert.Equal(t, 200, transport.MaxIdleConnsPerHost)
}

func provider(t *testing.T, score float64) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprintf(w, `{"customer_id":"c1","score":%v,"category":"standard","updated_at":"2026-10-10T10:00:00Z"}`, score)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func downProvider(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func parallelConfig(t *testing.T, of, od, kyc string, required bool) config {
	t.Helper()
	m := requiredEnv()
	m["OPEN_FINANCE_URL"] = of
	m["OPEN_DATA_URL"] = od
	m["KYC_URL"] = kyc
	m["OPEN_FINANCE_REQUIRED"] = strconv.FormatBool(required)
	cfg, err := loadConfig(env(m))
	require.NoError(t, err)
	return cfg
}

func TestNewProfileSourceConsultsTheThreeSources(t *testing.T) {
	cfg := parallelConfig(t, provider(t, 0.2), provider(t, 0.4), provider(t, 0.6), true)

	got, err := newProfileSource(cfg, newHTTPClient(cfg.maxConnsPerHost)).FetchProfile(context.Background(), "c1")

	require.NoError(t, err)
	require.Len(t, got.Sources, 3)
	assert.InDelta(t, 0.4, got.Score, 1e-9)
	assert.Equal(t, domain.SourceExternal, got.FreshSource())
}

func TestNewProfileSourceWithOnlyOpenFinanceKeepsTheHAD01Answer(t *testing.T) {
	cfg := parallelConfig(t, provider(t, 0.3), "", "", true)

	got, err := newProfileSource(cfg, newHTTPClient(cfg.maxConnsPerHost)).FetchProfile(context.Background(), "c1")

	require.NoError(t, err)
	require.Len(t, got.Sources, 1)
	assert.Equal(t, domain.SourceOpenFinance, got.FreshSource(), "the HA-D01 scenario still sees source=open_finance")
}

func TestNewProfileSourceHonorsOpenFinanceRequired(t *testing.T) {
	t.Run("required: no profile without Open Finance", func(t *testing.T) {
		cfg := parallelConfig(t, downProvider(t), provider(t, 0.4), provider(t, 0.6), true)

		_, err := newProfileSource(cfg, newHTTPClient(cfg.maxConnsPerHost)).FetchProfile(context.Background(), "c1")

		assert.ErrorIs(t, err, ports.ErrSourceUnavailable)
	})
	t.Run("not required: partial profile with the other sources", func(t *testing.T) {
		cfg := parallelConfig(t, downProvider(t), provider(t, 0.4), provider(t, 0.6), false)

		got, err := newProfileSource(cfg, newHTTPClient(cfg.maxConnsPerHost)).FetchProfile(context.Background(), "c1")

		require.NoError(t, err)
		assert.True(t, got.Partial)
		assert.InDelta(t, 0.5, got.Score, 1e-9)
	})
}
