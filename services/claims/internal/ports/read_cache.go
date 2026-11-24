package ports

import (
	"context"
	"sync"
	"time"
)

// CachedRead is a read of the insured's claims kept in the read cache
// (HA-L02): the serialized result and when the source answered it.
type CachedRead struct {
	Data []byte
	At   time.Time
}

// ReadCache keeps the last read of every claim query so that the query can
// still answer when the database is slow or down (HA-L02). It is a fallback:
// a failure of the cache never fails a read, so the adapter logs it and
// answers as a miss.
type ReadCache interface {
	// Get returns the stored read, or false when there is none.
	Get(ctx context.Context, key string) (CachedRead, bool)
	// Put stores the read, replacing the previous one.
	Put(ctx context.Context, key string, read CachedRead)
}

// Freshness tells the HTTP layer where a read came from. Stale is true when
// the source did not answer in time and the read came from the cache; AsOf is
// then when the source answered it.
type Freshness struct {
	mu    sync.Mutex
	stale bool
	asOf  time.Time
}

// MarkStale records that the read came from the cache, read at asOf. When
// several reads of one request come from the cache, the oldest one wins.
func (f *Freshness) MarkStale(asOf time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.stale || asOf.Before(f.asOf) {
		f.asOf = asOf
	}
	f.stale = true
}

// Stale returns whether the read came from the cache and when it was read.
func (f *Freshness) Stale() (bool, time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.stale, f.asOf
}

type freshnessKey struct{}

// TrackFreshness returns a context whose reads report their freshness to f.
func TrackFreshness(ctx context.Context) (context.Context, *Freshness) {
	f := &Freshness{}
	return context.WithValue(ctx, freshnessKey{}, f), f
}

// FreshnessFrom returns the freshness tracked by the context, or nil.
func FreshnessFrom(ctx context.Context) *Freshness {
	f, _ := ctx.Value(freshnessKey{}).(*Freshness)
	return f
}
