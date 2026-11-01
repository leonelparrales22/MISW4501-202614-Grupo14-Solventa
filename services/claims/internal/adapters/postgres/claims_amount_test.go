package postgres_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/adapters/postgres"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/ports"
)

// HU-M2.2-01: el monto estimado de la radicación del móvil queda con el caso,
// en la misma columna que el de la web (HU-W2.2-01).

func TestThePhoneFilingStoresItsEstimatedAmount(t *testing.T) {
	s := postgres.NewClaimRecordStore(newDB(t))
	ctx := context.Background()
	c := claim("cl-1", "sub-carolina", "draft-1")
	monto := int64(480_000_000)
	c.EstimatedAmountMinor = &monto

	stored, created, err := s.Create(ctx, c)

	require.NoError(t, err)
	assert.True(t, created)
	require.NotNil(t, stored.EstimatedAmountMinor)
	assert.EqualValues(t, 480_000_000, *stored.EstimatedAmountMinor)
	read, err := s.Get(ctx, stored.ID)
	require.NoError(t, err)
	require.NotNil(t, read.EstimatedAmountMinor)
	assert.EqualValues(t, 480_000_000, *read.EstimatedAmountMinor)

	again, created, err := s.Create(ctx, c)
	require.NoError(t, err)
	assert.False(t, created, "the same filing with its amount is a retry")
	assert.Equal(t, stored.ID, again.ID)

	otro := int64(500_000_000)
	c.EstimatedAmountMinor = &otro
	_, _, err = s.Create(ctx, c)
	assert.ErrorIs(t, err, ports.ErrFilingKeyReused, "another amount with the same key is another filing")
}

func TestAPhoneFilingWithoutAmountKeepsItEmpty(t *testing.T) {
	s := postgres.NewClaimRecordStore(newDB(t))

	stored, _, err := s.Create(context.Background(), claim("cl-1", "sub-carolina", "draft-1"))

	require.NoError(t, err)
	read, err := s.Get(context.Background(), stored.ID)
	require.NoError(t, err)
	assert.Nil(t, read.EstimatedAmountMinor)
}
