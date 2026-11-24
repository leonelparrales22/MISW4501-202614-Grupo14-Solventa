package postgres_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
)

// HU-W2.1-02: the last change of each claim and the tally of the insured's
// claims, over a real PostgreSQL.

func activityID(n int) string { return fmt.Sprintf("8f14e45f-ceea-467a-9f3a-5a2d0b1c7f%02d", n) }

// fileFor files a web claim of the subject and moves it to the status, as the
// review and the decision would: the trigger records every change.
func (s webStores) fileFor(t *testing.T, n int, subject string, status domain.Status) string {
	t.Helper()
	id := activityID(n)
	c := webClaim(id, subject, fmt.Sprintf("form-%d", n))
	c.FiledAt = filedAt.Add(time.Duration(n) * time.Hour)
	_, _, err := s.claims.CreateWeb(context.Background(), c)
	require.NoError(t, err)
	if status != domain.StatusActive {
		_, err = s.db.Exec(`UPDATE claim SET status = $2 WHERE id = $1`, id, string(status))
		require.NoError(t, err)
	}
	return id
}

// paidAt records the payout of a paid claim.
func (s webStores) paidAt(t *testing.T, id string, amount int64, at time.Time) {
	t.Helper()
	_, err := s.db.Exec(`INSERT INTO claim_decision (claim_id, outcome, decided_by, decided_at, gross_amount_minor,
			currency, payout_key, payout_amount_minor, payout_status, paid_at)
		VALUES ($1, 'approved', 'analista', $2, $3, 'COP', $4, $3, 'confirmed', $2)`, id, at, amount, "pago-"+id)
	require.NoError(t, err)
}

func TestTheLastChangeIsTheOneTheDatabaseRecorded(t *testing.T) {
	s := newWebStores(t)
	ctx := context.Background()
	filed := s.fileFor(t, 1, "sub-carolina", domain.StatusActive)
	moved := s.fileFor(t, 2, "sub-carolina", domain.StatusReturned)

	got, err := s.claims.LastChanges(ctx, []string{filed, moved, activityID(99)})

	require.NoError(t, err)
	require.Len(t, got, 2, "a claim that does not exist has no change")
	assert.True(t, got[filed].Equal(filedAt.Add(time.Hour)), "a claim never moved last changed when it was filed")
	assert.WithinDuration(t, time.Now(), got[moved], time.Minute, "the change of status, when it happened")
	empty, err := s.claims.LastChanges(ctx, nil)
	require.NoError(t, err)
	assert.Empty(t, empty)
}

func TestTheTallyIsOfTheInsuredAndTheirYear(t *testing.T) {
	s := newWebStores(t)
	ctx := context.Background()
	year := time.Date(2026, 1, 1, 5, 0, 0, 0, time.UTC)
	s.fileFor(t, 1, "sub-carolina", domain.StatusActive)
	older := s.fileFor(t, 2, "sub-carolina", domain.StatusReturned)
	newer := s.fileFor(t, 3, "sub-carolina", domain.StatusReturned)
	s.fileFor(t, 4, "sub-carolina", domain.StatusInEvaluation)
	thisYear := s.fileFor(t, 5, "sub-carolina", domain.StatusPaid)
	s.paidAt(t, thisYear, 18_000_000, year.Add(48*time.Hour))
	lastYear := s.fileFor(t, 6, "sub-carolina", domain.StatusPaid)
	s.paidAt(t, lastYear, 99_000_000, year.Add(-time.Hour))
	s.fileFor(t, 7, "sub-carolina", domain.StatusRejected)
	s.fileFor(t, 8, "sub-otro", domain.StatusReturned)

	tally, err := s.claims.Tally(ctx, "sub-carolina", year)

	require.NoError(t, err)
	assert.Equal(t, map[domain.Status]int{domain.StatusActive: 1, domain.StatusReturned: 2,
		domain.StatusInEvaluation: 1, domain.StatusPaid: 2, domain.StatusRejected: 1}, tally.ByStatus)
	assert.Equal(t, 4, tally.InProgress())
	assert.Equal(t, 1, tally.PaidSince, "the payout of last year does not count")
	assert.Equal(t, int64(18_000_000), tally.PaidAmountMinor)
	assert.Equal(t, "COP", tally.Currency)
	require.Len(t, tally.PendingAction, 2)
	assert.Equal(t, newer, tally.PendingAction[0].ID, "newest filed first")
	assert.Equal(t, older, tally.PendingAction[1].ID)
	assert.NotEmpty(t, tally.PendingAction[0].FilingNumber)
	assert.Equal(t, []domain.PolicyRef{{ID: "AU-2025-118204", Line: domain.LineAuto, Product: "Autos",
		InsuredObject: "KJT-482"}}, tally.Policies, "every claim of Carolina is on one policy, listed once")

	none, err := s.claims.Tally(ctx, "sub-nadie", year)
	require.NoError(t, err)
	assert.Empty(t, none.ByStatus)
	assert.Zero(t, none.PaidAmountMinor)
	assert.Empty(t, none.Currency)
	assert.Empty(t, none.PendingAction)
	assert.Empty(t, none.Policies)
}

func TestThePoliciesOfTheTallyIncludeTheOnesWithoutACopy(t *testing.T) {
	s := newWebStores(t)
	ctx := context.Background()
	s.fileFor(t, 1, "sub-carolina", domain.StatusActive)
	_, _, err := s.claims.Create(ctx, domain.Claim{ID: activityID(2), Subject: "sub-carolina", FilingKey: "movil-1",
		PolicyID: "AU-2026-000001", IncidentType: domain.IncidentCollision, OccurredAt: filedAt.Add(-time.Hour),
		Place: "Calle 80", Description: "Choque por alcance en el semáforo.", Details: map[string]string{},
		Status: domain.StatusActive, FiledAt: filedAt})
	require.NoError(t, err)

	tally, err := s.claims.Tally(ctx, "sub-carolina", filedAt)

	require.NoError(t, err)
	assert.Equal(t, []domain.PolicyRef{
		{ID: "AU-2025-118204", Line: domain.LineAuto, Product: "Autos", InsuredObject: "KJT-482"},
		{ID: "AU-2026-000001"},
	}, tally.Policies, "a filing of the phone has no copy of its policy, but it is still filtered by it")
}
