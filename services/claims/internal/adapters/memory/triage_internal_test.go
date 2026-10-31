package memory

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/ports"
)

// The calculations of the level kept in memory (HU-W2.2-01), with the same
// rules as PostgreSQL. The tests change the claims directly, as the stories
// that update a claim will.

var triageFiledAt = time.Date(2026, 9, 4, 13, 30, 0, 0, time.UTC)

func triageClaim(id string, minutes int, cop *int64) domain.Claim {
	return domain.Claim{ID: id, Subject: "sub-carolina", FilingKey: id, IncidentType: domain.IncidentCollision,
		Status: domain.StatusActive, FiledAt: triageFiledAt.Add(time.Duration(minutes) * time.Minute), Details: map[string]string{}}
}

func minor(cop int64) *int64 {
	m := cop * 100
	return &m
}

func newTriageStores(t *testing.T, claims ...domain.Claim) (*CaseStore, *TriageStore) {
	t.Helper()
	cases := NewCaseStore()
	for _, c := range claims {
		_, _, err := cases.Create(context.Background(), c)
		require.NoError(t, err)
	}
	return cases, NewTriageStore(cases)
}

func (s *CaseStore) change(id string, f func(*domain.ClaimRecord)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.byID[id]
	f(&c)
	s.byID[id] = c
}

func record(t *testing.T, s *TriageStore, c ports.TriageCandidate) domain.TriageRecord {
	t.Helper()
	p := domain.TriagePolicyV1()
	a, _ := p.Assess(p.InputOf(c.Claim))
	r, ok := domain.NextTriage(c.Claim.ID, a, c.Last, triageFiledAt.Add(time.Hour))
	require.True(t, ok)
	require.NoError(t, s.Record(context.Background(), r))
	return r
}

var statuses = domain.TriageableStatuses()

func TestTheMemoryStorePendsTheClaimsWithoutACalculationOldestFirst(t *testing.T) {
	cases, s := newTriageStores(t, triageClaim("cl-2", 2, nil), triageClaim("cl-1", 1, nil), triageClaim("cl-3", 3, nil))
	cases.change("cl-3", func(c *domain.ClaimRecord) { c.Status = "paid" })
	ctx := context.Background()

	pending, err := s.Pending(ctx, statuses, domain.TriagePolicyVersionV1, 10)
	require.NoError(t, err)
	require.Len(t, pending, 2)
	assert.Equal(t, "cl-1", pending[0].Claim.ID)
	assert.Equal(t, "cl-2", pending[1].Claim.ID)
	assert.Nil(t, pending[0].Last)

	first, err := s.Pending(ctx, statuses, domain.TriagePolicyVersionV1, 1)
	require.NoError(t, err)
	assert.Len(t, first, 1)

	record(t, s, pending[0])
	pending, err = s.Pending(ctx, statuses, domain.TriagePolicyVersionV1, 10)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	assert.Equal(t, "cl-2", pending[0].Claim.ID)
}

func TestTheMemoryStorePendsAClaimWhoseInputChanged(t *testing.T) {
	cases, s := newTriageStores(t, triageClaim("cl-1", 1, nil))
	ctx := context.Background()
	c, err := s.Candidate(ctx, "cl-1")
	require.NoError(t, err)
	record(t, s, c)

	changes := map[string]func(*domain.ClaimRecord){
		"amount appears": func(c *domain.ClaimRecord) { c.EstimatedAmountMinor = minor(4_800_000) },
		"amount changes": func(c *domain.ClaimRecord) { c.EstimatedAmountMinor = minor(40_000_000) },
		"amount goes":    func(c *domain.ClaimRecord) { c.EstimatedAmountMinor = nil },
		"incident type":  func(c *domain.ClaimRecord) { c.IncidentType = domain.IncidentTheft },
	}
	for _, name := range []string{"amount appears", "amount changes", "amount goes", "incident type"} {
		cases.change("cl-1", changes[name])
		pending, err := s.Pending(ctx, statuses, domain.TriagePolicyVersionV1, 10)
		require.NoError(t, err)
		require.Len(t, pending, 1, name)
		record(t, s, pending[0])

		pending, err = s.Pending(ctx, statuses, domain.TriagePolicyVersionV1, 10)
		require.NoError(t, err)
		assert.Empty(t, pending, name)
	}

	pending, err := s.Pending(ctx, statuses, "triage-policy-v2", 10)
	require.NoError(t, err)
	assert.Len(t, pending, 1, "another version of the policy")

	history, err := s.History(ctx, "cl-1")
	require.NoError(t, err)
	require.Len(t, history, 5)
	assert.Equal(t, 5, history[0].Seq, "newest first")
}

func TestTheMemoryStoreRecordsEachPlaceOnce(t *testing.T) {
	_, s := newTriageStores(t, triageClaim("cl-1", 1, nil))
	c, err := s.Candidate(context.Background(), "cl-1")
	require.NoError(t, err)
	r := record(t, s, c)

	assert.ErrorIs(t, s.Record(context.Background(), r), ports.ErrTriageConflict, "the same place")
	r.Seq = 3
	assert.ErrorIs(t, s.Record(context.Background(), r), ports.ErrTriageConflict, "a place that skips one")
}

func TestTheMemoryStoreSharesNothingWithItsCallers(t *testing.T) {
	cases, s := newTriageStores(t, triageClaim("cl-1", 1, nil))
	cases.change("cl-1", func(c *domain.ClaimRecord) { c.EstimatedAmountMinor = minor(4_800_000) })
	c, err := s.Candidate(context.Background(), "cl-1")
	require.NoError(t, err)
	r := record(t, s, c)
	previous := domain.Triage3
	r.PreviousLevel = &previous

	r.Causes[0] = "tampered"
	*r.Assessment.Input.EstimatedAmountMinor = 1
	*c.Claim.EstimatedAmountMinor = 1

	history, err := s.History(context.Background(), "cl-1")
	require.NoError(t, err)
	assert.Equal(t, domain.CauseEnteredEvaluation, history[0].Causes[0])
	assert.Equal(t, int64(4_800_000_00), *history[0].Assessment.Input.EstimatedAmountMinor)
	again, err := s.Candidate(context.Background(), "cl-1")
	require.NoError(t, err)
	assert.Equal(t, int64(4_800_000_00), *again.Claim.EstimatedAmountMinor)

	copied := copyTriage(domain.TriageRecord{PreviousLevel: &previous})
	*copied.PreviousLevel = domain.Triage1
	assert.Equal(t, domain.Triage3, previous)
}

func TestTheMemoryStoreReadsTheCurrentTriageOfAPage(t *testing.T) {
	cases, s := newTriageStores(t, triageClaim("cl-1", 1, nil), triageClaim("cl-2", 2, nil))
	cases.change("cl-2", func(c *domain.ClaimRecord) { c.Status = domain.StatusInEvaluation })
	c, err := s.Candidate(context.Background(), "cl-1")
	require.NoError(t, err)
	record(t, s, c)

	got, err := s.Current(context.Background(), []string{"cl-1", "cl-2", "cl-404"})

	require.NoError(t, err)
	require.Len(t, got, 2)
	require.NotNil(t, got["cl-1"].Last)
	assert.Equal(t, domain.Triage2, got["cl-1"].Last.Assessment.Level)
	assert.Equal(t, domain.StatusInEvaluation, got["cl-2"].Claim.Status)
	assert.Nil(t, got["cl-2"].Last)
	assert.Empty(t, got["cl-2"].Claim.Subject, "only the id and the status")

	_, err = s.Candidate(context.Background(), "cl-404")
	assert.ErrorIs(t, err, ports.ErrClaimNotFound)
}
