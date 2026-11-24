package readcache_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/security"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/filing"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/ports"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/readcache"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/review"
)

// The read cache of the insured's claim queries (HA-L02).

const budget = 30 * time.Millisecond

var readAt = time.Date(2026, 11, 8, 19, 30, 0, 0, time.UTC)

type fixedClock struct{ t time.Time }

func (c *fixedClock) Now() time.Time { return c.t }

// memCache is the cache of the tests.
type memCache struct {
	mu   sync.Mutex
	data map[string]ports.CachedRead
	gets int
}

func newCache() *memCache { return &memCache{data: map[string]ports.CachedRead{}} }

func (c *memCache) Get(_ context.Context, key string) (ports.CachedRead, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.gets++
	r, ok := c.data[key]
	return r, ok
}

func (c *memCache) Put(_ context.Context, key string, r ports.CachedRead) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.data[key] = r
}

// source is the database of the tests: it answers the claim, waits for the
// budget to run out (slow) or fails.
type source struct {
	claim domain.ClaimRecord
	page  filing.ClaimPage
	hist  review.History
	slow  bool
	err   error
	links int
}

func (s *source) answer(ctx context.Context) error {
	if s.slow {
		<-ctx.Done()
		return ctx.Err()
	}
	return s.err
}

func (s *source) List(ctx context.Context, _ string, _ filing.ClaimQuery) (filing.ClaimPage, error) {
	if err := s.answer(ctx); err != nil {
		return filing.ClaimPage{}, err
	}
	return s.page, nil
}

func (s *source) Get(ctx context.Context, _ string) (domain.ClaimRecord, error) {
	if err := s.answer(ctx); err != nil {
		return domain.ClaimRecord{}, err
	}
	return s.claim, nil
}

func (s *source) Receipt(context.Context, string) (domain.ClaimRecord, []byte, error) {
	return s.claim, []byte("%PDF"), nil
}

func (s *source) EvidenceLinks(context.Context, domain.ClaimRecord) []filing.EvidenceLink { return nil }

func (s *source) SupportLink(_ context.Context, _, supportID string) (filing.SupportLink, error) {
	s.links++
	return filing.SupportLink{URL: "https://bucket/" + supportID}, nil
}

func (s *source) History(ctx context.Context, _, _ string) (review.History, error) {
	if err := s.answer(ctx); err != nil {
		return review.History{}, err
	}
	return s.hist, nil
}

func claim(status domain.Status) domain.ClaimRecord {
	amount := int64(250_000_00)
	policy := domain.Policy{ID: "HG-2025-880231", Line: domain.LineHome, Product: "Hogar", InsuredObject: "Carrera 15 # 93-40"}
	return domain.ClaimRecord{
		Claim: domain.Claim{ID: "c-1", FilingNumber: "SIN-2026-01655", Subject: "sub-ana", PolicyID: policy.ID,
			IncidentType: domain.IncidentOther, OccurredAt: readAt.Add(-72 * time.Hour), Place: "Carrera 15",
			Details: map[string]string{"room": "kitchen"}, Status: status, FiledAt: readAt.Add(-48 * time.Hour),
			Evidence:             []domain.Evidence{{ObjectKey: "evidence/sub-ana/x.jpg", ByteSize: 10}},
			EstimatedAmountMinor: &amount},
		Channel: domain.ChannelMobile, Policy: &policy,
	}
}

func as(subject string) context.Context {
	return security.WithClaims(context.Background(), security.Claims{Subject: subject, Roles: []security.Role{security.RoleInsured}})
}

func tracked(ctx context.Context) (context.Context, *ports.Freshness) {
	return ports.TrackFreshness(ctx)
}

func TestAnAnsweredReadIsFreshAndKeptInTheCache(t *testing.T) {
	cache, src := newCache(), &source{claim: claim(domain.StatusActive)}
	claims := readcache.NewClaims(src, cache, budget, &fixedClock{readAt})
	ctx, fresh := tracked(as("sub-ana"))

	got, err := claims.Get(ctx, "c-1")

	require.NoError(t, err)
	assert.Equal(t, src.claim, got)
	stale, _ := fresh.Stale()
	assert.False(t, stale)
	require.Len(t, cache.data, 1)
	for _, r := range cache.data {
		assert.Equal(t, readAt, r.At)
	}
}

func TestASlowDatabaseServesTheLastReadMarkedStale(t *testing.T) {
	cache, src := newCache(), &source{claim: claim(domain.StatusActive)}
	clock := &fixedClock{readAt}
	claims := readcache.NewClaims(src, cache, budget, clock)
	_, err := claims.Get(as("sub-ana"), "c-1")
	require.NoError(t, err)

	src.slow, clock.t = true, readAt.Add(3*time.Minute)
	ctx, fresh := tracked(as("sub-ana"))
	start := time.Now()
	got, err := claims.Get(ctx, "c-1")

	require.NoError(t, err)
	assert.Less(t, time.Since(start), 10*budget, "the read stops waiting at the budget")
	assert.Equal(t, claim(domain.StatusActive), got, "the cached claim survives the round trip")
	stale, asOf := fresh.Stale()
	assert.True(t, stale)
	assert.Equal(t, readAt, asOf, "the time of the read, not of the request")
}

