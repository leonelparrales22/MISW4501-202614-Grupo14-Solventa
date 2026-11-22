package assessors_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/adapters/memory"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/assessors"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/ports"
)

// The follow-up of the intervention of the provider and the closing of the
// case (HU-W2.3-02), over the stores in memory.

type interventionScene struct {
	*scene
	steps         *memory.InterventionStore
	interventions *assessors.InterventionService
}

// assignedScene is a claim in evaluation with the provider of the mockup
// assigned.
func assignedScene(t *testing.T) *interventionScene {
	t.Helper()
	s := newScene(t, mockup()...)
	s.file(t, "cl-1", domain.IncidentWaterDamage, domain.StatusInEvaluation, fromApp(8))
	s.search(t, "cl-1")
	_, err := s.svc.Assign(context.Background(), "cl-1", "prv-servicios-hogar-norte", 1, analyst)
	require.NoError(t, err)
	steps := memory.NewInterventionStore(s.store)
	return &interventionScene{scene: s, steps: steps,
		interventions: assessors.NewInterventionService(steps, s.clock)}
}

// step records the next step one hour after the clock, and moves the clock
// past it.
func (s *interventionScene) step(t *testing.T, step domain.InterventionStep) assessors.InterventionView {
	t.Helper()
	s.clock.advance(time.Hour)
	occurred := s.clock.Now()
	in := domain.StepRequest{Step: step, OccurredAt: &occurred, Note: "reportado por el prestador"}
	if step == domain.StepVisitDone {
		cost, days, photos := int64(435_000_000), 2, 8
		in.Report = &domain.ReportRequest{Cause: "Filtración por tubería del piso superior",
			Damage: "Techo y muro de cocina · 6 m²", RepairCostMinor: &cost, RepairDays: &days, Photos: &photos}
	}
	v, err := s.interventions.RecordStep(context.Background(), "cl-1", in, analyst)
	require.NoError(t, err)
	return v
}

func (s *interventionScene) finish(t *testing.T) {
	t.Helper()
	for _, step := range domain.InterventionSteps {
		s.step(t, step)
	}
}

func TestTheInterventionStartsWithTheAssignment(t *testing.T) {
	s := assignedScene(t)
	v, err := s.interventions.View(context.Background(), "cl-1")

	require.NoError(t, err)
	assert.Equal(t, domain.InterventionInProgress, v.State)
	assert.Equal(t, "prv-servicios-hogar-norte", v.Assignment.ProviderID)
	assert.Empty(t, v.Steps)
	assert.Equal(t, domain.StepAccepted, v.Next)
	assert.Equal(t, domain.InterventionSteps, v.Missing)
	assert.False(t, v.Closable())
}

func TestAClaimWithoutProviderHasNoInterventionToFollow(t *testing.T) {
	s := newScene(t, mockup()...)
	s.file(t, "cl-2", domain.IncidentWaterDamage, domain.StatusInEvaluation, fromApp(8))
	in := assessors.NewInterventionService(memory.NewInterventionStore(s.store), s.clock)

	v, err := in.View(context.Background(), "cl-2")
	require.NoError(t, err)
	assert.Equal(t, domain.InterventionNotAssigned, v.State)
	assert.Equal(t, domain.InterventionStep(""), v.Next)

	occurred := start
	_, err = in.RecordStep(context.Background(), "cl-2", domain.StepRequest{Step: domain.StepAccepted,
		OccurredAt: &occurred}, analyst)
	assert.ErrorIs(t, err, domain.ErrNotAssigned)

	_, err = in.View(context.Background(), "nope")
	assert.ErrorIs(t, err, ports.ErrClaimNotFound)
}

func TestEachProgressIsReflectedAtOnce(t *testing.T) {
	s := assignedScene(t)
	v := s.step(t, domain.StepAccepted)
	assert.Len(t, v.Steps, 1)
	assert.Equal(t, domain.StepEnRoute, v.Next)

	s.step(t, domain.StepEnRoute)
	v = s.step(t, domain.StepVisitDone)
	require.NotNil(t, v.Report)
	assert.Equal(t, int64(435_000_000), v.Report.RepairCostMinor)
	assert.Equal(t, 8, v.Report.Photos)
	assert.Equal(t, analyst, v.Steps[2].RecordedBy)
	assert.Equal(t, []domain.InterventionStep{domain.StepRepairAuthorized, domain.StepRepairExecuted}, v.Missing)

	c, err := s.cases.Get(context.Background(), "cl-1")
	require.NoError(t, err)
	assert.Equal(t, domain.StatusInEvaluation, c.Status, "the status of the claim does not change")
}

