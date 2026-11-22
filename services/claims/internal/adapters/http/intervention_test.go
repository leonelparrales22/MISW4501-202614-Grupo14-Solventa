package httpadapter_test

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	httpadapter "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/adapters/http"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/assessors"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/ports"
)

// The routes of the follow-up of the intervention and the closing of the case
// (HU-W2.3-02).

type fakeInterventions struct {
	view  assessors.InterventionView
	err   error
	calls int

	gotClaim string
	gotStep  domain.StepRequest
	gotClose domain.CloseRequest
	gotBy    domain.Analyst
}

func (f *fakeInterventions) View(_ context.Context, id string) (assessors.InterventionView, error) {
	f.calls++
	f.gotClaim = id
	return f.view, f.err
}

func (f *fakeInterventions) RecordStep(_ context.Context, id string, in domain.StepRequest,
	by domain.Analyst) (assessors.InterventionView, error) {
	f.calls++
	f.gotClaim, f.gotStep, f.gotBy = id, in, by
	return f.view, f.err
}

func (f *fakeInterventions) Close(_ context.Context, id string, in domain.CloseRequest,
	by domain.Analyst) (assessors.InterventionView, error) {
	f.calls++
	f.gotClaim, f.gotClose, f.gotBy = id, in, by
	return f.view, f.err
}

func interventionRouter(f *fakeInterventions) http.Handler {
	return httpadapter.NewRouter(httpadapter.WithIntervention(f, analysts))
}

// inProgress is the screen O8 of the mockup: the visit reported, the repair
// authorized next.
func inProgress() assessors.InterventionView {
	a := assigned().Assignment
	report := &domain.ProviderReport{Cause: "Filtración por tubería del piso superior",
		Damage: "Techo y muro de cocina · 6 m²", RepairCostMinor: 435_000_000, RepairDays: 2, Photos: 8}
	step := func(s domain.InterventionStep, hours int, note string) domain.StepRecord {
		at := a.AssignedAt.Add(time.Duration(hours) * time.Hour)
		return domain.StepRecord{ClaimID: claimID, Step: s, OccurredAt: at, Note: note, RecordedBy: andres,
			RecordedAt: at.Add(time.Minute)}
	}
	visit := step(domain.StepVisitDone, 3, "")
	visit.Report = report
	return assessors.InterventionView{Claim: waterDamage(), State: domain.InterventionInProgress, Assignment: a,
		Steps: []domain.StepRecord{step(domain.StepAccepted, 1, "visita programada 15/09/2026 8:00 a 12:00"),
			step(domain.StepEnRoute, 2, ""), visit},
		Next: domain.StepRepairAuthorized, Report: report,
		Missing: []domain.InterventionStep{domain.StepRepairAuthorized, domain.StepRepairExecuted}}
}

func TestTheInterventionAsTheScreenShowsIt(t *testing.T) {
	f := &fakeInterventions{view: inProgress()}
	status, out, header := send(interventionRouter(f), http.MethodGet, "/v1/claims/"+claimID+"/intervention",
		"operations", "")

	require.Equal(t, http.StatusOK, status)
	assert.Equal(t, "no-store", header.Get("Cache-Control"))
	assert.Equal(t, claimID, f.gotClaim)
	assert.Equal(t, "SIN-2026-01655", out["filing_number"])
	assert.Equal(t, "in_progress", out["state"])
	assert.Equal(t, "repair_authorized", out["next_step"])
	assert.Equal(t, false, out["closable"])
	assert.Equal(t, []any{"repair_authorized", "repair_executed"}, out["missing"])
	assert.Nil(t, out["closure"])

	assignment := out["assignment"].(map[string]any)
	provider := assignment["provider"].(map[string]any)
	assert.Equal(t, "Servicios Hogar Norte", provider["name"])
	assert.Equal(t, "despachos@hogarnorte.example.com", provider["contact_email"])
	assert.Equal(t, 2.3, provider["distance_km"])
	assert.Equal(t, "Andrés Ruiz", assignment["assigned_by"].(map[string]any)["name"])

	steps := out["steps"].([]any)
	require.Len(t, steps, 3)
	first := steps[0].(map[string]any)
	assert.Equal(t, "accepted", first["step"])
	assert.Equal(t, "visita programada 15/09/2026 8:00 a 12:00", first["note"])
	assert.Nil(t, first["report"])
	assert.Nil(t, steps[1].(map[string]any)["note"])
	assert.Equal(t, "2026-09-06T16:15:02Z", first["occurred_at"])

	report := out["report"].(map[string]any)
	assert.Equal(t, float64(435_000_000), report["repair_cost_minor"])
	assert.Equal(t, float64(8), report["photos"])
	assert.Equal(t, report, steps[2].(map[string]any)["report"])
}

