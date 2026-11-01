package main

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func env(values map[string]string) func(string) string {
	return func(name string) string { return values[name] }
}

func TestThePolicyCoreIsOptionalAndTakes700MsByDefault(t *testing.T) {
	cfg, err := loadPolicyCoreConfig(env(nil))

	require.NoError(t, err)
	assert.Empty(t, cfg.url)
	assert.Equal(t, 700*time.Millisecond, cfg.timeout)
}

func TestThePolicyCoreConfiguration(t *testing.T) {
	cfg, err := loadPolicyCoreConfig(env(map[string]string{
		"POLICY_CORE_URL": "http://policy-core:9060", "POLICY_CORE_TIMEOUT_MS": "250",
	}))

	require.NoError(t, err)
	assert.Equal(t, "http://policy-core:9060", cfg.url)
	assert.Equal(t, 250*time.Millisecond, cfg.timeout)
}

func TestAnInvalidPolicyCoreTimeoutIsRefused(t *testing.T) {
	for _, raw := range []string{"0", "-1", "abc", "1.5"} {
		_, err := loadPolicyCoreConfig(env(map[string]string{"POLICY_CORE_TIMEOUT_MS": raw}))
		assert.Error(t, err, raw)
	}
}
