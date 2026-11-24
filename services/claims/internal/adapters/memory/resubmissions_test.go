package memory_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/adapters/memory"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/ports"
)

// The resubmission of a returned filing in memory (HU-W2.1-04), with the
// rules of PostgreSQL.

func returnedForResubmission(t *testing.T) (*memory.CaseStore, *memory.ReviewStore, string) {
	t.Helper()
	cases, id := filedForReview(t)
	store := memory.NewReviewStore(cases)
	_, err := store.Review(context.Background(), domain.Review{ClaimID: id, Outcome: domain.ReviewReturned,
		ReviewedBy: "asesor", ReviewedAt: filedAt.Add(time.Hour), ReasonCode: domain.ReasonMissingInformation,
		Message: "Falta la denuncia.", FactsToCorrect: []domain.Fact{domain.FactPlace}})
	require.NoError(t, err)
	return cases, store, id
}

func resubmissionOf(t *testing.T, cases *memory.CaseStore, id string, support domain.SupportFile) domain.Resubmission {
	t.Helper()
	c, err := cases.Get(context.Background(), id)
	require.NoError(t, err)
	c.Place, c.Status = "Parqueadero del centro comercial Andino, Bogotá", domain.StatusActive
	added := domain.AttachedSupport{RequirementCode: domain.SupportPoliceReport, File: support}
	c.Supports = []domain.AttachedSupport{added}
	return domain.Resubmission{ClaimID: id, ReviewSeq: 1, ResubmittedBy: "sub-carolina",
		ResubmittedAt: filedAt.Add(2 * time.Hour),
		Facts:         []domain.FactChange{{Fact: domain.FactPlace, After: c.Place}},
		Supports: []domain.SupportChange{{RequirementCode: domain.SupportPoliceReport, Label: "Denuncia ante autoridad",
			Added: []string{support.ID}}},
		Claim: c, Added: []domain.AttachedSupport{added}}
}

func denuncia(n int) domain.SupportFile {
	return domain.SupportFile{ID: fmt.Sprintf("sup-%032x", n), OwnerSubject: "sub-carolina", Format: domain.FormatPDF}
}

func TestTheMemoryStoreResubmitsAReturnedFiling(t *testing.T) {
	cases, store, id := returnedForResubmission(t)
	ctx := context.Background()

	got, err := store.Resubmit(ctx, resubmissionOf(t, cases, id, denuncia(1)))

	require.NoError(t, err)
	assert.Equal(t, 1, got.Seq)
	c, err := cases.Get(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, domain.StatusActive, c.Status)
	assert.Equal(t, "Parqueadero del centro comercial Andino, Bogotá", c.Place)
	require.Len(t, c.Supports, 1)

	got.Supports[0].Added[0] = "cambiado"
	all, err := store.Resubmissions(ctx, id)
	require.NoError(t, err)
	require.Len(t, all, 1)
	assert.Equal(t, denuncia(1).ID, all[0].Supports[0].Added[0], "nothing is shared with the caller")
	history, err := store.History(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, domain.StatusActive, history[len(history)-1].Status)

	_, err = store.Resubmit(ctx, resubmissionOf(t, cases, id, denuncia(2)))
	assert.ErrorIs(t, err, domain.ErrNotReturned, "it was sent already")
}

func TestTheMemoryStoreRefusesWhatPostgreSQLRefuses(t *testing.T) {
	cases, store, id := returnedForResubmission(t)
	ctx := context.Background()
	r := resubmissionOf(t, cases, id, denuncia(3))

	stale := r
	stale.ReviewSeq = 2
	_, err := store.Resubmit(ctx, stale)
	assert.ErrorIs(t, err, domain.ErrNotReturned)

	missing := r
	missing.ClaimID = "no-existe"
	_, err = store.Resubmit(ctx, missing)
	assert.ErrorIs(t, err, ports.ErrClaimNotFound)

	other, _, err := cases.CreateWeb(ctx, domain.ClaimRecord{Claim: domain.Claim{ID: "cl-2", Subject: "sub-carolina",
		FilingKey: "k2", Status: domain.StatusActive, FiledAt: filedAt, Details: map[string]string{}},
		Supports: []domain.AttachedSupport{{RequirementCode: domain.SupportPoliceReport, File: denuncia(3)}}})
	require.NoError(t, err)
	require.NotEmpty(t, other.ID)
	_, err = store.Resubmit(ctx, r)
	assert.ErrorIs(t, err, ports.ErrSupportAlreadyAttached)
	c, err := cases.Get(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, domain.StatusReturned, c.Status, "nothing changed")
}
