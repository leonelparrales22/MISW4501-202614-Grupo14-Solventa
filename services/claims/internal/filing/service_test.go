package filing_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/adapters/memory"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/filing"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/ports"
)

// The use case of the filing (HU-M2.2-01).

var filedAt = time.Date(2026, 10, 23, 15, 0, 0, 0, time.UTC)

type fixedClock struct{ t time.Time }

func (c fixedClock) Now() time.Time { return c.t }

func service() (*memory.ClaimStore, *filing.Service) {
	claims := memory.NewClaimStore()
	return claims, filing.NewService(claims, fixedClock{t: filedAt})
}

func request() domain.FilingRequest {
	return domain.FilingRequest{
		PolicyID:            "AU-2025-118204",
		IncidentType:        "collision",
		OccurredAt:          "2026-10-20T23:30:00Z",
		Place:               "Calle 80 con Carrera 15, Bogotá",
		Description:         "Choque por alcance en el semáforo.",
		DeclarationAccepted: true,
	}
}

func TestFilingCreatesTheCaseWithItsNumber(t *testing.T) {
	_, svc := service()

	c, created, err := svc.File(context.Background(), "sub-carolina", "draft-1", request())

	require.NoError(t, err)
	assert.True(t, created)
	assert.NotEmpty(t, c.ID)
	assert.Equal(t, "SIN-2026-00001", c.FilingNumber)
	assert.Equal(t, domain.StatusActive, c.Status)
	assert.Equal(t, filedAt, c.FiledAt)
	assert.Equal(t, "sub-carolina", c.Subject, "the case belongs to whoever filed it")
}

// The filing identifies its subject by the token, never by what the body says.
func TestTheCaseIsFiledForTheSubjectOfTheSession(t *testing.T) {
	_, svc := service()

	first, _, err := svc.File(context.Background(), "sub-ana", "draft-1", request())
	require.NoError(t, err)
	second, _, err := svc.File(context.Background(), "sub-luis", "draft-1", request())
	require.NoError(t, err)

	assert.NotEqual(t, first.ID, second.ID, "the same draft key of two people is two cases")
	assert.NotEqual(t, first.FilingNumber, second.FilingNumber)
}

// A retry after a lost response must not file the claim twice.
func TestTheSameKeyReturnsTheCaseThatAlreadyExists(t *testing.T) {
	_, svc := service()
	first, created, err := svc.File(context.Background(), "sub-carolina", "draft-1", request())
	require.NoError(t, err)
	require.True(t, created)

	second, created, err := svc.File(context.Background(), "sub-carolina", "draft-1", request())

	require.NoError(t, err)
	assert.False(t, created)
	assert.Equal(t, first.ID, second.ID)
	assert.Equal(t, first.FilingNumber, second.FilingNumber)
	assert.Equal(t, first.FiledAt, second.FiledAt, "it keeps the instant of the original filing")
}

// The same key with another filing is a mistake of the client, not a retry.
func TestTheSameKeyWithAnotherFilingIsRefused(t *testing.T) {
	_, svc := service()
	_, _, err := svc.File(context.Background(), "sub-carolina", "draft-1", request())
	require.NoError(t, err)

	other := request()
	other.IncidentType = "theft"
	_, _, err = svc.File(context.Background(), "sub-carolina", "draft-1", other)

	assert.ErrorIs(t, err, ports.ErrFilingKeyReused)
}

func TestAnInvalidFilingStoresNothing(t *testing.T) {
	_, svc := service()
	r := request()
	r.DeclarationAccepted = false

	_, created, err := svc.File(context.Background(), "sub-carolina", "draft-1", r)

	var invalid *domain.ValidationError
	assert.ErrorAs(t, err, &invalid)
	assert.False(t, created)

	// The same key is still free: the filing never happened.
	c, created, err := svc.File(context.Background(), "sub-carolina", "draft-1", request())
	require.NoError(t, err)
	assert.True(t, created)
	assert.Equal(t, "SIN-2026-00001", c.FilingNumber, "no number was burned by the invalid filing")
}

func TestEachFilingTakesItsOwnNumber(t *testing.T) {
	_, svc := service()

	first, _, err := svc.File(context.Background(), "sub-carolina", "draft-1", request())
	require.NoError(t, err)
	second, _, err := svc.File(context.Background(), "sub-carolina", "draft-2", request())
	require.NoError(t, err)

	assert.Equal(t, "SIN-2026-00001", first.FilingNumber)
	assert.Equal(t, "SIN-2026-00002", second.FilingNumber)
}
