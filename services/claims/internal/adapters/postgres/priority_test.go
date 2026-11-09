package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/adapters/postgres"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/ports"
)

// HU-W2.2-02: the queue by priority over a real PostgreSQL, with the levels
// that the triage recorded.

// assessAll records the first calculation of every claim waiting for one.
func (s triageStores) assessAll(t *testing.T) {
	t.Helper()
	for _, c := range s.pending(t, domain.TriagePolicyVersionV1) {
		s.record(t, c, domain.TriagePolicyVersionV1)
	}
}

func queueIDs(got []ports.PrioritizedClaim) []string {
	out := make([]string, 0, len(got))
	for _, c := range got {
		out = append(out, c.Claim.ID)
	}
	return out
}

func TestTheQueueIsOrderedByLevelThenByTheTimeWaited(t *testing.T) {
	s := newTriageStores(t)
	ctx := context.Background()
	s.file(t, "t2-new", 30, minorCOP(4_800_000)) // water damage, low amount: Triage 2
	s.file(t, "t1", 20, minorCOP(40_000_000))    // high amount: Triage 1
	s.file(t, "t2-old", 10, minorCOP(4_800_000))
	s.file(t, "t3", 0, minorCOP(900_000))
	_, err := s.db.Exec(`UPDATE claim SET incident_type = 'glass' WHERE id = 't3'`) // glass, little money: Triage 3
	require.NoError(t, err)
	s.assessAll(t)
	s.file(t, "none", -60, nil) // filed first, but without a level yet
	q := postgres.PriorityQueueOf(s.claims)

	got, err := q.ListByPriority(ctx, ports.PriorityQuery{Statuses: domain.TriageableStatuses(), Limit: 10})

	require.NoError(t, err)
	assert.Equal(t, []string{"t1", "t2-old", "t2-new", "t3", "none"}, queueIDs(got))
	assert.Equal(t, []int{1, 2, 2, 3, ports.RankUnassessed}, []int{got[0].Rank, got[1].Rank, got[2].Rank, got[3].Rank, got[4].Rank})
	assert.Equal(t, "Calle 80 con Carrera 15, Bogotá", got[0].Claim.Place, "the claim comes whole")

	page, err := q.ListByPriority(ctx, ports.PriorityQuery{Statuses: domain.TriageableStatuses(), Limit: 2,
		After: &ports.PriorityCursor{Rank: got[1].Rank, FiledAt: got[1].Claim.FiledAt, ID: got[1].Claim.ID}})
	require.NoError(t, err)
	assert.Equal(t, []string{"t2-new", "t3"}, queueIDs(page), "the page after the cursor")

	byRank, matching, err := q.CountByRank(ctx, ports.PriorityQuery{Statuses: domain.TriageableStatuses(), Rank: 2})
	require.NoError(t, err)
	assert.Equal(t, map[int]int{1: 1, 2: 2, 3: 1, ports.RankUnassessed: 1}, byRank)
	assert.Equal(t, 2, matching)

	homeOnly, err := q.ListByPriority(ctx, ports.PriorityQuery{Statuses: domain.TriageableStatuses(), Limit: 10,
		IncidentTypes: []domain.IncidentType{domain.IncidentGlass}})
	require.NoError(t, err)
	assert.Equal(t, []string{"t3"}, queueIDs(homeOnly))

	none, err := q.ListByPriority(ctx, ports.PriorityQuery{Statuses: domain.TriageableStatuses(), Limit: 10, Rank: ports.RankUnassessed})
	require.NoError(t, err)
	assert.Equal(t, []string{"none"}, queueIDs(none))
}

func TestANewCalculationMovesTheClaimInTheNextRead(t *testing.T) {
	s := newTriageStores(t)
	ctx := context.Background()
	s.file(t, "first", 0, minorCOP(4_800_000))
	s.file(t, "second", 10, minorCOP(4_800_000))
	s.assessAll(t)
	q := postgres.PriorityQueueOf(s.claims)
	got, err := q.ListByPriority(ctx, ports.PriorityQuery{Statuses: domain.TriageableStatuses(), Limit: 10})
	require.NoError(t, err)
	require.Equal(t, []string{"first", "second"}, queueIDs(got))

	_, err = s.db.Exec(`UPDATE claim SET estimated_amount_minor = $1 WHERE id = 'second'`, *minorCOP(40_000_000))
	require.NoError(t, err)
	s.assessAll(t)
	got, err = q.ListByPriority(ctx, ports.PriorityQuery{Statuses: domain.TriageableStatuses(), Limit: 10})

	require.NoError(t, err)
	assert.Equal(t, []string{"second", "first"}, queueIDs(got), "Triage 1 now goes first")
}

func TestAnEmptyQueueCountsNothing(t *testing.T) {
	s := newTriageStores(t)
	q := postgres.PriorityQueueOf(s.claims)

	got, err := q.ListByPriority(context.Background(), ports.PriorityQuery{Statuses: domain.TriageableStatuses(), Limit: 10})
	require.NoError(t, err)
	assert.Empty(t, got)
	byRank, matching, err := q.CountByRank(context.Background(), ports.PriorityQuery{Statuses: domain.TriageableStatuses()})
	require.NoError(t, err)
	assert.Empty(t, byRank)
	assert.Zero(t, matching)
	nothing, err := q.ListByPriority(context.Background(), ports.PriorityQuery{Limit: 10})
	require.NoError(t, err)
	assert.Nil(t, nothing)
	_, decided, err := q.MeanResolution(context.Background(), filedAt)
	require.NoError(t, err)
	assert.Zero(t, decided)
}

func TestTheMeanResolutionIsOfTheClaimsDecidedSinceThen(t *testing.T) {
	s := newTriageStores(t)
	for i, id := range []string{"quick", "slow", "old"} {
		s.file(t, id, 0, nil)
		decided := filedAt.Add(time.Duration(i+1) * 24 * time.Hour)
		if id == "old" {
			decided = filedAt.Add(-24 * time.Hour)
		}
		_, err := s.db.Exec(`INSERT INTO claim_decision (claim_id, outcome, decided_by, decided_at, rejection_reason, notice_to)
			VALUES ($1, 'rejected', 'analista', $2, 'x', 'c@correo.example')`, id, decided)
		require.NoError(t, err)
	}

	mean, decided, err := postgres.PriorityQueueOf(s.claims).MeanResolution(context.Background(), filedAt)

	require.NoError(t, err)
	assert.Equal(t, 2, decided, "the old decision is out of the window")
	assert.Equal(t, 36*time.Hour, mean, "1 and 2 days")
}
