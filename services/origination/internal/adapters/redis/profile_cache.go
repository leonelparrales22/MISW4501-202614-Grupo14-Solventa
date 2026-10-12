// Package redis implements the Perfil_Riesgo_Cache port on ElastiCache Redis
// with the cache-aside pattern.
package redis

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
)

// DefaultTTL is the validity of a cached profile (30 days).
const DefaultTTL = 30 * 24 * time.Hour

// DefaultOperationTimeout bounds each Redis call so a slow cache never blocks
// the offer journey.
const DefaultOperationTimeout = 100 * time.Millisecond

type entry struct {
	CustomerID   string    `json:"customer_id"`
	Score        float64   `json:"score"`
	Category     string    `json:"category"`
	CalculatedAt time.Time `json:"calculated_at"`
	// Partial is absent in entries written before HU-W1.2-02, which are read
	// as complete.
	Partial bool `json:"partial,omitempty"`
}

// ProfileCache implements ports.RiskProfileCache.
type ProfileCache struct {
	client  *goredis.Client
	ttl     time.Duration
	timeout time.Duration
}

// NewProfileCache builds the cache. Zero ttl or timeout use the defaults.
func NewProfileCache(client *goredis.Client, ttl, opTimeout time.Duration) *ProfileCache {
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	if opTimeout <= 0 {
		opTimeout = DefaultOperationTimeout
	}
	return &ProfileCache{client: client, ttl: ttl, timeout: opTimeout}
}

func key(customerID string) string {
	return "risk_profile:" + customerID
}

// Get returns the cached profile; found=false on a miss.
func (c *ProfileCache) Get(ctx context.Context, customerID string) (domain.RiskProfile, bool, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	raw, err := c.client.Get(ctx, key(customerID)).Bytes()
	if errors.Is(err, goredis.Nil) {
		return domain.RiskProfile{}, false, nil
	}
	if err != nil {
		return domain.RiskProfile{}, false, err
	}
	var e entry
	if err := json.Unmarshal(raw, &e); err != nil {
		return domain.RiskProfile{}, false, err
	}
	return domain.RiskProfile{CustomerID: e.CustomerID, Score: e.Score, Category: e.Category, CalculatedAt: e.CalculatedAt, Partial: e.Partial}, true, nil
}

// Set stores the profile with the configured TTL.
func (c *ProfileCache) Set(ctx context.Context, p domain.RiskProfile) error {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	raw, err := json.Marshal(entry{CustomerID: p.CustomerID, Score: p.Score, Category: p.Category, CalculatedAt: p.CalculatedAt, Partial: p.Partial})
	if err != nil {
		return err
	}
	return c.client.Set(ctx, key(p.CustomerID), raw, c.ttl).Err()
}

// Invalidate removes the cached profile; a missing key is not an error.
func (c *ProfileCache) Invalidate(ctx context.Context, customerID string) error {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	return c.client.Del(ctx, key(customerID)).Err()
}