func TestAClosedCaseShowsItsClosing(t *testing.T) {
	v := inProgress()
	v.State, v.Next, v.Missing, v.Report = domain.InterventionClosed, "", []domain.InterventionStep{}, nil
	v.Closure = &domain.Closure{ClaimID: claimID, Result: domain.ResultRepaired, ClosedAt: searchedAt.Add(48 * time.Hour),
		ClosedBy: andres, RecordedAt: searchedAt.Add(49 * time.Hour)}
	status, out, _ := send(interventionRouter(&fakeInterventions{view: v}), http.MethodGet,
		"/v1/claims/"+claimID+"/intervention", "operations", "")

	require.Equal(t, http.StatusOK, status)
	assert.Nil(t, out["next_step"])
	assert.Equal(t, []any{}, out["missing"])
	closure := out["closure"].(map[string]any)
	assert.Equal(t, "repaired", closure["result"])
	assert.Nil(t, closure["notes"])
	assert.Equal(t, "andres.ruiz@solventa.example", closure["closed_by"].(map[string]any)["email"])
}

func TestTheAnalystRecordsAStepWithTheReportOfTheVisit(t *testing.T) {
	f := &fakeInterventions{view: inProgress()}
	status, out, _ := send(interventionRouter(f), http.MethodPost, "/v1/claims/"+claimID+"/intervention/steps",
		"operations", `{"step": "visit_done", "occurred_at": "2026-09-15T14:30:00Z", "note": "informe enviado",
		"report": {"cause": "Filtración", "damage": "Techo", "repair_cost_minor": 435000000, "repair_days": 2,
		"photos": 8}}`)

	require.Equal(t, http.StatusCreated, status)
	assert.Equal(t, "in_progress", out["state"])
	assert.Equal(t, andres, f.gotBy)
	assert.Equal(t, domain.StepVisitDone, f.gotStep.Step)
	assert.Equal(t, time.Date(2026, 9, 15, 14, 30, 0, 0, time.UTC), f.gotStep.OccurredAt.UTC())
	assert.Equal(t, "informe enviado", f.gotStep.Note)
	require.NotNil(t, f.gotStep.Report)
	assert.Equal(t, int64(435_000_000), *f.gotStep.Report.RepairCostMinor)
	assert.Equal(t, 8, *f.gotStep.Report.Photos)
}

func TestTheAnalystClosesTheCase(t *testing.T) {
	f := &fakeInterventions{view: inProgress()}
	status, _, _ := send(interventionRouter(f), http.MethodPost, "/v1/claims/"+claimID+"/closure", "operations",
		`{"result": "repaired", "closed_at": "2026-09-18T17:00:00-05:00", "notes": "Asegurada conforme"}`)

	require.Equal(t, http.StatusCreated, status)
	assert.Equal(t, andres, f.gotBy)
	assert.Equal(t, domain.ResultRepaired, f.gotClose.Result)
	assert.Equal(t, time.Date(2026, 9, 18, 22, 0, 0, 0, time.UTC), f.gotClose.ClosedAt.UTC())
	assert.Equal(t, "Asegurada conforme", f.gotClose.Notes)
}