func TestADownDatabaseServesTheCacheToo(t *testing.T) {
	cache, src := newCache(), &source{page: filing.ClaimPage{Claims: []domain.ClaimRecord{claim(domain.StatusActive)}}}
	claims := readcache.NewClaims(src, cache, budget, &fixedClock{readAt})
	q := filing.ClaimQuery{Status: "active"}
	_, err := claims.List(as("sub-ana"), "sub-ana", q)
	require.NoError(t, err)

	src.err = errors.New("dial tcp: connection refused")
	ctx, fresh := tracked(as("sub-ana"))
	page, err := claims.List(ctx, "sub-ana", q)

	require.NoError(t, err)
	require.Len(t, page.Claims, 1)
	stale, _ := fresh.Stale()
	assert.True(t, stale)
}

func TestWithoutACachedReadTheFailureStands(t *testing.T) {
	src := &source{slow: true}
	claims := readcache.NewClaims(src, newCache(), budget, &fixedClock{readAt})
	ctx, fresh := tracked(as("sub-ana"))

	_, err := claims.Get(ctx, "c-1")

	require.ErrorIs(t, err, context.DeadlineExceeded)
	stale, _ := fresh.Stale()
	assert.False(t, stale)
}

func TestTheAnswersOfTheDatabaseAreNotDegraded(t *testing.T) {
	for _, answer := range []error{ports.ErrClaimNotFound, security.ErrForbidden, filing.ErrInvalidQuery} {
		cache, src := newCache(), &source{claim: claim(domain.StatusActive)}
		claims := readcache.NewClaims(src, cache, budget, &fixedClock{readAt})
		_, err := claims.Get(as("sub-ana"), "c-1")
		require.NoError(t, err)

		src.err = answer
		_, err = claims.Get(as("sub-ana"), "c-1")

		assert.ErrorIs(t, err, answer)
		assert.Zero(t, cache.gets, "the cache is not even read")
	}
}

func TestOneInsuredNeverReadsTheCacheOfAnother(t *testing.T) {
	cache, src := newCache(), &source{claim: claim(domain.StatusActive)}
	claims := readcache.NewClaims(src, cache, budget, &fixedClock{readAt})
	_, err := claims.Get(as("sub-ana"), "c-1")
	require.NoError(t, err)

	src.err = errors.New("timeout")
	_, err = claims.Get(as("sub-intruso"), "c-1")

	assert.Error(t, err, "the claim is cached under its owner only")
}

func TestTheListIsCachedPerQuery(t *testing.T) {
	cache, src := newCache(), &source{page: filing.ClaimPage{Claims: []domain.ClaimRecord{claim(domain.StatusActive)}}}
	claims := readcache.NewClaims(src, cache, budget, &fixedClock{readAt})
	_, err := claims.List(as("sub-ana"), "sub-ana", filing.ClaimQuery{Status: "active"})
	require.NoError(t, err)

	src.err = errors.New("timeout")
	_, err = claims.List(as("sub-ana"), "sub-ana", filing.ClaimQuery{Status: "closed"})

	assert.Error(t, err, "another filter is another read")
}

func TestTheReceiptAndTheLinksAreNotCached(t *testing.T) {
	cache, src := newCache(), &source{claim: claim(domain.StatusActive)}
	claims := readcache.NewClaims(src, cache, budget, &fixedClock{readAt})

	_, pdf, err := claims.Receipt(as("sub-ana"), "c-1")
	require.NoError(t, err)
	assert.Equal(t, []byte("%PDF"), pdf)
	assert.Nil(t, claims.EvidenceLinks(as("sub-ana"), src.claim))
	assert.Empty(t, cache.data)
}

func TestTheHistoryFallsBackPerOwner(t *testing.T) {
	cache := newCache()
	src := &source{hist: review.History{Claim: claim(domain.StatusActive), Changes: []domain.StatusChange{
		{ID: 1, ClaimID: "c-1", Status: domain.StatusActive, ChangedAt: readAt.Add(-time.Hour)}}}}
	history := readcache.NewHistory(src, cache, budget, &fixedClock{readAt})
	_, err := history.History(context.Background(), "c-1", "sub-ana")
	require.NoError(t, err)

	src.slow = true
	ctx, fresh := tracked(context.Background())
	got, err := history.History(ctx, "c-1", "sub-ana")
	require.NoError(t, err)
	assert.Equal(t, src.hist, got)
	stale, _ := fresh.Stale()
	assert.True(t, stale)

	_, err = history.History(context.Background(), "c-1", "sub-intruso")
	assert.Error(t, err, "another insured has no cached history")
	_, err = history.History(context.Background(), "c-1", "")
	assert.Error(t, err, "the staff key is apart from the insured's")
}

func TestAZeroBudgetIsTheDefault(t *testing.T) {
	assert.Equal(t, 900*time.Millisecond, readcache.DefaultBudget)
	claims := readcache.NewClaims(&source{claim: claim(domain.StatusActive)}, newCache(), 0, &fixedClock{readAt})
	_, err := claims.Get(as("sub-ana"), "c-1")
	assert.NoError(t, err)
}

func TestTheDownloadOfASupportIsNeverCached(t *testing.T) {
	src := &source{}
	c := readcache.NewClaims(src, newCache(), budget, &fixedClock{readAt})

	for i := 0; i < 2; i++ {
		link, err := c.SupportLink(context.Background(), "cl-1", "sup-1")
		require.NoError(t, err)
		assert.Equal(t, "https://bucket/sup-1", link.URL)
	}

	assert.Equal(t, 2, src.links, "a signed link is short-lived: every read signs again")
}
