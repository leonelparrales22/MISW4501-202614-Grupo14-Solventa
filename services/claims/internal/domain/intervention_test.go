package domain_test

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
)

// The follow-up of the intervention of the provider and the closing of the
// case (HU-W2.3-02).

var assignedAt = time.Date(2026, 9, 6, 15, 15, 0, 0, time.UTC)

func assignmentOf() *domain.Assignment {
	return &domain.Assignment{ClaimID: "8f14e45f-ceea-467a-9f3a-5a2d0b1c7e90", ProviderID: "prv-servicios-hogar-norte",
		ProviderName: "Servicios Hogar Norte", AssignedBy: analyst, AssignedAt: assignedAt}
}

func at(hours int) *time.Time {
	t := assignedAt.Add(time.Duration(hours) * time.Hour)
	return &t
}

func visitReport() *domain.ReportRequest {
	cost, days, photos := int64(435_000_000), 2, 8
	return &domain.ReportRequest{Cause: "Filtración por tubería del piso superior", Damage: "Techo y muro de cocina",
		RepairCostMinor: &cost, RepairDays: &days, Photos: &photos}
}

// stepsUpTo records the steps of the intervention up to n, one hour apart.
func stepsUpTo(t *testing.T, n int) []domain.StepRecord {
	t.Helper()
	var steps []domain.StepRecord
	for i := 0; i < n; i++ {
		in := domain.StepRequest{Step: domain.InterventionSteps[i], OccurredAt: at(i + 1)}
		if in.Step == domain.StepVisitDone {
			in.Report = visitReport()
		}
		r, err := domain.RecordStep(assignmentOf(), steps, nil, in, analyst, *at(24))
		require.NoError(t, err)
		steps = append(steps, r)
	}
	return steps
}

func invalidFields(t *testing.T, err error) []domain.FieldError {
	t.Helper()
	var invalid *domain.ValidationError
	require.ErrorAs(t, err, &invalid)
	return invalid.Fields
}

func TestTheStepsAreRecordedInOrderWithWhoAndWhen(t *testing.T) {
	now := *at(24)
	r, err := domain.RecordStep(assignmentOf(), nil, nil, domain.StepRequest{Step: domain.StepAccepted,
		OccurredAt: at(1), Note: "  visita programada 15/09 8:00 a 12:00 "}, analyst, now)

	require.NoError(t, err)
	assert.Equal(t, domain.StepRecord{ClaimID: assignmentOf().ClaimID, Step: domain.StepAccepted,
		OccurredAt: *at(1), Note: "visita programada 15/09 8:00 a 12:00", RecordedBy: analyst, RecordedAt: now}, r)

	steps := stepsUpTo(t, 5)
	assert.Len(t, steps, 5)
	assert.Equal(t, domain.InterventionStep(""), domain.NextStep(steps))
	assert.Empty(t, domain.MissingSteps(steps))
	assert.Equal(t, int64(435_000_000), domain.ReportOf(steps).RepairCostMinor)
}

func TestAStepThatIsNotTheNextOneIsOutOfOrder(t *testing.T) {
	steps := stepsUpTo(t, 1)
	for _, step := range []domain.InterventionStep{domain.StepAccepted, domain.StepVisitDone} {
		_, err := domain.RecordStep(assignmentOf(), steps, nil, domain.StepRequest{Step: step, OccurredAt: at(3)},
			analyst, *at(24))
		assert.ErrorIs(t, err, domain.ErrStepOutOfOrder, step)
	}
	_, err := domain.RecordStep(assignmentOf(), stepsUpTo(t, 5), nil,
		domain.StepRequest{Step: domain.StepRepairExecuted, OccurredAt: at(9)}, analyst, *at(24))
	assert.ErrorIs(t, err, domain.ErrStepOutOfOrder)
}

func TestAStepNeedsAProviderAndAnOpenCase(t *testing.T) {
	_, err := domain.RecordStep(nil, nil, nil, domain.StepRequest{Step: domain.StepAccepted, OccurredAt: at(1)},
		analyst, *at(24))
	assert.ErrorIs(t, err, domain.ErrNotAssigned)

	_, err = domain.RecordStep(assignmentOf(), nil, &domain.Closure{}, domain.StepRequest{Step: domain.StepAccepted,
		OccurredAt: at(1)}, analyst, *at(24))
	assert.ErrorIs(t, err, domain.ErrCaseClosed)

	_, err = domain.RecordStep(assignmentOf(), nil, nil, domain.StepRequest{Step: "teleported", OccurredAt: at(1)},
		analyst, *at(24))
	assert.Equal(t, []domain.FieldError{{Field: "step", Code: domain.CodeInvalidFormat}}, invalidFields(t, err))
}

