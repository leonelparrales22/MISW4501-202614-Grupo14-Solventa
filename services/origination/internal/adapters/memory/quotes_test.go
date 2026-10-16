package memory_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/memory"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
)

func TestCustomerStoreSavesAndFindsByDocument(t *testing.T) {
	s := memory.NewCustomerStore()
	ctx := context.Background()

	_, err := s.GetByDocument(ctx, "1032456789")
	assert.ErrorIs(t, err, ports.ErrCustomerNotFound)

	saved, err := s.Save(ctx, domain.Customer{ID: "c1", DocumentID: "1032456789", FullName: "Ana", Email: "a@x.co"})
	require.NoError(t, err)
	got, err := s.GetByDocument(ctx, "1032456789")
	require.NoError(t, err)
	assert.Equal(t, saved, got)
}

func TestCustomerStoreKeepsTheIDWhenTheDocumentIsSavedAgain(t *testing.T) {
	s := memory.NewCustomerStore()
	ctx := context.Background()
	_, err := s.Save(ctx, domain.Customer{ID: "c1", DocumentID: "1032456789", Email: "old@x.co"})
	require.NoError(t, err)

	updated, err := s.Save(ctx, domain.Customer{ID: "other", DocumentID: "1032456789", Email: "new@x.co"})

	require.NoError(t, err)
	assert.Equal(t, "c1", updated.ID)
	assert.Equal(t, "new@x.co", updated.Email)
}

func TestQuoteStoreCreatesAndGets(t *testing.T) {
	s := memory.NewQuoteStore()
	ctx := context.Background()
	q := domain.Quote{
		ID: "q1", Premium: 90_000, ExpiresAt: time.Now(),
		Coverages: []domain.Coverage{{Code: domain.CoverageDeath, InsuredAmount: 1}},
	}

	_, err := s.Get(ctx, "q1")
	assert.ErrorIs(t, err, ports.ErrQuoteNotFound)

	require.NoError(t, s.Create(ctx, q))
	got, err := s.Get(ctx, "q1")
	require.NoError(t, err)
	assert.Equal(t, q.Premium, got.Premium)

	got.Coverages[0].Code = "mutated"
	again, err := s.Get(ctx, "q1")
	require.NoError(t, err)
	assert.Equal(t, domain.CoverageDeath, again.Coverages[0].Code, "the store hands out copies")
}
