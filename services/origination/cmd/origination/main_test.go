package main

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