func TestTheInstantOfAStepIsBetweenTheLastOneAndNow(t *testing.T) {
	steps := stepsUpTo(t, 2) // the last one at +2h
	now := *at(24)
	cases := map[string]*time.Time{"missing": nil, "before the last step": at(1), "in the future": at(25)}
	for name, occurred := range cases {
		_, err := domain.RecordStep(assignmentOf(), steps, nil, domain.StepRequest{Step: domain.StepVisitDone,
			OccurredAt: occurred, Report: visitReport()}, analyst, now)
		code := domain.CodeOutOfRange
		if occurred == nil {
			code = domain.CodeRequired
		}
		assert.Equal(t, []domain.FieldError{{Field: "occurred_at", Code: code}}, invalidFields(t, err), name)
	}

	before := assignedAt.Add(-time.Minute)
	_, err := domain.RecordStep(assignmentOf(), nil, nil, domain.StepRequest{Step: domain.StepAccepted,
		OccurredAt: &before}, analyst, now)
	assert.Equal(t, []domain.FieldError{{Field: "occurred_at", Code: domain.CodeOutOfRange}}, invalidFields(t, err),
		"before the assignment")

	sameMinute := assignedAt.Add(35 * time.Second)
	a := assignmentOf()
	a.AssignedAt = sameMinute
	_, err = domain.RecordStep(a, nil, nil, domain.StepRequest{Step: domain.StepAccepted,
		OccurredAt: &assignedAt}, analyst, now)
	assert.NoError(t, err, "the screen types minutes: the same minute as the assignment is not before it")

	skewed := now.Add(2 * time.Minute)
	_, err = domain.RecordStep(assignmentOf(), steps, nil, domain.StepRequest{Step: domain.StepVisitDone,
		OccurredAt: &skewed, Report: visitReport()}, analyst, now)
	assert.NoError(t, err, "a browser a little ahead of the server")
}

func TestTheVisitCarriesTheWholeReport(t *testing.T) {
	steps := stepsUpTo(t, 2)
	_, err := domain.RecordStep(assignmentOf(), steps, nil, domain.StepRequest{Step: domain.StepVisitDone,
		OccurredAt: at(3)}, analyst, *at(24))
	assert.Equal(t, []domain.FieldError{{Field: "report", Code: domain.CodeRequired}}, invalidFields(t, err))

	negative, tooLong, tooMany := int64(-1), 366, 51
	_, err = domain.RecordStep(assignmentOf(), steps, nil, domain.StepRequest{Step: domain.StepVisitDone,
		OccurredAt: at(3), Report: &domain.ReportRequest{Cause: "  ", Damage: "ok", RepairCostMinor: &negative,
			RepairDays: &tooLong, Photos: &tooMany}}, analyst, *at(24))
	assert.ElementsMatch(t, []domain.FieldError{
		{Field: "report.cause", Code: domain.CodeRequired}, {Field: "report.damage", Code: domain.CodeOutOfRange},
		{Field: "report.repair_cost_minor", Code: domain.CodeOutOfRange},
		{Field: "report.repair_days", Code: domain.CodeOutOfRange}, {Field: "report.photos", Code: domain.CodeOutOfRange},
	}, invalidFields(t, err))

	_, err = domain.RecordStep(assignmentOf(), steps, nil, domain.StepRequest{Step: domain.StepVisitDone,
		OccurredAt: at(3), Report: &domain.ReportRequest{Cause: "Filtración", Damage: "Techo"}}, analyst, *at(24))
	assert.ElementsMatch(t, []domain.FieldError{{Field: "report.repair_cost_minor", Code: domain.CodeRequired},
		{Field: "report.repair_days", Code: domain.CodeRequired}}, invalidFields(t, err), "the photos are optional")

	_, err = domain.RecordStep(assignmentOf(), nil, nil, domain.StepRequest{Step: domain.StepAccepted,
		OccurredAt: at(1), Report: visitReport(), Note: string(make([]rune, domain.MaxInterventionNote+1))},
		analyst, *at(24))
	assert.ElementsMatch(t, []domain.FieldError{{Field: "note", Code: domain.CodeOutOfRange},
		{Field: "report", Code: domain.CodeOutOfRange}}, invalidFields(t, err), "only the visit carries a report")
}

