package postgres_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/adapters/postgres"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/ports"
)

// HU-W2.1-04: the resubmission of a returned filing over a real PostgreSQL.

const resubmittedClaim = "8f14e45f-ceea-467a-9f3a-5a2d0b1c7e96"

type resubmissionStores struct {
	webStores
	reviews       *postgres.ReviewStore
	resubmissions *postgres.ResubmissionStore
	photos        []domain.SupportFile
}

// newReturnedClaim files a web collision with two photographs and has the
// advisor return it.
func newReturnedClaim(t *testing.T) resubmissionStores {
	t.Helper()
	s := newWebStores(t)
	ctx := context.Background()
	photos := s.upload(t, "sub-carolina", 2)
	_, _, err := s.claims.CreateWeb(ctx, webClaim(resubmittedClaim, "sub-carolina", "form-reenvio", photos...))
	require.NoError(t, err)
	reviews := postgres.ReviewStoreOf(s.claims)
	_, err = reviews.Review(ctx, domain.Review{ClaimID: resubmittedClaim, Outcome: domain.ReviewReturned,
		ReviewedBy: "juan@solventa.example", ReviewerName: "Juan Ríos", ReviewedAt: reviewedAt,
		ReasonCode: domain.ReasonIllegibleOrIncompleteSupports, Message: "Una foto está borrosa.",
		Supports: []domain.SupportAssessment{{RequirementCode: domain.SupportVehiclePhotos, Label: "Fotos del vehículo",
			Verdict: domain.VerdictIllegible}}})
	require.NoError(t, err)
	return resubmissionStores{webStores: s, reviews: reviews, resubmissions: postgres.ResubmissionStoreOf(s.claims),
		photos: photos}
}

// replacing is the resubmission that replaces the first photograph with new.
func (s resubmissionStores) replacing(t *testing.T, newPhoto domain.SupportFile) domain.Resubmission {
	t.Helper()
	c, err := s.claims.Get(context.Background(), resubmittedClaim)
	require.NoError(t, err)
	c.Place, c.Status = "Calle 80 con carrera 30, frente al centro comercial, Bogotá", domain.StatusActive
	added := domain.AttachedSupport{RequirementCode: domain.SupportVehiclePhotos, Position: 2, File: newPhoto}
	c.Supports = []domain.AttachedSupport{c.Supports[1], added}
	return domain.Resubmission{ClaimID: resubmittedClaim, ReviewSeq: 1, ResubmittedBy: "sub-carolina",
		ResubmittedAt: reviewedAt.Add(24 * time.Hour),
		Facts:         []domain.FactChange{{Fact: domain.FactPlace, Before: "Calle 80", After: c.Place}},
		Supports: []domain.SupportChange{{RequirementCode: domain.SupportVehiclePhotos, Label: "Fotos del vehículo",
			Added: []string{newPhoto.ID}, Withdrawn: []string{s.photos[0].ID}}},
		Claim: c, Added: []domain.AttachedSupport{added}}
}

