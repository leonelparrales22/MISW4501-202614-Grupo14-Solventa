package main

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/ports"
)

// The composition of the read cache (HA-L02).

func TestTheReadCacheIsOffWithoutRedis(t *testing.T) {
	cfg, err := loadReadCacheConfig(env(nil))

	require.NoError(t, err)
	assert.Empty(t, cfg.addr)
	assert.Equal(t, 900*time.Millisecond, cfg.budget)
	assert.Equal(t, 5*time.Minute, cfg.ttl)
}

func TestTheBudgetAndTheTTLOfTheReadCache(t *testing.T) {
	cfg, err := loadReadCacheConfig(env(map[string]string{
		"REDIS_ADDR": "redis:6379", "CLAIM_READ_BUDGET_MS": "500", "CLAIM_READ_CACHE_TTL_SECONDS": "120"}))

	require.NoError(t, err)
	assert.Equal(t, "redis:6379", cfg.addr)
	assert.Equal(t, 500*time.Millisecond, cfg.budget)
	assert.Equal(t, 2*time.Minute, cfg.ttl)

	for name, vars := range map[string]map[string]string{
		"budget too short":      {"CLAIM_READ_BUDGET_MS": "10"},
		"budget not a number":   {"CLAIM_READ_BUDGET_MS": "900ms"},
		"ttl over five minutes": {"CLAIM_READ_CACHE_TTL_SECONDS": "301"},
		"ttl zero":              {"CLAIM_READ_CACHE_TTL_SECONDS": "0"},
	} {
		_, err := loadReadCacheConfig(env(vars))
		assert.Error(t, err, name)
	}
}

func TestTheReadCacheConnectsToRedis(t *testing.T) {
	srv := miniredis.RunT(t)
	cache := newReadCache(context.Background(), readCacheConfig{addr: srv.Addr(), ttl: time.Minute})

	cache.Put(context.Background(), "k", ports.CachedRead{Data: []byte(`1`), At: time.Now()})
	_, ok := cache.Get(context.Background(), "k")

	assert.True(t, ok)
}

func TestARedisThatDoesNotAnswerYetIsNotFatal(t *testing.T) {
	cache := newReadCache(context.Background(), readCacheConfig{addr: "127.0.0.1:1", ttl: time.Minute})
	_, ok := cache.Get(context.Background(), "k")
	assert.False(t, ok)
}
