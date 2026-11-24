package memory_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/idempotency"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/adapters/memory"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
)

// HU-W2.1-02: the last change and the tally in memory, with the rules of
// PostgreSQL.

type decisionsOf map[string]domain.Decision

func (d decisionsOf) Decision(_ context.Context, id string) (domain.Decision, error) {
	if dec, ok := d[id]; ok {
		return dec, nil
	}
	return domain.Decision{}, errors.New("not decided")
}

func fileWith(t *testing.T, cases *memory.CaseStore, n int, subject string) string {
	t.Helper()
	c, _, err := cases.Create(context.Background(), domain.Claim{ID: fmt.Sprintf("cl-%d", n), Subject: subject,
		FilingKey: fmt.Sprintf("k%d", n), PolicyID: "AU-2025-118204", IncidentType: domain.IncidentTheft, Status: domain.StatusActive,
		FiledAt: filedAt.Add(time.Duration(n) * time.Hour), Details: map[string]string{}})
	require.NoError(t, err)
	return c.ID
}

func returnIt(t *testing.T, store *memory.ReviewStore, id string, at time.Time) {
	t.Helper()
	_, err := store.Review(context.Background(), domain.Review{ClaimID: id, Outcome: domain.ReviewReturned,
		ReviewedBy: "asesor", ReviewedAt: at, ReasonCode: domain.ReasonOther, Message: "Falta algo.",
		FactsToCorrect: []domain.Fact{domain.FactPlace}})
	require.NoError(t, err)
}

func TestTheMemoryActivityReadsTheLastChangeAndTheTally(t *testing.T) {
	cases := memory.NewCaseStore()
	reviews := memory.NewReviewStore(cases)
	ctx := context.Background()
	active := fileWith(t, cases, 1, "sub-carolina")
	older := fileWith(t, cases, 2, "sub-carolina")
	newer := fileWith(t, cases, 3, "sub-carolina")
	fileWith(t, cases, 4, "sub-otro")
	returnedAt := filedAt.Add(30 * time.Hour)
	returnIt(t, reviews, older, returnedAt)
	returnIt(t, reviews, newer, returnedAt)
	activity := memory.NewClaimActivity(cases, nil)

	changes, err := activity.LastChanges(ctx, []string{active, older, "no-existe"})
	require.NoError(t, err)
	assert.Equal(t, map[string]time.Time{active: filedAt.Add(time.Hour), older: returnedAt}, changes)

	tally, err := activity.Tally(ctx, "sub-carolina", filedAt)
	require.NoError(t, err)
	assert.Equal(t, map[domain.Status]int{domain.StatusActive: 1, domain.StatusReturned: 2}, tally.ByStatus)
	require.Len(t, tally.PendingAction, 2)
	assert.Equal(t, newer, tally.PendingAction[0].ID, "newest filed first")
	assert.Zero(t, tally.PaidSince, "without decisions nothing is paid")
	assert.Equal(t, []domain.PolicyRef{{ID: "AU-2025-118204"}}, tally.Policies, "filed from the phone: no copy, only the id")
}

func TestTheMemoryTallyAddsUpThePayoutsOfTheYear(t *testing.T) {
	cases := memory.NewCaseStore()
	ctx := context.Background()
	var ids []string
	for n := 1; n <= 3; n++ {
		ids = append(ids, fileWith(t, cases, n, "sub-carolina"))
	}
	year := time.Date(2026, 1, 1, 5, 0, 0, 0, time.UTC)
	paid := func(amount int64, at time.Time, status idempotency.Status) domain.Decision {
		return domain.Decision{Outcome: domain.OutcomeApproved, Payout: &domain.Payout{AmountMinor: amount,
			Currency: "COP", Status: status, ConfirmedAt: at}}
	}
	decisions := decisionsOf{
		ids[0]: paid(18_000_000, year.Add(time.Hour), idempotency.StatusConfirmed),
		ids[1]: paid(99_000_000, year.Add(-time.Hour), idempotency.StatusConfirmed),
	}
	for _, id := range ids {
		require.NoError(t, cases.MoveForTest(id, domain.StatusPaid, year))
	}

	tally, err := memory.NewClaimActivity(cases, decisions).Tally(ctx, "sub-carolina", year)

	require.NoError(t, err)
	assert.Equal(t, 1, tally.PaidSince, "last year's and the undecided do not count")
	assert.Equal(t, int64(18_000_000), tally.PaidAmountMinor)
	assert.Equal(t, "COP", tally.Currency)
}
