package main

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"time"

	goredis "github.com/redis/go-redis/v9"

	redisadapter "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/adapters/redis"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/ports"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/readcache"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/review"
)

// Variables of the read cache of the insured's claim queries (HA-L02).
// Without REDIS_ADDR the queries read the database only, as before.
const (
	envRedisAddr      = "REDIS_ADDR"
	envReadBudget     = "CLAIM_READ_BUDGET_MS"
	envReadCacheTTL   = "CLAIM_READ_CACHE_TTL_SECONDS"
	minReadBudgetMS   = 50
	maxReadBudgetMS   = 5000
	minReadCacheTTL   = 1
	maxReadCacheTTL   = 300
	readCacheTTLLimit = "5 minutes, the freshness HA-L02 allows"
)

// readCacheConfig is how the read cache is set up.
type readCacheConfig struct {
	addr   string
	budget time.Duration
	ttl    time.Duration
}

// loadReadCacheConfig reads the read cache: off without REDIS_ADDR, a budget
// of 900 ms and a TTL of 5 minutes unless the environment says otherwise.
func loadReadCacheConfig(getenv func(string) string) (readCacheConfig, error) {
	cfg := readCacheConfig{addr: getenv(envRedisAddr), budget: readcache.DefaultBudget, ttl: redisadapter.DefaultTTL}
	if raw := getenv(envReadBudget); raw != "" {
		ms, err := strconv.Atoi(raw)
		if err != nil || ms < minReadBudgetMS || ms > maxReadBudgetMS {
			return readCacheConfig{}, fmt.Errorf("%s must be a whole number of milliseconds between %d and %d, got %q",
				envReadBudget, minReadBudgetMS, maxReadBudgetMS, raw)
		}
		cfg.budget = time.Duration(ms) * time.Millisecond
	}
	if raw := getenv(envReadCacheTTL); raw != "" {
		seconds, err := strconv.Atoi(raw)
		if err != nil || seconds < minReadCacheTTL || seconds > maxReadCacheTTL {
			return readCacheConfig{}, fmt.Errorf("%s must be a whole number of seconds between %d and %d (%s), got %q",
				envReadCacheTTL, minReadCacheTTL, maxReadCacheTTL, readCacheTTLLimit, raw)
		}
		cfg.ttl = time.Duration(seconds) * time.Second
	}
	return cfg, nil
}

// newReadCache connects to Redis. A failed ping is not fatal: the cache is a
// fallback, and every call of the adapter already reads a failure as a miss.
func newReadCache(ctx context.Context, cfg readCacheConfig) ports.ReadCache {
	client := goredis.NewClient(&goredis.Options{Addr: cfg.addr})
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := client.Ping(pingCtx).Err(); err != nil {
		log.Printf("read cache (HA-L02): Redis at %s does not answer yet (%v); the queries keep reading the database", cfg.addr, err)
	}
	return redisadapter.NewReadCache(client, cfg.ttl)
}

// cachedReviewer is the review of the advisor whose history of the status of
// a claim goes through the read cache (HA-L02). The review itself, and its
// writes, never read the cache.
type cachedReviewer struct {
	*review.Service
	history *readcache.History
}

// History reads the history through the read cache.
func (r cachedReviewer) History(ctx context.Context, claimID, owner string) (review.History, error) {
	return r.history.History(ctx, claimID, owner)
}