func TestAResubmissionCorrectsTheClaimAndReplacesItsSupports(t *testing.T) {
	s := newReturnedClaim(t)
	ctx := context.Background()
	newPhoto := s.upload(t, "sub-carolina", 1)[0]

	r, err := s.resubmissions.Resubmit(ctx, s.replacing(t, newPhoto))

	require.NoError(t, err)
	assert.Equal(t, 1, r.Seq)
	c, err := s.claims.Get(ctx, resubmittedClaim)
	require.NoError(t, err)
	assert.Equal(t, domain.StatusActive, c.Status)
	assert.Equal(t, "Calle 80 con carrera 30, frente al centro comercial, Bogotá", c.Place)
	require.Len(t, c.Supports, 2, "the withdrawn photograph is not shown")
	assert.Equal(t, s.photos[1].ID, c.Supports[0].File.ID)
	assert.Equal(t, newPhoto.ID, c.Supports[1].File.ID)

	got, err := s.resubmissions.Resubmissions(ctx, resubmittedClaim)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, r.Facts, got[0].Facts)
	assert.Equal(t, r.Supports, got[0].Supports)
	assert.Equal(t, reviewedAt.Add(24*time.Hour), got[0].ResubmittedAt)
	assert.Equal(t, "sub-carolina", got[0].ResubmittedBy)
	assert.Equal(t, 1, got[0].ReviewSeq)
	assert.NotEmpty(t, got[0].FilingNumber)

	pending, err := s.supports.CountPending(ctx, "sub-carolina")
	require.NoError(t, err)
	assert.Zero(t, pending, "the new photograph is attached, the withdrawn one is not pending")
	history, err := s.reviews.History(ctx, resubmittedClaim)
	require.NoError(t, err)
	assert.Equal(t, domain.StatusActive, history[len(history)-1].Status, "the database records the change")
	assert.Equal(t, domain.StatusReturned, history[len(history)-1].PreviousStatus)
}

func TestTwoResubmissionsAtOnceRecordOne(t *testing.T) {
	s := newReturnedClaim(t)
	photos := s.upload(t, "sub-carolina", 2)
	attempts := []domain.Resubmission{s.replacing(t, photos[0]), s.replacing(t, photos[1])}
	results := make([]error, len(attempts))
	var wg sync.WaitGroup
	for i := range attempts {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, results[i] = s.resubmissions.Resubmit(context.Background(), attempts[i])
		}(i)
	}
	wg.Wait()

	var ok, refused int
	for _, err := range results {
		switch {
		case err == nil:
			ok++
		case assert.ErrorIs(t, err, domain.ErrNotReturned):
			refused++
		}
	}
	assert.Equal(t, 1, ok)
	assert.Equal(t, 1, refused)
	got, err := s.resubmissions.Resubmissions(context.Background(), resubmittedClaim)
	require.NoError(t, err)
	assert.Len(t, got, 1)
}

func TestOnlyAClaimReturnedByThatReviewIsResubmitted(t *testing.T) {
	s := newReturnedClaim(t)
	ctx := context.Background()
	r := s.replacing(t, s.upload(t, "sub-carolina", 1)[0])

	stale := r
	stale.ReviewSeq = 2
	_, err := s.resubmissions.Resubmit(ctx, stale)
	assert.ErrorIs(t, err, domain.ErrNotReturned, "it answers a return that is not the last one")

	missing := r
	missing.ClaimID = "8f14e45f-ceea-467a-9f3a-5a2d0b1c7e00"
	_, err = s.resubmissions.Resubmit(ctx, missing)
	assert.ErrorIs(t, err, ports.ErrClaimNotFound)

	_, err = s.resubmissions.Resubmit(ctx, r)
	require.NoError(t, err)
	_, err = s.resubmissions.Resubmit(ctx, r)
	assert.ErrorIs(t, err, domain.ErrNotReturned, "it was sent already")
}

func TestASupportOfAnotherClaimUndoesTheResubmission(t *testing.T) {
	s := newReturnedClaim(t)
	ctx := context.Background()
	taken := s.upload(t, "sub-carolina", 1)[0]
	_, _, err := s.claims.CreateWeb(ctx, webClaim("8f14e45f-ceea-467a-9f3a-5a2d0b1c7e97", "sub-carolina", "form-otro", taken))
	require.NoError(t, err)

	_, err = s.resubmissions.Resubmit(ctx, s.replacing(t, taken))

	assert.ErrorIs(t, err, ports.ErrSupportAlreadyAttached)
	c, err := s.claims.Get(ctx, resubmittedClaim)
	require.NoError(t, err)
	assert.Equal(t, domain.StatusReturned, c.Status, "nothing changed")
	assert.Len(t, c.Supports, 2)
	got, err := s.resubmissions.Resubmissions(ctx, resubmittedClaim)
	require.NoError(t, err)
	assert.Empty(t, got)
}
