package redis_test

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	redisadapter "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/adapters/redis"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/ports"
)

// The read cache of the claim queries on Redis (HA-L02).

func newCache(t *testing.T, ttl time.Duration) (*redisadapter.ReadCache, *miniredis.Miniredis) {
	t.Helper()
	srv := miniredis.RunT(t)
	client := goredis.NewClient(&goredis.Options{Addr: srv.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	return redisadapter.NewReadCache(client, ttl), srv
}

func TestAReadIsStoredAndReadBack(t *testing.T) {
	cache, srv := newCache(t, 0)
	at := time.Date(2026, 11, 8, 14, 30, 0, 0, time.FixedZone("COT", -5*3600))

	cache.Put(context.Background(), "owner:sub-ana:claim:c-1", ports.CachedRead{Data: []byte(`{"ID":"c-1"}`), At: at})
	got, ok := cache.Get(context.Background(), "owner:sub-ana:claim:c-1")

	require.True(t, ok)
	assert.JSONEq(t, `{"ID":"c-1"}`, string(got.Data))
	assert.True(t, at.Equal(got.At))
	assert.Equal(t, time.UTC, got.At.Location())
	assert.True(t, srv.Exists("claims:read:v1:owner:sub-ana:claim:c-1"), "the keys carry their own prefix")
	assert.Equal(t, redisadapter.DefaultTTL, srv.TTL("claims:read:v1:owner:sub-ana:claim:c-1"))
}

func TestAnEntryExpiresWithItsTTL(t *testing.T) {
	cache, srv := newCache(t, time.Minute)
	cache.Put(context.Background(), "k", ports.CachedRead{Data: []byte(`1`), At: time.Now()})

	srv.FastForward(61 * time.Second)
	_, ok := cache.Get(context.Background(), "k")

	assert.False(t, ok, "a change older than the TTL can never be hidden by the cache")
}

func TestAMissIsNotFound(t *testing.T) {
	cache, _ := newCache(t, 0)
	_, ok := cache.Get(context.Background(), "nothing")
	assert.False(t, ok)
}

func TestACorruptEntryIsAMiss(t *testing.T) {
	cache, srv := newCache(t, 0)
	require.NoError(t, srv.Set("claims:read:v1:k", "not json"))
	_, ok := cache.Get(context.Background(), "k")
	assert.False(t, ok)
}

func TestARedisDownNeverFailsTheRead(t *testing.T) {
	cache, srv := newCache(t, 0)
	srv.Close()

	cache.Put(context.Background(), "k", ports.CachedRead{Data: []byte(`1`), At: time.Now()})
	_, ok := cache.Get(context.Background(), "k")

	assert.False(t, ok)
}

func TestACanceledRequestStillReadsTheCache(t *testing.T) {
	cache, _ := newCache(t, 0)
	cache.Put(context.Background(), "k", ports.CachedRead{Data: []byte(`1`), At: time.Now()})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, ok := cache.Get(ctx, "k")

	assert.True(t, ok, "the fallback runs after the budget of the request ran out")
}
