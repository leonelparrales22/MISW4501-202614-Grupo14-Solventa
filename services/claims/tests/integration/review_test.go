package integration_test

import (
	"net/http"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	httpadapter "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/adapters/http"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/adapters/postgres"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/evaluation"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/review"
)

// withReviews mounts the review of the advisor and the history of a claim
// (HU-W2.1-03) over the evaluation the service already reads with, and the
// resubmission of a returned filing (HU-W2.1-04).
func withReviews(records *postgres.ClaimRecordStore, reader *evaluation.Service) httpadapter.Option {
	store := postgres.ReviewStoreOf(records)
	svc := review.NewService(records, store, store, reader, clock{}, nil).
		WithResubmissions(postgres.ResubmissionStoreOf(records), postgres.SupportStoreOf(records)).
		WithOutcome(postgres.DecisionStoreOf(records), postgres.AssignmentStoreOf(records))
	reviews, resubmission := httpadapter.WithReviews(svc, verifier), httpadapter.WithResubmission(svc, verifier)
	return func(r chi.Router) {
		reviews(r)
		resubmission(r)
	}
}

// admit is the advisor approving a filing: since HU-W2.1-03 only then does
// it enter evaluation, get its priority and reach operations.
func admit(t *testing.T, svc service, id string) string {
	t.Helper()
	rec, _ := call(t, svc, http.MethodPost, "/v1/claims/"+id+"/review-approval", "advisor", nil, nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	return id
}

func historyOf(t *testing.T, svc service, token, id string) (*int, map[string]any) {
	t.Helper()
	rec, body := call(t, svc, http.MethodGet, "/v1/claims/"+id+"/history", token, nil, nil)
	return &rec.Code, body
}

func statusesOf(body map[string]any) []string {
	var out []string
	for _, c := range body["changes"].([]any) {
		out = append(out, c.(map[string]any)["status"].(string))
	}
	return out
}

func idsOf(page map[string]any) []string {
	var out []string
	for _, it := range page["items"].([]any) {
		out = append(out, it.(map[string]any)["claim_id"].(string))
	}
	return out
}

func TestAdvisorReviewAcceptanceCriteria(t *testing.T) {
	requireDocker(t)
	core, g, m := newTermsCore(t), newGateway(t), newMailbox(t)
	svc := startWithDecisions(t, newDatabase(t), core, g, m)
	approved := fileClaim(t, svc, "form-aprueba", waterDamageBody(t, svc, "carolina", cents(4_800_000)))
	returned := fileClaim(t, svc, "form-devuelve", waterDamageBody(t, svc, "carolina", nil))

	t.Run("a filing waits for the review of the advisor and does not reach operations", func(t *testing.T) {
		assert.Equal(t, "active", statusFor(t, svc, "carolina", approved))
		rec, page := call(t, svc, http.MethodGet, "/v1/claims/evaluations", "analista", nil, nil)
		require.Equal(t, http.StatusOK, rec.Code)
		assert.Empty(t, page["items"], "nothing is in evaluation until the advisor approves it")

		rec, page = call(t, svc, http.MethodGet, "/v1/claims/reviews", "advisor", nil, nil)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		assert.Equal(t, []string{approved, returned}, idsOf(page), "oldest filed first")
		item := page["items"].([]any)[0].(map[string]any)
		assert.Equal(t, "not_applicable", item["triage"].(map[string]any)["state"], "no priority before the review")
	})

	t.Run("CA-1 the advisor sees the reported facts and the attached supports", func(t *testing.T) {
		rec, detail := call(t, svc, http.MethodGet, "/v1/claims/"+approved+"/review", "advisor", nil, nil)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		claim := detail["claim"].(map[string]any)
		assert.Equal(t, "active", claim["status"])
		assert.NotEmpty(t, claim["description"])
		assert.NotEmpty(t, claim["place"])
		assert.EqualValues(t, cents(4_800_000), claim["estimated_amount_minor"])
		assert.Len(t, detail["support_files"], 3)
		assert.NotNil(t, detail["coverage"])
		assert.Empty(t, detail["reviews"])

		// S8: one row per support of the claim type, with the files attached.
		checklist := map[string]float64{}
		for _, it := range detail["checklist"].([]any) {
			item := it.(map[string]any)
			assert.Equal(t, true, item["required"], item["requirement_code"])
			checklist[item["requirement_code"].(string)] = item["files"].(float64)
		}
		assert.Equal(t, map[string]float64{"damage_photos": 2, "repair_quote": 1}, checklist)
		// The other claim of Carolina on the same policy.
		require.Len(t, detail["prior_claims"], 1)
		assert.Equal(t, returned, detail["prior_claims"].([]any)[0].(map[string]any)["claim_id"])
	})

	t.Run("only the advisor reviews", func(t *testing.T) {
		for _, token := range []string{"carolina", "analista"} {
			rec, _ := call(t, svc, http.MethodGet, "/v1/claims/reviews", token, nil, nil)
			assert.Equal(t, http.StatusForbidden, rec.Code, token)
			rec, _ = call(t, svc, http.MethodPost, "/v1/claims/"+approved+"/review-approval", token, nil, nil)
			assert.Equal(t, http.StatusForbidden, rec.Code, token)
			rec, _ = postJSON(t, svc, "/v1/claims/"+approved+"/review-return", token,
				map[string]any{"reason_code": "other", "message": "x", "facts_to_correct": []string{"place"}})
			assert.Equal(t, http.StatusForbidden, rec.Code, token)
		}
		assert.Equal(t, "active", statusFor(t, svc, "carolina", approved))
	})

	t.Run("CA-2 an approved filing is in evaluation and enters the triage", func(t *testing.T) {
		rec, body := call(t, svc, http.MethodPost, "/v1/claims/"+approved+"/review-approval", "advisor", nil, nil)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		assert.Equal(t, "in_evaluation", body["status"])
		r := body["review"].(map[string]any)
		assert.Equal(t, "approved", r["outcome"])
		assert.EqualValues(t, 1, r["seq"])
		assert.Nil(t, r["reason_code"])
		assert.Nil(t, r["message"])
		assert.Equal(t, "sub-advisor", r["reviewer_name"], "the advisor of the test has no name: their id")
		for _, a := range r["supports"].([]any) {
			assert.Equal(t, "valid", a.(map[string]any)["verdict"], "an approval has every support valid")
		}
		assert.Equal(t, "in_evaluation", statusFor(t, svc, "carolina", approved))

		rec, page := call(t, svc, http.MethodGet, "/v1/claims/evaluations", "analista", nil, nil)
		require.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, []string{approved}, idsOf(page))
		require.Eventually(t, func() bool {
			_, detail := call(t, svc, http.MethodGet, "/v1/claims/"+approved+"/evaluation", "analista", nil, nil)
			return detail["triage"].(map[string]any)["state"] == "assessed"
		}, 60*time.Second, 50*time.Millisecond, "the triage gives it its level")

		rec, _ = call(t, svc, http.MethodPost, "/v1/claims/"+approved+"/review-approval", "advisor", nil, nil)
		assert.Equal(t, http.StatusConflict, rec.Code, "a reviewed filing is not reviewed again")
	})

	t.Run("CA-3 a return needs the reason, the message and what is missing", func(t *testing.T) {
		illegible := []map[string]any{{"requirement_code": "repair_quote", "verdict": "illegible"}}
		for name, tc := range map[string]struct {
			body   map[string]any
			status int
			code   string
		}{
			"no reason": {map[string]any{"message": "x", "supports": illegible}, http.StatusBadRequest, "invalid_request"},
			"reason of no list": {map[string]any{"reason_code": "porque", "message": "x", "supports": illegible},
				http.StatusBadRequest, "invalid_request"},
			"blank message": {map[string]any{"reason_code": "other", "message": "  ", "supports": illegible},
				http.StatusUnprocessableEntity, "message_required"},
			"nothing requested": {map[string]any{"reason_code": "other", "message": "Falta información"},
				http.StatusUnprocessableEntity, "nothing_requested"},
			"support of no type": {map[string]any{"reason_code": "other", "message": "x",
				"supports": []map[string]any{{"requirement_code": "police_report", "verdict": "missing"}}},
				http.StatusUnprocessableEntity, "support_not_applicable"},
			"attached marked missing": {map[string]any{"reason_code": "other", "message": "x",
				"supports": []map[string]any{{"requirement_code": "repair_quote", "verdict": "missing"}}},
				http.StatusUnprocessableEntity, "verdict_not_applicable"},
			"unknown fact": {map[string]any{"reason_code": "other", "message": "x", "facts_to_correct": []string{"policy_id"}},
				http.StatusBadRequest, "invalid_request"},
			"unknown field": {map[string]any{"reason_code": "other", "message": "x", "supports": illegible, "extra": true},
				http.StatusBadRequest, "invalid_request"},
		} {
			rec, body := postJSON(t, svc, "/v1/claims/"+returned+"/review-return", "advisor", tc.body)
			assert.Equal(t, tc.status, rec.Code, name)
			assert.Equal(t, tc.code, body["error"], name)
		}
		assert.Equal(t, "active", statusFor(t, svc, "carolina", returned), "nothing was returned")
	})

	t.Run("CA-3 a returned filing records the reason and the verdict of each support", func(t *testing.T) {
		rec, body := postJSON(t, svc, "/v1/claims/"+returned+"/review-return", "advisor", map[string]any{
			"reason_code": "illegible_or_incomplete_supports",
			"message":     "La factura de la reparación no es legible.",
			"supports": []map[string]any{{"requirement_code": "repair_quote", "verdict": "illegible",
				"note": "borrosa, no se lee el valor"}},
			"facts_to_correct": []string{"estimated_amount"},
		})
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		assert.Equal(t, "returned", body["status"])
		r := body["review"].(map[string]any)
		assert.Equal(t, "returned", r["outcome"])
		assert.Equal(t, "illegible_or_incomplete_supports", r["reason_code"])
		assert.Equal(t, "La factura de la reparación no es legible.", r["message"])
		assert.Equal(t, []any{
			map[string]any{"requirement_code": "damage_photos", "label": "Fotos del daño", "verdict": "valid", "note": nil},
			map[string]any{"requirement_code": "repair_quote", "label": "Factura o cotización de la reparación",
				"verdict": "illegible", "note": "borrosa, no se lee el valor"},
		}, r["supports"], "what the advisor does not mention is valid")
		assert.Equal(t, []any{"estimated_amount"}, r["facts_to_correct"])

		assert.Equal(t, "returned", statusFor(t, svc, "carolina", returned), "the insured sees it returned")
		_, page := call(t, svc, http.MethodGet, "/v1/claims/evaluations", "analista", nil, nil)
		assert.NotContains(t, idsOf(page), returned, "a returned filing is not evaluated")
		_, page = call(t, svc, http.MethodGet, "/v1/claims/reviews", "advisor", nil, nil)
		assert.NotContains(t, idsOf(page), returned, "it waits for the insured, not the advisor")
		_, detail := call(t, svc, http.MethodGet, "/v1/claims/"+returned+"/review", "advisor", nil, nil)
		assert.Len(t, detail["reviews"], 1)
	})

	t.Run("the history keeps every change of status and who can read it", func(t *testing.T) {
		code, body := historyOf(t, svc, "carolina", returned)
		require.Equal(t, http.StatusOK, *code, body)
		assert.Equal(t, []string{"active", "returned"}, statusesOf(body))
		assert.Equal(t, "returned", body["status"])
		last := body["changes"].([]any)[1].(map[string]any)
		assert.Equal(t, last["changed_at"], body["last_changed_at"])
		assert.Equal(t, "active", last["previous_status"])
		require.NotNil(t, body["last_return"])
		assert.Equal(t, "La factura de la reparación no es legible.", body["last_return"].(map[string]any)["message"])
		assert.Len(t, body["reviews"], 1)

		_, body = historyOf(t, svc, "analista", approved)
		assert.Equal(t, []string{"active", "in_evaluation"}, statusesOf(body))
		assert.Nil(t, body["last_return"])
		require.Len(t, body["reviews"], 1, "who approved it (screen A8)")
		assert.Equal(t, "approved", body["reviews"].([]any)[0].(map[string]any)["outcome"])
		code, _ = historyOf(t, svc, "advisor", approved)
		assert.Equal(t, http.StatusOK, *code)

		code, _ = historyOf(t, svc, "andres", approved)
		assert.Equal(t, http.StatusNotFound, *code, "another insured does not learn the claim exists")
		code, _ = historyOf(t, svc, "carolina", "8f14e45f-ceea-467a-9f3a-5a2d0b1c7e90")
		assert.Equal(t, http.StatusNotFound, *code)
	})

	t.Run("the decision and the payout are in the history too", func(t *testing.T) {
		rec, _ := postJSON(t, svc, "/v1/claims/"+approved+"/approval", "analista",
			map[string]any{"gross_amount_minor": cents(4_350_000)})
		require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
		waitForStatus(t, svc, "carolina", approved, "paid")
		_, body := historyOf(t, svc, "carolina", approved)
		assert.Equal(t, []string{"active", "in_evaluation", "approved", "paid"}, statusesOf(body))
	})

	t.Run("the reviews and the history are append-only", func(t *testing.T) {
		for _, stmt := range []string{`UPDATE claim_review SET message = 'x'`, `DELETE FROM claim_review`,
			`TRUNCATE claim_review`, `UPDATE claim_status_change SET status = 'paid'`,
			`DELETE FROM claim_status_change`, `TRUNCATE claim_status_change`} {
			_, err := svc.db.Exec(stmt)
			assert.ErrorContains(t, err, "append-only", stmt)
		}
	})
}
