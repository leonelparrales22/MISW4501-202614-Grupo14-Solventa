package redis_test

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	adapter "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/redis"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
)

func newCache(t *testing.T, ttl time.Duration) (*adapter.ProfileCache, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	client := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	return adapter.NewProfileCache(client, ttl, 200*time.Millisecond), mr
}

func TestSetThenGetRoundTrip(t *testing.T) {
	c, mr := newCache(t, 0)
	p := domain.RiskProfile{CustomerID: "c1", Score: 0.37, Category: "standard", CalculatedAt: time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)}

	require.NoError(t, c.Set(context.Background(), p))
	got, found, err := c.Get(context.Background(), "c1")

	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, p.CustomerID, got.CustomerID)
	assert.Equal(t, p.Score, got.Score)
	assert.Equal(t, p.Category, got.Category)
	assert.True(t, p.CalculatedAt.Equal(got.CalculatedAt))
	assert.Equal(t, 30*24*time.Hour, mr.TTL("risk_profile:c1"), "default TTL is 30 days")
}

func TestGetMissIsNotAnError(t *testing.T) {
	c, _ := newCache(t, 0)

	_, found, err := c.Get(context.Background(), "ghost")

	require.NoError(t, err)
	assert.False(t, found)
}

func TestCustomTTLAndExpiry(t *testing.T) {
	c, mr := newCache(t, time.Hour)
	require.NoError(t, c.Set(context.Background(), domain.RiskProfile{CustomerID: "c1"}))
	assert.Equal(t, time.Hour, mr.TTL("risk_profile:c1"))

	mr.FastForward(2 * time.Hour)
	_, found, err := c.Get(context.Background(), "c1")
	require.NoError(t, err)
	assert.False(t, found, "an expired entry is a miss")
}

func TestGetCorruptedEntryIsAnError(t *testing.T) {
	c, mr := newCache(t, 0)
	require.NoError(t, mr.Set("risk_profile:c1", "{not json"))

	_, found, err := c.Get(context.Background(), "c1")

	require.Error(t, err)
	assert.False(t, found)
}

func TestRedisDownReturnsErrors(t *testing.T) {
	c, mr := newCache(t, 0)
	mr.Close()

	_, _, getErr := c.Get(context.Background(), "c1")
	setErr := c.Set(context.Background(), domain.RiskProfile{CustomerID: "c1"})

	assert.Error(t, getErr)
	assert.Error(t, setErr)
}
