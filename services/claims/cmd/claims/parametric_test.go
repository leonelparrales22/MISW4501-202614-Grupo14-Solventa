package main

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/adapters/memory"
)

// The composition of the parametric engine (HU-M2.4-01).

func TestTheParametricEngineReadsItsQueueTopicAndPoll(t *testing.T) {
	cfg, err := loadParametricConfig(env(map[string]string{
		"EVENTS_QUEUE_URL": "http://localstack:4566/000000000000/solventa-local-claims",
		"EVENTS_TOPIC_ARN": "arn:aws:sns:us-east-2:000000000000:solventa-local-claims",
	}))
	require.NoError(t, err)
	assert.Equal(t, "http://localstack:4566/000000000000/solventa-local-claims", cfg.queueURL)
	assert.Equal(t, "arn:aws:sns:us-east-2:000000000000:solventa-local-claims", cfg.topicARN)
	assert.Equal(t, time.Second, cfg.pollInterval)

	cfg, err = loadParametricConfig(env(map[string]string{"OUTBOX_POLL_INTERVAL_MS": "250"}))
	require.NoError(t, err)
	assert.Equal(t, 250*time.Millisecond, cfg.pollInterval)
}

func TestAnInvalidPollOfTheRelayStopsTheService(t *testing.T) {
	for _, raw := range []string{"0", "99", "60001", "fast"} {
		_, err := loadParametricConfig(env(map[string]string{"OUTBOX_POLL_INTERVAL_MS": raw}))
		assert.Error(t, err, raw)
	}
}

func TestWithoutTheDatabaseTheEngineIsNotMounted(t *testing.T) {
	err := startParametric(context.Background(), memory.NewCaseStore(), nil, nil, env(map[string]string{
		"EVENTS_QUEUE_URL": "http://localstack:4566/000000000000/solventa-local-claims",
	}))
	assert.NoError(t, err)
}
