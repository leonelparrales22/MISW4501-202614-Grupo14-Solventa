package main

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/security"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
)

func TestAcceptanceRoutesAreDisabledWithoutAuth(t *testing.T) {
	cfg, err := loadConfig(env(requiredEnv()))

	require.NoError(t, err)
	assert.False(t, cfg.acceptances.enabled)
	assert.Equal(t, time.Second, cfg.acceptances.processor.PollInterval)
	assert.Equal(t, 5*time.Second, cfg.acceptances.processor.AttemptTimeout)
	assert.Equal(t, 2*time.Second, cfg.acceptances.processor.RetryBase)
	assert.Equal(t, time.Minute, cfg.acceptances.processor.RetryMax)
	assert.Equal(t, 2, cfg.acceptances.flakyFailures)
}

func TestAcceptanceConfigWithAuthAndOverrides(t *testing.T) {
	m := requiredEnv()
	m["AUTH_ISSUER"] = "https://cognito-idp.us-east-2.amazonaws.com/pool"
	m["AUTH_CLIENT_IDS"] = "web,mobile"
	m["ACCEPTANCE_POLL_INTERVAL_MS"] = "500"
	m["ACCEPTANCE_ATTEMPT_TIMEOUT_MS"] = "1500"
	m["ACCEPTANCE_RETRY_BASE_MS"] = "1000"
	m["ACCEPTANCE_RETRY_MAX_MS"] = "8000"
	m["UNDERWRITING_STUB_FLAKY_FAILURES"] = "0"

	cfg, err := loadConfig(env(m))

	require.NoError(t, err)
	a := cfg.acceptances
	assert.True(t, a.enabled)
	assert.Equal(t, "https://cognito-idp.us-east-2.amazonaws.com/pool", a.auth.Issuer)
	assert.Equal(t, []string{"web", "mobile"}, a.auth.ClientIDs)
	assert.Equal(t, 500*time.Millisecond, a.processor.PollInterval)
	assert.Equal(t, 1500*time.Millisecond, a.processor.AttemptTimeout)
	assert.Equal(t, time.Second, a.processor.RetryBase)
	assert.Equal(t, 8*time.Second, a.processor.RetryMax)
	assert.Zero(t, a.flakyFailures)
}

func TestAcceptanceConfigRejectsInvalidValues(t *testing.T) {
	for name, extra := range map[string]map[string]string{
		"auth without clients": {"AUTH_ISSUER": "https://issuer"},
		"poll":                 {"ACCEPTANCE_POLL_INTERVAL_MS": "0"},
		"attempt":              {"ACCEPTANCE_ATTEMPT_TIMEOUT_MS": "x"},
		"base":                 {"ACCEPTANCE_RETRY_BASE_MS": "-1"},
		"max":                  {"ACCEPTANCE_RETRY_MAX_MS": "x"},
		"max below base":       {"ACCEPTANCE_RETRY_BASE_MS": "5000", "ACCEPTANCE_RETRY_MAX_MS": "1000"},
		"flaky":                {"UNDERWRITING_STUB_FLAKY_FAILURES": "-1"},
	} {
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

func TestNewAcceptanceServiceProcessesInTheBackground(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	svc := newAcceptanceService(ctx, acceptanceConfig{flakyFailures: 0})
	caller := security.WithClaims(ctx, security.Claims{Subject: "cust-1", Roles: []security.Role{security.RoleInsured}})

	a, created, err := svc.Accept(caller, "demo-active-1", domain.ChannelWeb)
	require.NoError(t, err)
	require.True(t, created)

	require.Eventually(t, func() bool {
		got, err := svc.Get(caller, a.ID)
		return err == nil && got.Status == domain.AcceptanceIssuancePending
	}, 3*time.Second, 20*time.Millisecond)
}
