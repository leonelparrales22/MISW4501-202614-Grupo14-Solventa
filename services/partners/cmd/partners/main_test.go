package main

import (
	"context"
	"testing"
	"time"

	sdkcloudwatch "github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/security"
)

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestLoadConfigDefaults(t *testing.T) {
	cfg, err := loadConfig(env(nil))
	require.NoError(t, err)
	assert.Equal(t, "8080", cfg.port)
	assert.Equal(t, "local", cfg.environment)
	assert.Equal(t, 24, cfg.graceHours)
	assert.Equal(t, time.Second, cfg.outboxPoll)
	assert.Equal(t, time.Minute, cfg.revocationPoll)
	assert.Nil(t, cfg.auth)
}

func TestLoadConfigValues(t *testing.T) {
	cfg, err := loadConfig(env(map[string]string{
		"PORT": "9000", "ENVIRONMENT": "staging", envGatewayRestAPIID: "api", envGatewayStageName: "v1",
		envGracePeriodHours: "48", envOutboxPollInterval: "250", envRevocationPollMS: "5000",
		security.EnvIssuer: "https://cognito-idp.us-east-2.amazonaws.com/pool", "AUTH_CLIENT_IDS": "client",
	}))
	require.NoError(t, err)
	assert.Equal(t, "9000", cfg.port)
	assert.Equal(t, "api", cfg.gatewayAPIID)
	assert.Equal(t, 48, cfg.graceHours)
	assert.Equal(t, 250*time.Millisecond, cfg.outboxPoll)
	assert.Equal(t, 5*time.Second, cfg.revocationPoll)
	require.NotNil(t, cfg.auth)
}

func TestLoadConfigErrors(t *testing.T) {
	for name, m := range map[string]map[string]string{
		"gateway alone":   {envGatewayRestAPIID: "api"},
		"grace":           {envGracePeriodHours: "x"},
		"outbox":          {envOutboxPollInterval: "0"},
		"revocation":      {envRevocationPollMS: "-1"},
		"auth incomplete": {security.EnvIssuer: "https://issuer"},
	} {
		_, err := loadConfig(env(m))
		assert.Error(t, err, name)
	}
}

func TestWithoutGatewayNorTopicNorVerifier(t *testing.T) {
	cfg, _ := loadConfig(env(nil))
	gw, err := newGateway(context.Background(), cfg)
	require.NoError(t, err)
	assert.Nil(t, gw)
	relay, err := newEventRelay(context.Background(), cfg, nil)
	require.NoError(t, err)
	assert.Nil(t, relay)
	opts, err := partnerOptions(context.Background(), cfg, nil, nil, nil)
	require.NoError(t, err)
	assert.Empty(t, opts, "without AUTH_ISSUER the routes are not mounted")

	opts, err = partnerOptions(context.Background(), cfg, nil, nil, tokenVerifier{})
	require.NoError(t, err)
	assert.Len(t, opts, 1)

	cfg.graceHours = 500
	_, err = partnerOptions(context.Background(), cfg, nil, nil, tokenVerifier{})
	require.Error(t, err)
	assert.False(t, systemClock{}.Now().IsZero())
}

type tokenVerifier struct{}

func (tokenVerifier) Verify(context.Context, string) (security.Claims, error) {
	return security.Claims{}, security.ErrInvalidToken
}

// HU-W3.2-03: the performance of the partner API needs the prefix of the SLO
// alarms and the user pool; the objective of RC-02 can be set.
func TestThePerformanceNeedsThePrefixAndTheUserPool(t *testing.T) {
	cfg, err := loadConfig(env(nil))
	require.NoError(t, err)
	opts, err := performanceOptions(context.Background(), cfg, tokenVerifier{})
	require.NoError(t, err)
	assert.Empty(t, opts, "without SLO_ALARM_PREFIX it is not mounted")

	cfg, err = loadConfig(env(map[string]string{envSLOAlarmPrefix: "solventa-stg-slo-", envSLOObjective: "99.5"}))
	require.NoError(t, err)
	assert.Equal(t, "solventa-stg-slo-", cfg.sloPrefix)
	assert.Equal(t, 99.5, cfg.sloObjective)
	opts, err = performanceOptions(context.Background(), cfg, nil)
	require.NoError(t, err)
	assert.Empty(t, opts, "without AUTH_ISSUER it is not mounted")

	opts, err = performanceRoutes(sdkcloudwatch.New(sdkcloudwatch.Options{Region: "us-east-2"}), cfg, tokenVerifier{})
	require.NoError(t, err)
	assert.Len(t, opts, 1)

	v, err := newVerifier(context.Background(), cfg)
	require.NoError(t, err)
	assert.Nil(t, v)
}

func TestTheObjectiveIsAPercentage(t *testing.T) {
	for _, raw := range []string{"0", "101", "abc", "-1"} {
		_, err := loadConfig(env(map[string]string{envSLOObjective: raw}))
		assert.Error(t, err, raw)
	}
}
