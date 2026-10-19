package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestLoadConfigDefaults(t *testing.T) {
	cfg, err := loadConfig(env(nil))

	require.NoError(t, err)
	assert.Equal(t, "8080", cfg.port)
	assert.Empty(t, cfg.databaseURL, "without DATABASE_URL the consent routes are not mounted")
	assert.False(t, cfg.auth.enabled, "without AUTH_ISSUER the evidence lookup is not mounted")
}

func TestLoadConfigReadsThePortAndTheDatabase(t *testing.T) {
	cfg, err := loadConfig(env(map[string]string{"PORT": "9090", "DATABASE_URL": "postgres://h/db"}))

	require.NoError(t, err)
	assert.Equal(t, "9090", cfg.port)
	assert.Equal(t, "postgres://h/db", cfg.databaseURL)
}

func TestLoadConfigEnablesTheHumanRoutesWithAValidAuthConfig(t *testing.T) {
	cfg, err := loadConfig(env(map[string]string{
		"AUTH_ISSUER":     "https://cognito-idp.us-east-1.amazonaws.com/us-east-1_abc",
		"AUTH_CLIENT_IDS": "web-client, mobile-client",
	}))

	require.NoError(t, err)
	assert.True(t, cfg.auth.enabled)
	assert.Equal(t, []string{"web-client", "mobile-client"}, cfg.auth.config.ClientIDs)
}

func TestLoadConfigRejectsAnIncompleteAuthConfig(t *testing.T) {
	_, err := loadConfig(env(map[string]string{"AUTH_ISSUER": "https://cognito-idp.us-east-1.amazonaws.com/us-east-1_abc"}))

	assert.ErrorContains(t, err, "AUTH_ISSUER")
}
