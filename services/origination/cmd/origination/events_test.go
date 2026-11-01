package main

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/memory"
)

func eventsEnv(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestLoadEventsConfigDefaults(t *testing.T) {
	var cfg eventsConfig

	require.NoError(t, loadEventsConfig(eventsEnv(nil), &cfg))

	assert.Empty(t, cfg.topicARN)
	assert.Equal(t, time.Second, cfg.pollInterval)
}

func TestLoadEventsConfigReadsTheTopicAndTheInterval(t *testing.T) {
	var cfg eventsConfig

	require.NoError(t, loadEventsConfig(eventsEnv(map[string]string{
		"EVENTS_TOPIC_ARN":        "arn:aws:sns:us-east-2:000000000000:solventa-staging-origination",
		"OUTBOX_POLL_INTERVAL_MS": "250",
	}), &cfg))

	assert.Equal(t, "arn:aws:sns:us-east-2:000000000000:solventa-staging-origination", cfg.topicARN)
	assert.Equal(t, 250*time.Millisecond, cfg.pollInterval)
}

func TestLoadEventsConfigRefusesAnInvalidInterval(t *testing.T) {
	for _, raw := range []string{"0", "-5", "soon"} {
		var cfg eventsConfig
		err := loadEventsConfig(eventsEnv(map[string]string{"OUTBOX_POLL_INTERVAL_MS": raw}), &cfg)
		assert.ErrorContains(t, err, "OUTBOX_POLL_INTERVAL_MS", raw)
	}
}

func TestTheServiceDoesNotStartWithAnInvalidOutboxInterval(t *testing.T) {
	_, err := loadConfig(eventsEnv(map[string]string{
		"REDIS_ADDR": "localhost:6379", "OPEN_FINANCE_URL": "http://localhost:9000", "OUTBOX_POLL_INTERVAL_MS": "x",
	}))
	assert.ErrorContains(t, err, "OUTBOX_POLL_INTERVAL_MS")
}

func TestWithoutATopicTheEventsStayInTheOutbox(t *testing.T) {
	buf := captureLog(t)

	relay, err := newEventRelay(context.Background(), eventsConfig{pollInterval: time.Second}, memory.NewOutbox())

	require.NoError(t, err)
	assert.Nil(t, relay)
	assert.Contains(t, buf.String(), "EVENTS_TOPIC_ARN is not set: domain events (CustomerLinked) stay in the outbox and are not published")
}

func TestWithATopicTheRelayIsBuilt(t *testing.T) {
	t.Setenv("AWS_REGION", "us-east-2")
	t.Setenv("AWS_ACCESS_KEY_ID", "test")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "test")
	buf := captureLog(t)

	relay, err := newEventRelay(context.Background(), eventsConfig{
		topicARN: "arn:aws:sns:us-east-2:000000000000:solventa-staging-origination", pollInterval: 2 * time.Second,
	}, memory.NewOutbox())

	require.NoError(t, err)
	assert.NotNil(t, relay)
	assert.Contains(t, buf.String(), "domain events: outbox relay to arn:aws:sns:us-east-2:000000000000:solventa-staging-origination every 2s")
}
