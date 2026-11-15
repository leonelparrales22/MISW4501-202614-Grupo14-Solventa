package memory_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/adapters/memory"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/ports"
)

// The requests for assistance without a database (HU-M2.3-01): the same
// rules of key and reading as PostgreSQL.

var requestedAt = time.Date(2026, 11, 14, 21, 31, 8, 0, time.UTC)

func assistanceRequest(id, subject, key, fingerprint string) domain.AssistanceRequest {
	accuracy, captured, place := 8.0, requestedAt.Add(-6*time.Second), "Autopista Norte km 12, Bogotá"
	return domain.AssistanceRequest{
		ID: id, Subject: subject, RequesterName: "Carolina Ruiz", Key: key, Fingerprint: fingerprint,
		Policy: domain.AssistancePolicy{ID: "AU-2025-118204", Line: domain.LineAuto, Product: "Autos",
			InsuredObject: "KJT-482"},
		Location: domain.AssistanceLocation{Status: domain.LocationSent,
			Coordinates: &domain.Coordinates{Latitude: 4.7712, Longitude: -74.0452}, AccuracyM: &accuracy,
			CapturedAt: &captured},
		Place: &place, Status: domain.AssistanceNew, RequestedAt: requestedAt,
	}
}

func TestTheMemoryStoreNumbersAndReadsTheRequests(t *testing.T) {
	s := memory.NewAssistanceStore()
	ctx := context.Background()

	first, created, err := s.Create(ctx, assistanceRequest("id-1", "sub-a", "k1", "f1"))
	require.NoError(t, err)
	assert.True(t, created)
	assert.Equal(t, "AST-2026-0001", first.Number)

	second, _, err := s.Create(ctx, assistanceRequest("id-2", "sub-a", "k2", "f2"))
	require.NoError(t, err)
	assert.Equal(t, "AST-2026-0002", second.Number)

	got, err := s.Get(ctx, "id-1")
	require.NoError(t, err)
	assert.Equal(t, first, got)

	_, err = s.Get(ctx, "id-404")
	assert.ErrorIs(t, err, ports.ErrAssistanceRequestNotFound)
}

func TestTheMemoryStoreAnswersARetryAndRejectsAReusedKey(t *testing.T) {
	s := memory.NewAssistanceStore()
	ctx := context.Background()
	first, _, err := s.Create(ctx, assistanceRequest("id-1", "sub-a", "k1", "f1"))
	require.NoError(t, err)

	again, created, err := s.Create(ctx, assistanceRequest("id-other", "sub-a", "k1", "f1"))
	require.NoError(t, err)
	assert.False(t, created)
	assert.Equal(t, first, again)

	_, _, err = s.Create(ctx, assistanceRequest("id-3", "sub-a", "k1", "another"))
	assert.ErrorIs(t, err, ports.ErrAssistanceKeyReused)

	theirs, created, err := s.Create(ctx, assistanceRequest("id-4", "sub-b", "k1", "f1"))
	require.NoError(t, err)
	assert.True(t, created, "the same key of another subject")
	assert.Equal(t, "id-4", theirs.ID)
}

func TestTheMemoryStoreSharesNothingWithItsCallers(t *testing.T) {
	s := memory.NewAssistanceStore()
	ctx := context.Background()
	in := assistanceRequest("id-1", "sub-a", "k1", "f1")
	stored, _, err := s.Create(ctx, in)
	require.NoError(t, err)

	in.Location.Coordinates.Latitude = 0
	*in.Place = "otro"
	stored.Location.Coordinates.Latitude = 1
	*stored.Location.AccuracyM = 99
	*stored.Location.CapturedAt = time.Time{}
	*stored.Place = "otro"

	got, err := s.Get(ctx, "id-1")
	require.NoError(t, err)
	assert.Equal(t, 4.7712, got.Location.Coordinates.Latitude)
	assert.Equal(t, 8.0, *got.Location.AccuracyM)
	assert.Equal(t, requestedAt.Add(-6*time.Second), *got.Location.CapturedAt)
	assert.Equal(t, "Autopista Norte km 12, Bogotá", *got.Place)

	unavailable := assistanceRequest("id-2", "sub-a", "k2", "f2")
	unavailable.Location = domain.AssistanceLocation{Status: domain.LocationUnavailable,
		UnavailableReason: domain.UnavailableNoFix}
	unavailable.Place = nil
	got, _, err = s.Create(ctx, unavailable)
	require.NoError(t, err)
	assert.Nil(t, got.Location.Coordinates)
	assert.Nil(t, got.Place)
}

func TestTheMemoryStoreSettlesARaceOnTheKey(t *testing.T) {
	s := memory.NewAssistanceStore()
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		created int
		ids     = map[string]bool{}
	)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			r, c, err := s.Create(context.Background(), assistanceRequest(fmt.Sprintf("id-%d", i), "sub-a", "k1", "f1"))
			assert.NoError(t, err)
			mu.Lock()
			defer mu.Unlock()
			ids[r.ID] = true
			if c {
				created++
			}
		}(i)
	}
	wg.Wait()
	assert.Equal(t, 1, created)
	assert.Len(t, ids, 1)
}
