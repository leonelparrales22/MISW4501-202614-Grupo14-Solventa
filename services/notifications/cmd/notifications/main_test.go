package main

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/events"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/notifications/internal/adapters/postgres"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/notifications/internal/domain"
)

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestConfigDefaultsLeaveEverythingOptional(t *testing.T) {
	cfg, err := loadConfig(env(nil))

	require.NoError(t, err)
	assert.Equal(t, "8080", cfg.port)
	assert.Empty(t, cfg.databaseURL)
	assert.False(t, cfg.auth.enabled)
	assert.Empty(t, cfg.pushProviderURL)
	assert.Equal(t, 2*time.Second, cfg.pushProviderTimeout)
	assert.Equal(t, time.Second, cfg.pushPollInterval)
	assert.Equal(t, time.Second, cfg.pushRetryBase)
	assert.Equal(t, time.Minute, cfg.pushRetryMax)
}

func TestConfigReadsItsVariables(t *testing.T) {
	cfg, err := loadConfig(env(map[string]string{
		"PORT": "9090", "DATABASE_URL": "postgres://x", "EVENTS_QUEUE_URL": "http://sqs/q", "PUSH_PROVIDER_URL": "http://push:9070",
		"PUSH_PROVIDER_TIMEOUT_MS": "500", "PUSH_POLL_INTERVAL_MS": "200", "PUSH_RETRY_BASE_MS": "100", "PUSH_RETRY_MAX_MS": "1000",
		"AUTH_ISSUER": "https://cognito-idp.us-east-2.amazonaws.com/pool", "AUTH_CLIENT_IDS": "client-1",
	}))

	require.NoError(t, err)
	assert.Equal(t, "9090", cfg.port)
	assert.True(t, cfg.auth.enabled)
	assert.Equal(t, "http://sqs/q", cfg.eventsQueueURL)
	assert.Equal(t, 500*time.Millisecond, cfg.pushProviderTimeout)
	assert.Equal(t, 200*time.Millisecond, cfg.pushPollInterval)
	assert.Equal(t, 100*time.Millisecond, cfg.pushRetryBase)
	assert.Equal(t, time.Second, cfg.pushRetryMax)
}

func TestConfigRefusesWhatCannotWork(t *testing.T) {
	for name, vars := range map[string]map[string]string{
		"bad timeout":       {"PUSH_PROVIDER_TIMEOUT_MS": "0"},
		"bad poll":          {"PUSH_POLL_INTERVAL_MS": "soon"},
		"max below base":    {"PUSH_RETRY_BASE_MS": "1000", "PUSH_RETRY_MAX_MS": "10"},
		"incomplete issuer": {"AUTH_ISSUER": "https://issuer"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := loadConfig(env(vars))
			require.Error(t, err)
		})
	}
}

func TestWithoutAProviderThereIsNoWorker(t *testing.T) {
	w, err := newPushWorker(config{}, nil)

	require.NoError(t, err)
	assert.Nil(t, w)
}

func TestWithAProviderTheWorkerSendsToIt(t *testing.T) {
	cfg, err := loadConfig(env(map[string]string{"PUSH_PROVIDER_URL": "http://push:9070"}))
	require.NoError(t, err)

	w, err := newPushWorker(cfg, postgres.NewStore(nil))

	require.NoError(t, err)
	assert.NotNil(t, w)
}

func TestWithoutAQueueThereIsNoConsumer(t *testing.T) {
	c, err := newEventConsumer(context.Background(), config{}, nil)

	require.NoError(t, err)
	assert.Nil(t, c)
}

type recorder struct{ got []domain.ParametricPayout }

func (r *recorder) RecordPayout(_ context.Context, p domain.ParametricPayout) error {
	r.got = append(r.got, p)
	return nil
}

func TestTheRouterHandlesTheAutomaticPayments(t *testing.T) {
	rec := &recorder{}
	router, err := eventRouter(rec)
	require.NoError(t, err)
	e, err := events.New("e-1", "ParametricPayoutCompleted", "claims", 1, time.Now(), map[string]any{
		"payout_id": "ppo-1", "subject": "sub-a", "amount_minor": 100, "currency": "COP",
		"trigger": map[string]any{"parameter": "flight_delay", "event_reference": "AV-8421"},
	})
	require.NoError(t, err)

	require.NoError(t, router.Handle(context.Background(), e))

	assert.Len(t, rec.got, 1)
}

func TestWithoutTheIssuerTheCenterIsNotMounted(t *testing.T) {
	assert.Empty(t, centerOptions(context.Background(), config{}, nil))
}
