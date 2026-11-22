package memory_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/adapters/memory"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/ports"
)

// The follow-up of the intervention and the closing of the case in memory
// (HU-W2.3-02), with the rules of PostgreSQL.

func TestTheInterventionInMemory(t *testing.T) {
	s := newAssignmentScene(t)
	require.NoError(t, s.store.RecordLocation(context.Background(), "cl-1", located(1)))
	s.searched(t, "cl-1")
	a := s.assignment(t, "cl-1", "prv-servicios-hogar-norte")
	require.NoError(t, s.store.Assign(context.Background(), a, noticeOf(a)))
	store := memory.NewInterventionStore(s.store)

	step := func(st domain.InterventionStep) domain.StepRecord {
		r := domain.StepRecord{ClaimID: "cl-1", Step: st, OccurredAt: assignedAt, RecordedBy: analyst,
			RecordedAt: assignedAt}
		if st == domain.StepVisitDone {
			r.Report = &domain.ProviderReport{Cause: "Filtración", Damage: "Techo", RepairCostMinor: 1, RepairDays: 2}
		}
		return r
	}
	closure := domain.Closure{ClaimID: "cl-1", Result: domain.ResultRepaired, ClosedAt: assignedAt, ClosedBy: analyst,
		RecordedAt: assignedAt}

	assert.ErrorIs(t, store.RecordStep(context.Background(), step(domain.StepEnRoute)), ports.ErrStepConflict)
	assert.ErrorIs(t, store.Close(context.Background(), closure), domain.ErrInterventionNotFinished)
	for _, st := range domain.InterventionSteps {
		require.NoError(t, store.RecordStep(context.Background(), step(st)))
	}
	assert.ErrorIs(t, store.RecordStep(context.Background(), step(domain.StepAccepted)), ports.ErrStepConflict)

	k, err := store.Intervention(context.Background(), "cl-1")
	require.NoError(t, err)
	require.Len(t, k.Steps, 5)
	assert.Equal(t, a, *k.Assignment)
	k.Steps[2].Report.Cause = "Otra"
	again, err := store.Intervention(context.Background(), "cl-1")
	require.NoError(t, err)
	assert.Equal(t, "Filtración", again.Steps[2].Report.Cause, "what the caller gets is a copy")

	require.NoError(t, store.Close(context.Background(), closure))
	assert.ErrorIs(t, store.Close(context.Background(), closure), domain.ErrCaseClosed)
	assert.ErrorIs(t, store.RecordStep(context.Background(), step(domain.StepAccepted)), domain.ErrCaseClosed)
	k, err = store.Intervention(context.Background(), "cl-1")
	require.NoError(t, err)
	assert.Equal(t, closure, *k.Closure)

	p, err := s.providers.Get(context.Background(), "prv-servicios-hogar-norte")
	require.NoError(t, err)
	assert.True(t, p.Available(), "closing releases the provider")

	_, err = store.Intervention(context.Background(), "cl-x")
	assert.ErrorIs(t, err, ports.ErrClaimNotFound)
}
