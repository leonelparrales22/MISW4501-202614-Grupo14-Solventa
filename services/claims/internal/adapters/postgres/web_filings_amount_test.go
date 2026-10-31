package postgres_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/ports"
)

// The estimated amount of the web filing in PostgreSQL (HU-W2.2-01, decision
// D12).

func TestTheEstimatedAmountOfAWebClaimIsStored(t *testing.T) {
	s := newTriageStores(t)
	s.file(t, "cl-1", 0, minorCOP(4_800_000))
	s.file(t, "cl-2", 1, nil)

	c, err := s.claims.Get(context.Background(), "cl-1")
	require.NoError(t, err)
	require.NotNil(t, c.EstimatedAmountMinor)
	assert.Equal(t, int64(4_800_000_00), *c.EstimatedAmountMinor)

	c, err = s.claims.Get(context.Background(), "cl-2")
	require.NoError(t, err)
	assert.Nil(t, c.EstimatedAmountMinor)

	for _, amount := range []int64{0, -1, 10_000_000_000_001} {
		_, err = s.db.Exec(`UPDATE claim SET estimated_amount_minor = $1 WHERE id = 'cl-2'`, amount)
		assert.Error(t, err, amount)
	}
}

func TestTheSameKeyWithAnotherAmountIsAnotherFiling(t *testing.T) {
	s := newTriageStores(t)
	first := webClaim("cl-1", "sub-carolina", "form-1")
	first.EstimatedAmountMinor = minorCOP(4_800_000)
	_, created, err := s.claims.CreateWeb(context.Background(), first)
	require.NoError(t, err)
	require.True(t, created)

	retry := webClaim("cl-2", "sub-carolina", "form-1")
	retry.EstimatedAmountMinor = minorCOP(4_800_000)
	stored, created, err := s.claims.CreateWeb(context.Background(), retry)
	require.NoError(t, err)
	assert.False(t, created)
	assert.Equal(t, "cl-1", stored.ID, "the same filing finds its claim")

	other := webClaim("cl-3", "sub-carolina", "form-1")
	other.EstimatedAmountMinor = minorCOP(5_000_000)
	_, _, err = s.claims.CreateWeb(context.Background(), other)
	assert.ErrorIs(t, err, ports.ErrFilingKeyReused)

	without := webClaim("cl-4", "sub-carolina", "form-1")
	_, _, err = s.claims.CreateWeb(context.Background(), without)
	assert.ErrorIs(t, err, ports.ErrFilingKeyReused, "the amount was part of the filing")
}
