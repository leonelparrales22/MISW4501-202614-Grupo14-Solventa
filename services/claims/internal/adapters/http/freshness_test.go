package httpadapter_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	httpadapter "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/adapters/http"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/filing"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/ports"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/review"
)

// HA-L02: every read of the insured's claims says whether it came from the
// read cache and when it was read.

var cachedAt = time.Date(2026, 11, 8, 19, 30, 0, 0, time.UTC)

// staleReads answers from "the cache": it marks the request stale.
type staleReads struct{ fakeReads }

func (s *staleReads) List(ctx context.Context, subject string, q filing.ClaimQuery) (filing.ClaimPage, error) {
	ports.FreshnessFrom(ctx).MarkStale(cachedAt)
	return s.fakeReads.List(ctx, subject, q)
}

func (s *staleReads) Get(ctx context.Context, id string) (domain.ClaimRecord, error) {
	ports.FreshnessFrom(ctx).MarkStale(cachedAt)
	return s.fakeReads.Get(ctx, id)
}

type staleReviewer struct{ fakeReviewer }

func (s *staleReviewer) History(ctx context.Context, claimID, owner string) (review.History, error) {
	ports.FreshnessFrom(ctx).MarkStale(cachedAt)
	return s.fakeReviewer.History(ctx, claimID, owner)
}

func TestAReadFromTheDatabaseIsFresh(t *testing.T) {
	reads := &fakeReads{claim: webClaim(), page: filing.ClaimPage{Claims: []domain.ClaimRecord{webClaim()}}}
	before := time.Now().UTC().Add(-time.Second)
	for _, path := range []string{"/v1/claims", "/v1/claims/" + claimID} {
		rec := do(webRouter(&fakeWeb{}, &fakeUploads{}, reads), http.MethodGet, path, "insured", nil, nil)

		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		assert.Equal(t, "false", rec.Header().Get("X-Stale"), path)
		assert.Equal(t, "database", rec.Header().Get("X-Data-Source"), path)
		body := decode(t, rec)
		assert.Equal(t, false, body["stale"], path)
		asOf, err := time.Parse(time.RFC3339Nano, body["as_of"].(string))
		require.NoError(t, err)
		assert.True(t, asOf.After(before), path)
	}
}

func TestAReadFromTheCacheIsMarkedStaleWithItsTime(t *testing.T) {
	reads := &staleReads{fakeReads{claim: webClaim(), page: filing.ClaimPage{Claims: []domain.ClaimRecord{webClaim()}}}}
	for _, path := range []string{"/v1/claims", "/v1/claims/" + claimID} {
		rec := do(webRouterOf(reads), http.MethodGet, path, "insured", nil, nil)

		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		assert.Equal(t, "true", rec.Header().Get("X-Stale"), path)
		assert.Equal(t, "cache", rec.Header().Get("X-Data-Source"), path)
		assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"), path)
		body := decode(t, rec)
		assert.Equal(t, true, body["stale"], path)
		assert.Equal(t, cachedAt.Format(time.RFC3339), body["as_of"], path)
	}
}

func TestTheHistoryCarriesItsFreshness(t *testing.T) {
	history := review.History{Claim: domain.ClaimRecord{Claim: domain.Claim{ID: claimID, FilingNumber: "SIN-2026-01655",
		Status: domain.StatusActive, FiledAt: cachedAt.Add(-time.Hour)}}}

	rec := do(reviewRouter(&fakeReviewer{history: history}), http.MethodGet, "/v1/claims/"+claimID+"/history", "insured", nil, nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, false, decode(t, rec)["stale"])

	stale := &staleReviewer{fakeReviewer{history: history}}
	rec = do(reviewRouterOf(stale), http.MethodGet, "/v1/claims/"+claimID+"/history", "insured", nil, nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	body := decode(t, rec)
	assert.Equal(t, true, body["stale"])
	assert.Equal(t, cachedAt.Format(time.RFC3339), body["as_of"])
	assert.Equal(t, "cache", rec.Header().Get("X-Data-Source"))
}

func webRouterOf(reads httpadapter.ClaimReader) http.Handler {
	return httpadapter.NewRouter(httpadapter.WithClaimReads(reads, callers))
}

func reviewRouterOf(r httpadapter.Reviewer) http.Handler {
	return httpadapter.NewRouter(httpadapter.WithReviews(r, reviewCallers))
}