func TestAClosingBeforeTheEndSaysWhatIsMissing(t *testing.T) {
	f := &fakeInterventions{err: &domain.NotFinishedError{Missing: []domain.InterventionStep{
		domain.StepRepairAuthorized, domain.StepRepairExecuted}}}
	status, out, _ := send(interventionRouter(f), http.MethodPost, "/v1/claims/"+claimID+"/closure", "operations",
		`{"result": "repaired", "closed_at": "2026-09-18T17:00:00Z"}`)

	assert.Equal(t, http.StatusConflict, status)
	assert.Equal(t, "intervention_not_finished", out["error"])
	assert.Equal(t, []any{"repair_authorized", "repair_executed"}, out["missing"])

	// A closing that lost the race to the store names the last step.
	f = &fakeInterventions{err: domain.ErrInterventionNotFinished}
	_, out, _ = send(interventionRouter(f), http.MethodPost, "/v1/claims/"+claimID+"/closure", "operations",
		`{"result": "repaired", "closed_at": "2026-09-18T17:00:00Z"}`)
	assert.Equal(t, []any{"repair_executed"}, out["missing"])
}

func TestEveryErrorOfTheInterventionHasItsCode(t *testing.T) {
	cases := []struct {
		err    error
		status int
		code   string
	}{
		{ports.ErrClaimNotFound, http.StatusNotFound, "claim_not_found"},
		{domain.ErrNotAssigned, http.StatusConflict, "not_assigned"},
		{domain.ErrCaseClosed, http.StatusConflict, "case_closed"},
		{domain.ErrStepOutOfOrder, http.StatusConflict, "step_out_of_order"},
		{&domain.ValidationError{Fields: []domain.FieldError{{Field: "result", Code: domain.CodeRequired}}},
			http.StatusUnprocessableEntity, "validation_failed"},
		{errors.New("connection refused"), http.StatusServiceUnavailable, "claims_unavailable"},
	}
	requests := []struct{ method, path, body string }{
		{http.MethodGet, "/intervention", ""},
		{http.MethodPost, "/intervention/steps", `{"step": "accepted", "occurred_at": "2026-09-15T14:30:00Z"}`},
		{http.MethodPost, "/closure", `{"result": "repaired", "closed_at": "2026-09-18T17:00:00Z"}`},
	}
	for _, c := range cases {
		for _, r := range requests {
			f := &fakeInterventions{err: c.err}
			status, out, _ := send(interventionRouter(f), r.method, "/v1/claims/"+claimID+r.path, "operations", r.body)
			assert.Equal(t, c.status, status, "%s %s: %v", r.method, r.path, c.err)
			assert.Equal(t, c.code, out["error"], "%s %s: %v", r.method, r.path, c.err)
		}
	}
}

func TestOnlyOperationsFollowsTheIntervention(t *testing.T) {
	for _, caller := range []string{"insured", "advisor", "partner"} {
		f := &fakeInterventions{view: inProgress()}
		for _, r := range []struct{ method, path string }{{http.MethodGet, "/intervention"},
			{http.MethodPost, "/intervention/steps"}, {http.MethodPost, "/closure"}} {
			status, _, _ := send(interventionRouter(f), r.method, "/v1/claims/"+claimID+r.path, caller, `{}`)
			assert.Equal(t, http.StatusForbidden, status, "%s %s as %s", r.method, r.path, caller)
		}
		assert.Zero(t, f.calls, caller)
	}
	status, _, _ := send(interventionRouter(&fakeInterventions{}), http.MethodGet,
		"/v1/claims/"+claimID+"/intervention", "", "")
	assert.Equal(t, http.StatusUnauthorized, status)
}

func TestABodyOutsideTheContractNeverReachesTheService(t *testing.T) {
	for path, body := range map[string]string{
		"/intervention/steps": `{"step": "accepted", "occurred_at": "ayer"}`,
		"/closure":            `{"result": "repaired", "approved": true}`,
		"/v1/../closure":      `{}`,
	} {
		f := &fakeInterventions{}
		status, _, _ := send(interventionRouter(f), http.MethodPost, "/v1/claims/"+claimID+path, "operations", body)
		assert.Contains(t, []int{http.StatusBadRequest, http.StatusNotFound}, status, path)
		assert.Zero(t, f.calls, path)
	}
	f := &fakeInterventions{}
	status, _, _ := send(interventionRouter(f), http.MethodGet, "/v1/claims/not-a-claim/intervention", "operations", "")
	assert.Equal(t, http.StatusNotFound, status)
	assert.Zero(t, f.calls)
}
