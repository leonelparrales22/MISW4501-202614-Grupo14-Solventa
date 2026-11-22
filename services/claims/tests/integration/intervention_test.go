package integration_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	httpadapter "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/adapters/http"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/adapters/postgres"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/assessors"
)

// Every acceptance criterion of HU-W2.3-02 (follow-up of the intervention and
// closing of the case) over HTTP: the real router and use cases over
// PostgreSQL in a container with the reference providers, from a claim the
// analyst assigned through the real assignment of HU-W2.3-01. Only the token
// verification is a double.

func startWithIntervention(t *testing.T, url string) service {
	t.Helper()
	ctx := context.Background()
	db, err := postgres.Open(ctx, url)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, postgres.Migrate(ctx, db))

	records := postgres.NewClaimRecordStore(db)
	store, providers := postgres.AssignmentStoreOf(records), postgres.ProviderStoreOf(records)
	return service{db: db, Handler: httpadapter.NewRouter(
		httpadapter.WithAssignment(assessors.NewService(store, providers, clock{}, nil),
			assessors.DefaultRetryInterval, verifier),
		httpadapter.WithProviders(assessors.NewProviderService(providers, clock{}), verifier),
		httpadapter.WithIntervention(assessors.NewInterventionService(postgres.InterventionStoreOver(store), clock{}),
			verifier),
	)}
}

// typed is a moment the analyst types, as the browser sends it.
func typed(t time.Time) string { return t.UTC().Format(time.RFC3339) }