func TestACaseClosesOnlyAfterTheInterventionIsFinished(t *testing.T) {
	_, err := domain.CloseCase(assignmentOf(), stepsUpTo(t, 3), nil, domain.CloseRequest{
		Result: domain.ResultRepaired, ClosedAt: at(10)}, analyst, *at(24))

	assert.ErrorIs(t, err, domain.ErrInterventionNotFinished)
	var notFinished *domain.NotFinishedError
	require.True(t, errors.As(err, &notFinished))
	assert.Equal(t, []domain.InterventionStep{domain.StepRepairAuthorized, domain.StepRepairExecuted},
		notFinished.Missing)
	assert.Equal(t, domain.ErrInterventionNotFinished.Error(), err.Error())
}

func TestTheClosingKeepsTheResultAndTheAnalystResponsible(t *testing.T) {
	now := *at(24)
	c, err := domain.CloseCase(assignmentOf(), stepsUpTo(t, 5), nil, domain.CloseRequest{
		Result: domain.ResultRepaired, ClosedAt: at(6), Notes: " Asegurada conforme "}, analyst, now)

	require.NoError(t, err)
	assert.Equal(t, domain.Closure{ClaimID: assignmentOf().ClaimID, Result: domain.ResultRepaired, ClosedAt: *at(6),
		Notes: "Asegurada conforme", ClosedBy: analyst, RecordedAt: now}, c)
}

func TestAClosingWithoutResultSaysWhatIsMissing(t *testing.T) {
	steps := stepsUpTo(t, 5) // finished at +5h
	_, err := domain.CloseCase(assignmentOf(), steps, nil, domain.CloseRequest{ClosedAt: at(4),
		Notes: string(make([]rune, domain.MaxInterventionNote+1))}, analyst, *at(24))
	assert.ElementsMatch(t, []domain.FieldError{{Field: "result", Code: domain.CodeRequired},
		{Field: "closed_at", Code: domain.CodeOutOfRange}, {Field: "notes", Code: domain.CodeOutOfRange}},
		invalidFields(t, err))

	_, err = domain.CloseCase(assignmentOf(), steps, nil, domain.CloseRequest{Result: "approved"}, analyst, *at(24))
	assert.ElementsMatch(t, []domain.FieldError{{Field: "result", Code: domain.CodeInvalidFormat},
		{Field: "closed_at", Code: domain.CodeRequired}}, invalidFields(t, err), "closing never approves the claim")
}

func TestACaseClosesOnce(t *testing.T) {
	_, err := domain.CloseCase(assignmentOf(), stepsUpTo(t, 5), &domain.Closure{}, domain.CloseRequest{
		Result: domain.ResultRepaired, ClosedAt: at(6)}, analyst, *at(24))
	assert.ErrorIs(t, err, domain.ErrCaseClosed)

	_, err = domain.CloseCase(nil, nil, nil, domain.CloseRequest{}, analyst, *at(24))
	assert.ErrorIs(t, err, domain.ErrNotAssigned)
}

func TestTheStateOfTheIntervention(t *testing.T) {
	assert.Equal(t, domain.InterventionNotAssigned, domain.InterventionStateOf(nil, nil, nil))
	assert.Equal(t, domain.InterventionInProgress, domain.InterventionStateOf(assignmentOf(), nil, nil))
	assert.Equal(t, domain.InterventionInProgress, domain.InterventionStateOf(assignmentOf(), stepsUpTo(t, 4), nil))
	assert.Equal(t, domain.InterventionReadyToClose,
		domain.InterventionStateOf(assignmentOf(), stepsUpTo(t, 5), nil))
	assert.Equal(t, domain.InterventionClosed,
		domain.InterventionStateOf(assignmentOf(), stepsUpTo(t, 5), &domain.Closure{}))
	assert.Nil(t, domain.ReportOf(stepsUpTo(t, 2)))
	assert.Equal(t, domain.InterventionSteps, domain.MissingSteps(nil))
	assert.True(t, domain.KnownStep(domain.StepEnRoute))
}
