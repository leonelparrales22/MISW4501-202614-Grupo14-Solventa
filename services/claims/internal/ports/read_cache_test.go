package ports_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/ports"
)

// The freshness of a read of the claims (HA-L02).

func TestAReadIsFreshUntilMarked(t *testing.T) {
	ctx, f := ports.TrackFreshness(context.Background())

	stale, asOf := f.Stale()
	assert.False(t, stale)
	assert.True(t, asOf.IsZero())
	assert.Same(t, f, ports.FreshnessFrom(ctx))
	assert.Nil(t, ports.FreshnessFrom(context.Background()), "an untracked request has no freshness")
}

func TestTheOldestCachedReadOfARequestWins(t *testing.T) {
	at := time.Date(2026, 11, 8, 19, 30, 0, 0, time.UTC)
	_, f := ports.TrackFreshness(context.Background())

	f.MarkStale(at)
	f.MarkStale(at.Add(time.Minute))
	f.MarkStale(at.Add(-time.Minute))

	stale, asOf := f.Stale()
	assert.True(t, stale)
	assert.Equal(t, at.Add(-time.Minute), asOf)
}
