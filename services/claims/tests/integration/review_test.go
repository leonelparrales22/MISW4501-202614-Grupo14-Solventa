package integration_test

import (
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	httpadapter "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/adapters/http"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/adapters/postgres"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/evaluation"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/review"
)

// withReviews mounts the review of the advisor and the history of a claim
// (HU-W2.1-03) over the evaluation the service already reads with.
func withReviews(records *postgres.ClaimRecordStore, reader *evaluation.Service) httpadapter.Option {
	store := postgres.ReviewStoreOf(records)
	return httpadapter.WithReviews(review.NewService(records, store, store, reader, clock{}, nil), verifier)
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
	})

	t.Run("only the advisor reviews", func(t *testing.T) {
		for _, token := range []string{"carolina", "analista"} {
			rec, _ := call(t, svc, http.MethodGet, "/v1/claims/reviews", token, nil, nil)
			assert.Equal(t, http.StatusForbidden, rec.Code, token)
			rec, _ = call(t, svc, http.MethodPost, "/v1/claims/"+approved+"/review-approval", token, nil, nil)
			assert.Equal(t, http.StatusForbidden, rec.Code, token)
			rec, _ = postJSON(t, svc, "/v1/claims/"+approved+"/review-return", token,
				map[string]any{"reason": "x", "facts_to_correct": []string{"place"}})
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
		assert.Nil(t, r["reason"])
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

	t.Run("CA-3 a return needs the reason and what is missing", func(t *testing.T) {
		for name, tc := range map[string]struct {
			body   map[string]any
			status int
			code   string
		}{
			"no reason":          {map[string]any{"facts_to_correct": []string{"place"}}, http.StatusBadRequest, "invalid_request"},
			"blank reason":       {map[string]any{"reason": "  ", "facts_to_correct": []string{"place"}}, http.StatusUnprocessableEntity, "reason_required"},
			"nothing requested":  {map[string]any{"reason": "Falta información"}, http.StatusUnprocessableEntity, "nothing_requested"},
			"support of no type": {map[string]any{"reason": "x", "missing_supports": []string{"police_report"}}, http.StatusUnprocessableEntity, "support_not_applicable"},
			"unknown fact":       {map[string]any{"reason": "x", "facts_to_correct": []string{"policy_id"}}, http.StatusBadRequest, "invalid_request"},
			"repeated support":   {map[string]any{"reason": "x", "missing_supports": []string{"repair_quote", "repair_quote"}}, http.StatusBadRequest, "invalid_request"},
			"unknown field":      {map[string]any{"reason": "x", "facts_to_correct": []string{"place"}, "extra": true}, http.StatusBadRequest, "invalid_request"},
		} {
			rec, body := postJSON(t, svc, "/v1/claims/"+returned+"/review-return", "advisor", tc.body)
			assert.Equal(t, tc.status, rec.Code, name)
			assert.Equal(t, tc.code, body["error"], name)
		}
		assert.Equal(t, "active", statusFor(t, svc, "carolina", returned), "nothing was returned")
	})

	t.Run("CA-3 a returned filing records the reason and the missing supports", func(t *testing.T) {
		rec, body := postJSON(t, svc, "/v1/claims/"+returned+"/review-return", "advisor", map[string]any{
			"reason":           "La factura de la reparación no es legible.",
			"missing_supports": []string{"repair_quote"},
			"facts_to_correct": []string{"estimated_amount"},
		})
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		assert.Equal(t, "returned", body["status"])
		r := body["review"].(map[string]any)
		assert.Equal(t, "returned", r["outcome"])
		assert.Equal(t, "La factura de la reparación no es legible.", r["reason"])
		assert.Equal(t, []any{"repair_quote"}, r["missing_supports"])
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
		assert.Equal(t, []any{"repair_quote"}, body["last_return"].(map[string]any)["missing_supports"])

		_, body = historyOf(t, svc, "analista", approved)
		assert.Equal(t, []string{"active", "in_evaluation"}, statusesOf(body))
		assert.Nil(t, body["last_return"])
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
		for _, stmt := range []string{`UPDATE claim_review SET reason = 'x'`, `DELETE FROM claim_review`,
			`TRUNCATE claim_review`, `UPDATE claim_status_change SET status = 'paid'`,
			`DELETE FROM claim_status_change`, `TRUNCATE claim_status_change`} {
			_, err := svc.db.Exec(stmt)
			assert.ErrorContains(t, err, "append-only", stmt)
		}
	})
}
