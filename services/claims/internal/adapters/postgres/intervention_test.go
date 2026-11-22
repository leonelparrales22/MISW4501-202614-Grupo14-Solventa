package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/adapters/postgres"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/assessors"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/ports"
)

// The follow-up of the intervention and the closing of the case in
// PostgreSQL (HU-W2.3-02): what is written reads back the same, it only
// grows, of two analysts recording the same step only one wins, and closing
// releases the provider in the same transaction.

type interventionStores struct {
	assignmentStores
	steps         *postgres.InterventionStore
	interventions *assessors.InterventionService
}

// assignedClaim is a claim in evaluation with Servicios Hogar Norte assigned.
func assignedClaim(t *testing.T) interventionStores {
	t.Helper()
	s := newAssignmentStores(t)
	s.file(t, "cl-1", capture(0, 8, true))
	s.search(t, "cl-1")
	_, err := s.svc.Assign(context.Background(), "cl-1", "prv-servicios-hogar-norte", 1, assignedBy)
	require.NoError(t, err)
	steps := postgres.InterventionStoreOver(s.store)
	return interventionStores{assignmentStores: s, steps: steps,
		interventions: assessors.NewInterventionService(steps, s.clock)}
}

func (s interventionStores) step(t *testing.T, step domain.InterventionStep) {
	t.Helper()
	s.clock.advance(time.Hour)
	occurred := s.clock.Now()
	in := domain.StepRequest{Step: step, OccurredAt: &occurred}
	if step == domain.StepVisitDone {
		cost, days, photos := int64(435_000_000), 2, 8
		in.Report = &domain.ReportRequest{Cause: "Filtración por tubería del piso superior",
			Damage: "Techo y muro de cocina · 6 m²", RepairCostMinor: &cost, RepairDays: &days, Photos: &photos}
	}
	_, err := s.interventions.RecordStep(context.Background(), "cl-1", in, assignedBy)
	require.NoError(t, err)
}

func TestTheStepsAndTheClosingReadBackAsTheyWereRecorded(t *testing.T) {
	s := assignedClaim(t)
	k, err := s.steps.Intervention(context.Background(), "cl-1")
	require.NoError(t, err)
	require.NotNil(t, k.Assignment)
	assert.Empty(t, k.Steps)
	assert.Nil(t, k.Closure)

	s.clock.advance(time.Hour)
	occurred := s.clock.Now()
	_, err = s.interventions.RecordStep(context.Background(), "cl-1", domain.StepRequest{Step: domain.StepAccepted,
		OccurredAt: &occurred, Note: "visita programada 15/09 8:00 a 12:00"}, assignedBy)
	require.NoError(t, err)
	for _, step := range domain.InterventionSteps[1:] {
		s.step(t, step)
	}
	s.clock.advance(time.Hour)
	closedAt := s.clock.Now()
	v, err := s.interventions.Close(context.Background(), "cl-1", domain.CloseRequest{Result: domain.ResultRepaired,
		ClosedAt: &closedAt, Notes: "Asegurada conforme"}, assignedBy)
	require.NoError(t, err)

	assert.Equal(t, domain.InterventionClosed, v.State)
	require.Len(t, v.Steps, 5)
	assert.Equal(t, domain.StepRecord{ClaimID: "cl-1", Step: domain.StepAccepted, OccurredAt: occurred,
		Note: "visita programada 15/09 8:00 a 12:00", RecordedBy: assignedBy, RecordedAt: occurred}, v.Steps[0])
	for i, step := range domain.InterventionSteps {
		assert.Equal(t, step, v.Steps[i].Step)
	}
	assert.Equal(t, &domain.ProviderReport{Cause: "Filtración por tubería del piso superior",
		Damage: "Techo y muro de cocina · 6 m²", RepairCostMinor: 435_000_000, RepairDays: 2, Photos: 8}, v.Report)
	assert.Nil(t, v.Steps[3].Report)
	assert.Equal(t, &domain.Closure{ClaimID: "cl-1", Result: domain.ResultRepaired, ClosedAt: closedAt,
		Notes: "Asegurada conforme", ClosedBy: assignedBy, RecordedAt: closedAt}, v.Closure)

	p, err := s.providers.Get(context.Background(), "prv-servicios-hogar-norte")
	require.NoError(t, err)
	assert.True(t, p.Available(), "closing the case releases the provider")
	assert.Nil(t, p.BusyClaimID)
	assert.Equal(t, assignedBy.Email, p.UpdatedBy)

	var status string
	require.NoError(t, s.db.QueryRow(`SELECT status FROM claim WHERE id = 'cl-1'`).Scan(&status))
	assert.Equal(t, string(domain.StatusInEvaluation), status, "closing never approves, rejects nor pays")
}

