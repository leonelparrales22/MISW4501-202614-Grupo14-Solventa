package main

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/security"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/renewal"
)

func TestPolicyCoreConfigDefaultsToNoCoreAnd700ms(t *testing.T) {
	var cfg policyCoreConfig

	require.NoError(t, loadPolicyCoreConfig(envOf(nil), &cfg))

	assert.Empty(t, cfg.url)
	assert.Equal(t, 700*time.Millisecond, cfg.timeout)
	assert.Nil(t, newPolicyPortfolio(cfg), "without POLICY_CORE_URL the wallet has no core")
}

func TestPolicyCoreConfigReadsItsVariables(t *testing.T) {
	var cfg policyCoreConfig

	require.NoError(t, loadPolicyCoreConfig(envOf(map[string]string{
		"POLICY_CORE_URL": "http://policy-core:9060", "POLICY_CORE_TIMEOUT_MS": "1500",
	}), &cfg))

	assert.Equal(t, "http://policy-core:9060", cfg.url)
	assert.Equal(t, 1500*time.Millisecond, cfg.timeout)
	assert.NotNil(t, newPolicyPortfolio(cfg))
}

func TestPolicyCoreConfigRefusesABadTimeout(t *testing.T) {
	var cfg policyCoreConfig

	require.Error(t, loadPolicyCoreConfig(envOf(map[string]string{"POLICY_CORE_TIMEOUT_MS": "-1"}), &cfg))
}

type noRenewals struct{}

func (noRenewals) ListPolicies(context.Context) ([]renewal.PolicyView, error) { return nil, nil }

type noVerifier struct{}

func (noVerifier) Verify(context.Context, string) (security.Claims, error) {
	return security.Claims{}, nil
}

func TestWalletIsMountedOnlyWithASourceOfPolicies(t *testing.T) {
	assert.Empty(t, walletOptions(nil, nil, noVerifier{}), "without the charge and the core there is nothing to list")
	assert.Len(t, walletOptions(noRenewals{}, nil, noVerifier{}), 1)
}
