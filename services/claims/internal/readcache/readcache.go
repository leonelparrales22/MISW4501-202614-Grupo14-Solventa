// Package readcache puts a read cache in front of the insured's claim queries
// (HA-L02, scenario EC-L02). Every read goes to the database first, with a
// budget of 900 ms; when the database answers, the read is stored in the
// cache, and when it is slow or down, the last stored read is served and
// marked as possibly out of date, with the time it was read.
//
// A read the database answered with a business error (not found, not yours,
// invalid query) is not degraded: the database answered, there is nothing to
// fall back from.
package readcache

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/security"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/filing"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/ports"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/review"
)

// DefaultBudget is how long a read waits for the database before it falls
// back to the cache: the value validated in the EC-L02 experiment.
const DefaultBudget = 900 * time.Millisecond

// Clock tells the time a read was answered.
type Clock interface {
	Now() time.Time
}

// ClaimSource is the query of the insured's own claims (filing.QueryService).
type ClaimSource interface {
	List(ctx context.Context, subject string, q filing.ClaimQuery) (filing.ClaimPage, error)
	Get(ctx context.Context, id string) (domain.ClaimRecord, error)
	Receipt(ctx context.Context, id string) (domain.ClaimRecord, []byte, error)
	EvidenceLinks(ctx context.Context, c domain.ClaimRecord) []filing.EvidenceLink
}

// HistorySource is the history of the status of a claim (review.Service).
type HistorySource interface {
	History(ctx context.Context, claimID, owner string) (review.History, error)
}

type reader struct {
	cache  ports.ReadCache
	budget time.Duration
	clock  Clock
}

func newReader(cache ports.ReadCache, budget time.Duration, clock Clock) reader {
	if budget <= 0 {
		budget = DefaultBudget
	}
	return reader{cache: cache, budget: budget, clock: clock}
}

// Claims serves the list and the detail of the insured's claims with the read
// cache. The receipt and the evidence links are not cached: the receipt is a
// document and the links are signed for a short time.
type Claims struct {
	source ClaimSource
	reader reader
}

// NewClaims builds the cached query. A zero budget uses DefaultBudget.
func NewClaims(source ClaimSource, cache ports.ReadCache, budget time.Duration, clock Clock) *Claims {
	return &Claims{source: source, reader: newReader(cache, budget, clock)}
}

// List returns one page of the subject's claims.
func (c *Claims) List(ctx context.Context, subject string, q filing.ClaimQuery) (filing.ClaimPage, error) {
	raw, err := json.Marshal(q)
	if err != nil {
		return filing.ClaimPage{}, fmt.Errorf("key of the query: %w", err)
	}
	sum := sha256.Sum256(raw)
	key := subject + ":list:" + hex.EncodeToString(sum[:16])
	return read(ctx, c.reader, key, func(ctx context.Context) (filing.ClaimPage, error) {
		return c.source.List(ctx, subject, q)
	})
}

// Get returns the claim when the caller filed it. The key carries the
// caller's subject, so a read from the cache only ever returns a claim the
// same caller already read from the database.
func (c *Claims) Get(ctx context.Context, id string) (domain.ClaimRecord, error) {
	return read(ctx, c.reader, callerScope(ctx)+":claim:"+id, func(ctx context.Context) (domain.ClaimRecord, error) {
		return c.source.Get(ctx, id)
	})
}

// Receipt is not cached.
func (c *Claims) Receipt(ctx context.Context, id string) (domain.ClaimRecord, []byte, error) {
	return c.source.Receipt(ctx, id)
}

// EvidenceLinks is not cached.
func (c *Claims) EvidenceLinks(ctx context.Context, rec domain.ClaimRecord) []filing.EvidenceLink {
	return c.source.EvidenceLinks(ctx, rec)
}

// History serves the history of the status of a claim with the read cache.
type History struct {
	source HistorySource
	reader reader
}

// NewHistory builds the cached history. A zero budget uses DefaultBudget.
func NewHistory(source HistorySource, cache ports.ReadCache, budget time.Duration, clock Clock) *History {
	return &History{source: source, reader: newReader(cache, budget, clock)}
}

// History returns the history of the claim. An insured (owner set) has their
// own key; the advisor and operations, who read any claim, share one.
func (h *History) History(ctx context.Context, claimID, owner string) (review.History, error) {
	scope := "staff"
	if owner != "" {
		scope = "owner:" + owner
	}
	return read(ctx, h.reader, scope+":history:"+claimID, func(ctx context.Context) (review.History, error) {
		return h.source.History(ctx, claimID, owner)
	})
}

// callerScope is the part of the key that keeps the reads of one caller apart
// from the others.
func callerScope(ctx context.Context) string {
	claims, _ := security.ClaimsFromContext(ctx)
	return "owner:" + claims.Subject
}

// read runs load within the budget. When it answers, the result is stored in
// the cache; when it misses the budget or fails with an error that is not a
// business answer, the cached result is served and the request is marked
// stale. Without a cached result the original error is returned.
func read[T any](ctx context.Context, r reader, key string, load func(context.Context) (T, error)) (T, error) {
	loadCtx, cancel := context.WithTimeout(ctx, r.budget)
	v, err := load(loadCtx)
	cancel()
	if err == nil {
		if raw, mErr := json.Marshal(v); mErr == nil {
			r.cache.Put(ctx, key, ports.CachedRead{Data: raw, At: r.clock.Now().UTC()})
		} else {
			slog.WarnContext(ctx, "could not serialize the claim read for the cache", "error", mErr)
		}
		return v, nil
	}
	if answered(err) {
		return v, err
	}
	cached, ok := r.cache.Get(ctx, key)
	if !ok {
		return v, err
	}
	var out T
	if uErr := json.Unmarshal(cached.Data, &out); uErr != nil {
		slog.WarnContext(ctx, "could not read the cached claim read", "error", uErr)
		return v, err
	}
	slog.WarnContext(ctx, "the claim read missed its budget; served from the read cache",
		"error", err, "cached_at", cached.At)
	if f := ports.FreshnessFrom(ctx); f != nil {
		f.MarkStale(cached.At)
	}
	return out, nil
}

// answered tells the errors that are the database's answer, which are never
// replaced by a cached read.
func answered(err error) bool {
	for _, target := range []error{
		ports.ErrClaimNotFound, security.ErrForbidden, security.ErrUnauthenticated,
		review.ErrNotOwner, filing.ErrInvalidQuery,
	} {
		if errors.Is(err, target) {
			return true
		}
	}
	return false
}
