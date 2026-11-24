package main

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
)

// HU-W1.6-02: the deadlines of the instalments, real by default and shortened
// by variables for a demonstration.

func TestTheInstalmentsUseTheRealDeadlinesByDefault(t *testing.T) {
	var cfg installmentConfig

	require.NoError(t, loadInstallmentConfig(envOf(nil), &cfg))

	assert.Equal(t, domain.DefaultInstallmentRules, cfg.rules)
	assert.Equal(t, time.Hour, cfg.interval)
}

func TestTheDefenceClockShortensTheInstalmentsToMinutes(t *testing.T) {
	var cfg installmentConfig

	require.NoError(t, loadInstallmentConfig(envOf(map[string]string{
		"INSTALLMENT_DAY_MS": "2000", "INSTALLMENT_SWEEP_INTERVAL_MS": "2000", "INSTALLMENT_UNKNOWN_RETRY_MS": "5000",
	}), &cfg))

	assert.Equal(t, 2*time.Second, cfg.rules.Day)
	assert.Equal(t, 2*time.Second, cfg.interval)
	assert.Equal(t, 5*time.Second, cfg.rules.RetryUnknownAfter)
	assert.Equal(t, 30, cfg.rules.GraceDays, "the rules do not change, only the length of a day")
	assert.Equal(t, []int{1, 8, 15}, cfg.rules.AttemptDays)
}

func TestInstalmentVariablesThatCannotBeAppliedAreRefused(t *testing.T) {
	for name, env := range map[string]map[string]string{
		"day too long":        {"INSTALLMENT_DAY_MS": "172800000"},
		"no day":              {"INSTALLMENT_DAY_MS": "0"},
		"attempt after grace": {"INSTALLMENT_GRACE_DAYS": "10", "INSTALLMENT_ATTEMPT_DAYS": "1,8,15"},
		"attempts not a list": {"INSTALLMENT_ATTEMPT_DAYS": "uno"},
		"no sweep":            {"INSTALLMENT_SWEEP_INTERVAL_MS": "0"},
	} {
		var cfg installmentConfig
		assert.Error(t, loadInstallmentConfig(envOf(env), &cfg), name)
	}
}
