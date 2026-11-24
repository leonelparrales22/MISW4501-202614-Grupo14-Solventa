// Package redis implements the read cache of the claim queries (HA-L02) on
// ElastiCache Redis.
package redis

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/ports"
)

// Defaults of the cache.
const (
	// DefaultTTL bounds how old a read served from the cache can be: a
	// change of status shows up in the cache, or the cache stops answering
	// for that claim, within 5 minutes (HA-L02 criterion 4).
	DefaultTTL = 5 * time.Minute
	// DefaultReadTimeout is short on purpose: the cache is the fallback of a
	// read that already missed its budget, so it cannot add much more.
	DefaultReadTimeout = 200 * time.Millisecond
	// DefaultWriteTimeout is wider: the write happens after the source
	// answered and is what keeps the cache warm.
	DefaultWriteTimeout = 2 * time.Second
)

// keyPrefix keeps the keys of this cache apart from any other in the cluster.
const keyPrefix = "claims:read:v1:"

type entry struct {
	At   time.Time       `json:"at"`
	Data json.RawMessage `json:"data"`
}

// ReadCache implements ports.ReadCache.
type ReadCache struct {
	client       goredis.Cmdable
	ttl          time.Duration
	readTimeout  time.Duration
	writeTimeout time.Duration
}

// NewReadCache builds the cache. A zero ttl uses DefaultTTL.
func NewReadCache(client goredis.Cmdable, ttl time.Duration) *ReadCache {
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	return &ReadCache{client: client, ttl: ttl, readTimeout: DefaultReadTimeout, writeTimeout: DefaultWriteTimeout}
}

// Get returns the stored read. An error of Redis is logged and read as a
// miss: the cache never fails a read.
func (c *ReadCache) Get(ctx context.Context, key string) (ports.CachedRead, bool) {
	// The read already missed its budget, so it is not the request's deadline
	// that bounds this call but its own timeout.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), c.readTimeout)
	defer cancel()
	raw, err := c.client.Get(ctx, keyPrefix+key).Bytes()
	if errors.Is(err, goredis.Nil) {
		return ports.CachedRead{}, false
	}
	if err != nil {
		slog.WarnContext(ctx, "could not read the claim read cache", "error", err)
		return ports.CachedRead{}, false
	}
	var e entry
	if err := json.Unmarshal(raw, &e); err != nil {
		slog.WarnContext(ctx, "corrupt entry in the claim read cache", "error", err)
		return ports.CachedRead{}, false
	}
	return ports.CachedRead{Data: e.Data, At: e.At}, true
}

// Put stores the read with the configured TTL. An error is logged: the
// source already answered.
func (c *ReadCache) Put(ctx context.Context, key string, read ports.CachedRead) {
	raw, err := json.Marshal(entry{At: read.At.UTC(), Data: read.Data})
	if err != nil {
		slog.WarnContext(ctx, "could not serialize the claim read", "error", err)
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), c.writeTimeout)
	defer cancel()
	if err := c.client.Set(ctx, keyPrefix+key, raw, c.ttl).Err(); err != nil {
		slog.WarnContext(ctx, "could not write the claim read cache", "error", err)
	}
}
