package main

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
)

func envOf(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestRenewalConfigDefaultsToTheRulesOfTheStory(t *testing.T) {
	var cfg renewalConfig

	require.NoError(t, loadRenewalConfig(envOf(nil), &cfg))

	assert.Equal(t, domain.DefaultRenewalPolicy, cfg.rules)
	assert.Equal(t, time.Minute, cfg.pollInterval)
}

func TestRenewalConfigReadsItsVariables(t *testing.T) {
	var cfg renewalConfig

	require.NoError(t, loadRenewalConfig(envOf(map[string]string{
		"RENEWAL_WINDOW_DAYS": "45", "RENEWAL_TERM_MONTHS": "24", "RENEWAL_PAYMENT_RETRY_HOURS": "48",
		"RENEWAL_GRACE_DAYS": "15", "RENEWAL_POLL_INTERVAL_MS": "2000",
	}), &cfg))

	assert.Equal(t, 45, cfg.rules.WindowDays)
	assert.Equal(t, 24, cfg.rules.TermMonths)
	assert.Equal(t, 48*time.Hour, cfg.rules.RetryWithin)
	assert.Equal(t, 15, cfg.rules.GraceDays)
	assert.Equal(t, 2*time.Second, cfg.pollInterval)
}

func TestRenewalConfigRefusesWhatCannotWork(t *testing.T) {
	for name, env := range map[string]map[string]string{
		"not a number":    {"RENEWAL_WINDOW_DAYS": "many"},
		"zero days":       {"RENEWAL_WINDOW_DAYS": "0"},
		"window too long": {"RENEWAL_WINDOW_DAYS": "400"},
		"grace too long":  {"RENEWAL_GRACE_DAYS": "91"},
		"negative grace":  {"RENEWAL_GRACE_DAYS": "-1"},
		"bad term":        {"RENEWAL_TERM_MONTHS": "x"},
		"bad retry":       {"RENEWAL_PAYMENT_RETRY_HOURS": "x"},
		"bad poll":        {"RENEWAL_POLL_INTERVAL_MS": "x"},
	} {
		var cfg renewalConfig
		assert.Error(t, loadRenewalConfig(envOf(env), &cfg), name)
	}
}