func TestInterventionFollowUpAcceptanceCriteria(t *testing.T) {
	requireDocker(t)
	svc := startWithIntervention(t, newDatabase(t))
	id := fileFromThePhone(t, svc, "8f14e45f-ceea-467a-9f3a-5a2d0b1c7e91")
	path := "/v1/claims/" + id

	intervention := func(t *testing.T) map[string]any {
		t.Helper()
		rec, body := call(t, svc, http.MethodGet, path+"/intervention", "coordinadora", nil, nil)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		return body
	}
	step := func(t *testing.T, body map[string]any) (int, map[string]any) {
		t.Helper()
		rec, out := postJSON(t, svc, path+"/intervention/steps", "coordinadora", body)
		return rec.Code, out
	}

	t.Run("only operations follows the intervention", func(t *testing.T) {
		for _, token := range []string{"carolina", "advisor", "partner"} {
			rec, _ := call(t, svc, http.MethodGet, path+"/intervention", token, nil, nil)
			assert.Equal(t, http.StatusForbidden, rec.Code, token)
		}
	})

	t.Run("a claim without provider has no intervention yet", func(t *testing.T) {
		body := intervention(t)
		assert.Equal(t, "not_assigned", body["state"])
		code, out := step(t, map[string]any{"step": "accepted", "occurred_at": typed(time.Now())})
		assert.Equal(t, http.StatusConflict, code)
		assert.Equal(t, "not_assigned", out["error"])
	})

	searchFor(t, svc, id, map[string]any{})
	rec, _ := postJSON(t, svc, path+"/assignment", "coordinadora",
		map[string]any{"provider_id": "prv-servicios-hogar-norte", "search_seq": 1})
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())

	t.Run("CA-1 each progress is recorded and reflected at once", func(t *testing.T) {
		body := intervention(t)
		assert.Equal(t, "in_progress", body["state"])
		assert.Equal(t, "accepted", body["next_step"])
		assert.Equal(t, "Servicios Hogar Norte",
			body["assignment"].(map[string]any)["provider"].(map[string]any)["name"])

		code, out := step(t, map[string]any{"step": "accepted", "occurred_at": typed(time.Now()),
			"note": "visita programada 15/09/2026 8:00 a 12:00"})
		require.Equal(t, http.StatusCreated, code, out)
		assert.Equal(t, "en_route", out["next_step"])
		first := out["steps"].([]any)[0].(map[string]any)
		assert.Equal(t, "visita programada 15/09/2026 8:00 a 12:00", first["note"])
		assert.Equal(t, "laura.mora@solventa.example", first["recorded_by"].(map[string]any)["email"])

		code, out = step(t, map[string]any{"step": "repair_executed", "occurred_at": typed(time.Now())})
		assert.Equal(t, http.StatusConflict, code)
		assert.Equal(t, "step_out_of_order", out["error"])

		code, _ = step(t, map[string]any{"step": "en_route", "occurred_at": typed(time.Now())})
		require.Equal(t, http.StatusCreated, code)

		code, out = step(t, map[string]any{"step": "visit_done", "occurred_at": typed(time.Now())})
		assert.Equal(t, http.StatusUnprocessableEntity, code)
		assert.Equal(t, "report", out["fields"].([]any)[0].(map[string]any)["field"])

		code, out = step(t, map[string]any{"step": "visit_done", "occurred_at": typed(time.Now()),
			"report": map[string]any{"cause": "Filtración por tubería del piso superior",
				"damage": "Techo y muro de cocina · 6 m²", "repair_cost_minor": 435_000_000, "repair_days": 2,
				"photos": 8}})
		require.Equal(t, http.StatusCreated, code, out)
		assert.Equal(t, 435_000_000.0, out["report"].(map[string]any)["repair_cost_minor"])
	})

	t.Run("CA-4 the case is not closed while the intervention is not finished", func(t *testing.T) {
		rec, out := postJSON(t, svc, path+"/closure", "coordinadora",
			map[string]any{"result": "repaired", "closed_at": typed(time.Now())})
		assert.Equal(t, http.StatusConflict, rec.Code)
		assert.Equal(t, "intervention_not_finished", out["error"])
		assert.Equal(t, []any{"repair_authorized", "repair_executed"}, out["missing"])
		assert.Equal(t, false, intervention(t)["closable"])
	})

	t.Run("CA-2 the chronological history", func(t *testing.T) {
		for _, s := range []string{"repair_authorized", "repair_executed"} {
			code, out := step(t, map[string]any{"step": s, "occurred_at": typed(time.Now())})
			require.Equal(t, http.StatusCreated, code, out)
		}
		body := intervention(t)
		assert.Equal(t, "ready_to_close", body["state"])
		assert.Equal(t, true, body["closable"])
		var names []any
		for _, s := range body["steps"].([]any) {
			names = append(names, s.(map[string]any)["step"])
		}
		assert.Equal(t, []any{"accepted", "en_route", "visit_done", "repair_authorized", "repair_executed"}, names)
	})

	t.Run("CA-3 and CA-5 the case closes with its result and the analyst responsible", func(t *testing.T) {
		rec, out := postJSON(t, svc, path+"/closure", "coordinadora", map[string]any{"closed_at": typed(time.Now())})
		assert.Equal(t, http.StatusUnprocessableEntity, rec.Code)
		assert.Equal(t, "result", out["fields"].([]any)[0].(map[string]any)["field"])

		rec, out = postJSON(t, svc, path+"/closure", "coordinadora",
			map[string]any{"result": "repaired", "closed_at": typed(time.Now()), "notes": "Asegurada conforme"})
		require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
		assert.Equal(t, "closed", out["state"])
		closure := out["closure"].(map[string]any)
		assert.Equal(t, "repaired", closure["result"])
		assert.Equal(t, "Laura Mora", closure["closed_by"].(map[string]any)["name"])

		rec, _ = call(t, svc, http.MethodGet, path+"/assignment", "coordinadora", nil, nil)
		require.Equal(t, http.StatusOK, rec.Code)
		var status string
		require.NoError(t, svc.db.QueryRow(`SELECT status FROM claim WHERE id = $1`, id).Scan(&status))
		assert.Equal(t, "in_evaluation", status, "closing never approves, rejects nor pays")

		rec, provider := call(t, svc, http.MethodGet, "/v1/claims/providers/prv-servicios-hogar-norte",
			"coordinadora", nil, nil)
		require.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, "available", provider["availability"], "the provider is free for another claim")

		rec, out = postJSON(t, svc, path+"/closure", "coordinadora",
			map[string]any{"result": "repaired", "closed_at": typed(time.Now())})
		assert.Equal(t, http.StatusConflict, rec.Code)
		assert.Equal(t, "case_closed", out["error"])
	})
}