func TestOfTwoAnalystsRecordingTheSameStepOneWins(t *testing.T) {
	s := assignedClaim(t)
	s.clock.advance(time.Hour)
	r := domain.StepRecord{ClaimID: "cl-1", Step: domain.StepAccepted, OccurredAt: s.clock.Now(),
		RecordedBy: assignedBy, RecordedAt: s.clock.Now()}

	errs := race(func() error { return s.steps.RecordStep(context.Background(), r) },
		func() error { return s.steps.RecordStep(context.Background(), r) })

	assert.ElementsMatch(t, []error{nil, ports.ErrStepConflict}, errs)
	assert.Equal(t, 1, s.count(t, `SELECT count(*) FROM claim_intervention_step`))
}

func TestAStepWithoutTheOneBeforeIsNotWritten(t *testing.T) {
	s := assignedClaim(t)
	r := domain.StepRecord{ClaimID: "cl-1", Step: domain.StepEnRoute, OccurredAt: s.clock.Now(),
		RecordedBy: assignedBy, RecordedAt: s.clock.Now()}
	assert.ErrorIs(t, s.steps.RecordStep(context.Background(), r), ports.ErrStepConflict)
	assert.ErrorIs(t, s.steps.Close(context.Background(), domain.Closure{ClaimID: "cl-1",
		Result: domain.ResultRepaired, ClosedAt: s.clock.Now(), ClosedBy: assignedBy, RecordedAt: s.clock.Now()}),
		domain.ErrInterventionNotFinished)
	assert.Zero(t, s.count(t, `SELECT count(*) FROM claim_intervention_step`))
	assert.Zero(t, s.count(t, `SELECT count(*) FROM claim_closure`))
}

func TestOfTwoAnalystsClosingTheCaseOneWinsAndNothingFollows(t *testing.T) {
	s := assignedClaim(t)
	for _, step := range domain.InterventionSteps {
		s.step(t, step)
	}
	c := domain.Closure{ClaimID: "cl-1", Result: domain.ResultRepaired, ClosedAt: s.clock.Now(),
		ClosedBy: assignedBy, RecordedAt: s.clock.Now()}

	errs := race(func() error { return s.steps.Close(context.Background(), c) },
		func() error { return s.steps.Close(context.Background(), c) })

	assert.ElementsMatch(t, []error{nil, domain.ErrCaseClosed}, errs)
	assert.Equal(t, 1, s.count(t, `SELECT count(*) FROM claim_closure`))
	// Even past the service, a closed case takes no step.
	r := domain.StepRecord{ClaimID: "cl-1", Step: domain.StepAccepted, OccurredAt: s.clock.Now(),
		RecordedBy: assignedBy, RecordedAt: s.clock.Now()}
	assert.ErrorIs(t, s.steps.RecordStep(context.Background(), r), domain.ErrCaseClosed)
}

func TestTheInterventionIsAppendOnly(t *testing.T) {
	s := assignedClaim(t)
	for _, step := range domain.InterventionSteps {
		s.step(t, step)
	}
	require.NoError(t, s.steps.Close(context.Background(), domain.Closure{ClaimID: "cl-1",
		Result: domain.ResultRepaired, ClosedAt: s.clock.Now(), ClosedBy: assignedBy, RecordedAt: s.clock.Now()}))

	for _, stmt := range []string{
		`UPDATE claim_intervention_step SET note = 'otra cosa'`,
		`DELETE FROM claim_intervention_step`,
		`TRUNCATE claim_intervention_step`,
		`UPDATE claim_closure SET result = 'not_repairable'`,
		`DELETE FROM claim_closure`,
		`TRUNCATE claim_closure`,
	} {
		_, err := s.db.Exec(stmt)
		require.Error(t, err, stmt)
		assert.Contains(t, err.Error(), "append-only", stmt)
	}
	assert.Equal(t, 5, s.count(t, `SELECT count(*) FROM claim_intervention_step`))
}

func TestOnlyTheVisitCarriesTheReport(t *testing.T) {
	s := assignedClaim(t)
	_, err := s.db.Exec(`INSERT INTO claim_intervention_step (claim_id, step, position, occurred_at, cause,
		recorded_by, recorded_at) VALUES ('cl-1', 'accepted', 1, now(), 'una causa', 'x', now())`)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "claim_intervention_step_report")
}

func TestTheInterventionOfAClaimThatDoesNotExist(t *testing.T) {
	s := assignedClaim(t)
	_, err := s.steps.Intervention(context.Background(), "nope")
	assert.ErrorIs(t, err, ports.ErrClaimNotFound)
}
