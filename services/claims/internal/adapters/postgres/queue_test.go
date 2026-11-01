package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/ports"
)

// HU-W2.2-03: la cola de los siniestros en evaluación, del más antiguo al más
// nuevo y de todos los asegurados.
func TestTheQueueListsTheClaimsOfEveryInsuredByStatusOldestFirst(t *testing.T) {
	s := newWebStores(t)
	ctx := context.Background()
	ids := []string{
		"8f14e45f-ceea-467a-9f3a-5a2d0b1c7e91",
		"8f14e45f-ceea-467a-9f3a-5a2d0b1c7e92",
		"8f14e45f-ceea-467a-9f3a-5a2d0b1c7e93",
		"8f14e45f-ceea-467a-9f3a-5a2d0b1c7e94",
	}
	for i, id := range ids {
		_, _, err := s.claims.CreateWeb(ctx, webClaim(id, "sub-"+string(rune('a'+i)), "form-"+id))
		require.NoError(t, err)
	}
	// The second filed earliest; the fourth already left evaluation.
	at := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	for i, id := range ids {
		_, err := s.db.ExecContext(ctx, `UPDATE claim SET filed_at = $1 WHERE id = $2`,
			at.Add(time.Duration((i+1)%4)*time.Hour), id)
		require.NoError(t, err)
	}
	_, err := s.db.ExecContext(ctx, `UPDATE claim SET status = 'paid' WHERE id = $1`, ids[3])
	require.NoError(t, err)
	_, err = s.db.ExecContext(ctx, `UPDATE claim SET status = 'in_evaluation' WHERE id = $1`, ids[2])
	require.NoError(t, err)
	statuses := []domain.Status{domain.StatusActive, domain.StatusInEvaluation}

	got, err := s.claims.ListByStatus(ctx, statuses, nil, 10)

	require.NoError(t, err)
	require.Len(t, got, 3)
	assert.Equal(t, []string{ids[0], ids[1], ids[2]}, []string{got[0].ID, got[1].ID, got[2].ID})
	assert.Empty(t, got[0].Supports, "the queue does not load the supports")

	first, err := s.claims.ListByStatus(ctx, statuses, nil, 2)
	require.NoError(t, err)
	require.Len(t, first, 2)
	last := first[1]
	rest, err := s.claims.ListByStatus(ctx, statuses, &ports.ClaimCursor{FiledAt: last.FiledAt, ID: last.ID}, 2)
	require.NoError(t, err)
	require.Len(t, rest, 1)
	assert.Equal(t, ids[2], rest[0].ID)
}

func TestTheQueueWithoutStatusesIsEmpty(t *testing.T) {
	s := newWebStores(t)

	got, err := s.claims.ListByStatus(context.Background(), nil, nil, 10)

	require.NoError(t, err)
	assert.Empty(t, got)
}
