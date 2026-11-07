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

// The reviews of the advisor in memory (HU-W2.1-03), with the rules of
// PostgreSQL: a review moves the claim and every change is in the history.

func filedForReview(t *testing.T) (*memory.CaseStore, string) {
	t.Helper()
	cases := memory.NewCaseStore()
	c, _, err := cases.Create(context.Background(), domain.Claim{ID: "cl-1", Subject: "sub-carolina", FilingKey: "k",
		IncidentType: domain.IncidentTheft, Status: domain.StatusActive, FiledAt: filedAt, Details: map[string]string{}})
	require.NoError(t, err)
	return cases, c.ID
}

func TestTheMemoryStoreRecordsAReviewAndMovesTheClaim(t *testing.T) {
	cases, id := filedForReview(t)
	store := memory.NewReviewStore(cases)
	at := filedAt.Add(time.Hour)
	r := domain.Review{ClaimID: id, Outcome: domain.ReviewReturned, ReviewedBy: "asesor", ReviewedAt: at,
		Reason: "Falta la denuncia.", MissingSupports: []string{domain.SupportPoliceReport}}

	got, err := store.Review(context.Background(), r)

	require.NoError(t, err)
	assert.Equal(t, 1, got.Seq)
	assert.NotEmpty(t, got.FilingNumber)
	c, err := cases.Get(context.Background(), id)
	require.NoError(t, err)
	assert.Equal(t, domain.StatusReturned, c.Status)

	got.MissingSupports[0] = "otro"
	reviews, err := store.Reviews(context.Background(), id)
	require.NoError(t, err)
	require.Len(t, reviews, 1)
	assert.Equal(t, []string{domain.SupportPoliceReport}, reviews[0].MissingSupports, "nothing is shared with the caller")

	_, err = store.Review(context.Background(), r)
	assert.ErrorIs(t, err, domain.ErrNotReviewable)
	r.ClaimID = "no-existe"
	_, err = store.Review(context.Background(), r)
	assert.ErrorIs(t, err, ports.ErrClaimNotFound)

	history, err := store.History(context.Background(), id)
	require.NoError(t, err)
	require.Len(t, history, 2)
	assert.Equal(t, domain.StatusActive, history[0].Status)
	assert.True(t, history[0].ChangedAt.Equal(filedAt))
	assert.Equal(t, domain.StatusReturned, history[1].Status)
	assert.Equal(t, domain.StatusActive, history[1].PreviousStatus)
	assert.True(t, history[1].ChangedAt.Equal(at))
}

func TestTheMemoryStoreRecordsTheChangesOfTheDecision(t *testing.T) {
	cases, id := filedForReview(t)
	reviews := memory.NewReviewStore(cases)
	_, err := reviews.Review(context.Background(), domain.Review{ClaimID: id, Outcome: domain.ReviewApproved,
		ReviewedBy: "asesor", ReviewedAt: filedAt.Add(time.Hour)})
	require.NoError(t, err)
	decisions := memory.NewDecisionStore(cases)
	_, err = decisions.Decide(context.Background(), domain.Decision{ClaimID: id, Outcome: domain.OutcomeRejected,
		DecidedBy: "analista", DecidedAt: filedAt.Add(2 * time.Hour), Reason: "x",
		Notice: &domain.Notice{To: "c@correo.example"}})
	require.NoError(t, err)

	history, err := reviews.History(context.Background(), id)

	require.NoError(t, err)
	var statuses []domain.Status
	for _, c := range history {
		statuses = append(statuses, c.Status)
	}
	assert.Equal(t, []domain.Status{domain.StatusActive, domain.StatusInEvaluation, domain.StatusRejected}, statuses)
	empty, err := reviews.History(context.Background(), "no-existe")
	require.NoError(t, err)
	assert.Empty(t, empty)
}
