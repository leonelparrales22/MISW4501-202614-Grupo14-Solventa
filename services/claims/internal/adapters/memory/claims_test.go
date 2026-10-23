package memory_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/adapters/memory"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/ports"
)

// The in-memory store runs when the service has no database, so it has to
// behave like the PostgreSQL one: same numbering and same answer to a
// repeated filing key.

var filedAt = time.Date(2026, 10, 23, 15, 0, 0, 0, time.UTC)

func claim(id, subject, key string) domain.Claim {
	return domain.Claim{
		ID:           id,
		Subject:      subject,
		FilingKey:    key,
		PolicyID:     "AU-2025-118204",
		IncidentType: domain.IncidentCollision,
		OccurredAt:   time.Date(2026, 10, 20, 23, 30, 0, 0, time.UTC),
		Place:        "Calle 80 con Carrera 15, Bogotá",
		Description:  "Choque por alcance en el semáforo.",
		Status:       domain.StatusActive,
		FiledAt:      filedAt,
	}
}

func TestEachFilingTakesItsOwnNumber(t *testing.T) {
	s := memory.NewClaimStore()

	first, created, err := s.Create(context.Background(), claim("cl-1", "sub-carolina", "draft-1"))
	require.NoError(t, err)
	assert.True(t, created)
	second, _, err := s.Create(context.Background(), claim("cl-2", "sub-carolina", "draft-2"))
	require.NoError(t, err)

	assert.Equal(t, "SIN-2026-00001", first.FilingNumber)
	assert.Equal(t, "SIN-2026-00002", second.FilingNumber)
}

func TestTheSameKeyReturnsTheCaseThatAlreadyExists(t *testing.T) {
	s := memory.NewClaimStore()
	first, _, err := s.Create(context.Background(), claim("cl-1", "sub-carolina", "draft-1"))
	require.NoError(t, err)

	second, created, err := s.Create(context.Background(), claim("cl-2", "sub-carolina", "draft-1"))

	require.NoError(t, err)
	assert.False(t, created)
	assert.Equal(t, first.ID, second.ID)
	assert.Equal(t, first.FilingNumber, second.FilingNumber)
}

func TestTheSameKeyWithAnotherFilingIsRefused(t *testing.T) {
	s := memory.NewClaimStore()
	_, _, err := s.Create(context.Background(), claim("cl-1", "sub-carolina", "draft-1"))
	require.NoError(t, err)

	other := claim("cl-2", "sub-carolina", "draft-1")
	other.Place = "Autopista Sur, Bogotá"

	_, _, err = s.Create(context.Background(), other)

	assert.ErrorIs(t, err, ports.ErrFilingKeyReused)
}

func TestTheKeyIsPerInsured(t *testing.T) {
	s := memory.NewClaimStore()

	_, _, err := s.Create(context.Background(), claim("cl-1", "sub-ana", "draft-1"))
	require.NoError(t, err)
	_, created, err := s.Create(context.Background(), claim("cl-2", "sub-luis", "draft-1"))

	require.NoError(t, err)
	assert.True(t, created)
}