func TestAStepRecordedTwiceIsOutOfOrder(t *testing.T) {
	s := assignedScene(t)
	s.step(t, domain.StepAccepted)
	occurred := s.clock.Now()
	_, err := s.interventions.RecordStep(context.Background(), "cl-1", domain.StepRequest{Step: domain.StepAccepted,
		OccurredAt: &occurred}, analyst)
	assert.ErrorIs(t, err, domain.ErrStepOutOfOrder)
}

func TestTheCaseIsNotClosedBeforeTheInterventionIsFinished(t *testing.T) {
	s := assignedScene(t)
	s.step(t, domain.StepAccepted)
	closedAt := s.clock.Now()

	_, err := s.interventions.Close(context.Background(), "cl-1", domain.CloseRequest{Result: domain.ResultRepaired,
		ClosedAt: &closedAt}, analyst)

	var notFinished *domain.NotFinishedError
	require.True(t, errors.As(err, &notFinished))
	assert.Equal(t, domain.InterventionSteps[1:], notFinished.Missing)
}

func TestClosingTheCaseReleasesTheProvider(t *testing.T) {
	s := assignedScene(t)
	s.finish(t)
	v, err := s.interventions.View(context.Background(), "cl-1")
	require.NoError(t, err)
	assert.Equal(t, domain.InterventionReadyToClose, v.State)
	assert.True(t, v.Closable())
	assert.Empty(t, v.Missing)

	s.clock.advance(time.Hour)
	closedAt := s.clock.Now()
	v, err = s.interventions.Close(context.Background(), "cl-1", domain.CloseRequest{Result: domain.ResultRepaired,
		ClosedAt: &closedAt, Notes: "Asegurada conforme"}, analyst)

	require.NoError(t, err)
	assert.Equal(t, domain.InterventionClosed, v.State)
	require.NotNil(t, v.Closure)
	assert.Equal(t, analyst, v.Closure.ClosedBy)
	assert.Equal(t, domain.ResultRepaired, v.Closure.Result)

	p, err := s.providers.Get(context.Background(), "prv-servicios-hogar-norte")
	require.NoError(t, err)
	assert.Equal(t, domain.AvailabilityAvailable, p.Availability)
	assert.Nil(t, p.BusyClaimID)

	c, err := s.cases.Get(context.Background(), "cl-1")
	require.NoError(t, err)
	assert.Equal(t, domain.StatusInEvaluation, c.Status, "closing never approves, rejects nor pays")

	_, err = s.interventions.Close(context.Background(), "cl-1", domain.CloseRequest{Result: domain.ResultRepaired,
		ClosedAt: &closedAt}, analyst)
	assert.ErrorIs(t, err, domain.ErrCaseClosed)
	occurred := s.clock.Now()
	_, err = s.interventions.RecordStep(context.Background(), "cl-1", domain.StepRequest{
		Step: domain.StepAccepted, OccurredAt: &occurred}, analyst)
	assert.ErrorIs(t, err, domain.ErrCaseClosed)
}

// failingInterventions is a store whose writes fail as the database would.
type failingInterventions struct {
	ports.InterventionStore
	err error
}

func (f failingInterventions) RecordStep(context.Context, domain.StepRecord) error { return f.err }
func (f failingInterventions) Close(context.Context, domain.Closure) error         { return f.err }

func TestAStepAnotherAnalystRecordedFirstIsOutOfOrder(t *testing.T) {
	s := assignedScene(t)
	for want, stored := range map[error]error{domain.ErrStepOutOfOrder: ports.ErrStepConflict,
		domain.ErrCaseClosed: domain.ErrCaseClosed} {
		svc := assessors.NewInterventionService(failingInterventions{InterventionStore: s.steps, err: stored}, s.clock)
		occurred := s.clock.Now()
		_, err := svc.RecordStep(context.Background(), "cl-1", domain.StepRequest{Step: domain.StepAccepted,
			OccurredAt: &occurred}, analyst)
		assert.ErrorIs(t, err, want)
	}
}

func TestAClosingTheStoreRefusesIsNotLost(t *testing.T) {
	s := assignedScene(t)
	s.finish(t)
	// Another analyst's view of the database disagrees with the one read.
	svc := assessors.NewInterventionService(failingInterventions{InterventionStore: s.steps,
		err: domain.ErrInterventionNotFinished}, s.clock)
	closedAt := s.clock.Now()
	_, err := svc.Close(context.Background(), "cl-1", domain.CloseRequest{Result: domain.ResultRepaired,
		ClosedAt: &closedAt}, analyst)
	assert.ErrorIs(t, err, domain.ErrInterventionNotFinished)
}
