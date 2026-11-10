package integration_test

import (
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// HU-W2.1-04: the insured corrects a returned filing and sends it again, over
// PostgreSQL and through the routes of the portal.

func returnTo(t *testing.T, svc service, id string, body map[string]any) {
	t.Helper()
	rec, _ := postJSON(t, svc, "/v1/claims/"+id+"/review-return", "advisor", body)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
}

func resubmit(t *testing.T, svc service, token, id string, body map[string]any) (int, map[string]any) {
	t.Helper()
	rec, out := postJSON(t, svc, "/v1/claims/"+id+"/resubmission", token, body)
	return rec.Code, out
}

// supportIDsOf are the ids of the files of a requirement, as the insured
// reads the claim.
func supportIDsOf(t *testing.T, svc service, id, code string) []string {
	t.Helper()
	rec, body := call(t, svc, http.MethodGet, "/v1/claims/"+id, "carolina", nil, nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var out []string
	for _, it := range body["supports"].(map[string]any)["items"].([]any) {
		item := it.(map[string]any)
		if item["requirement_code"] != code {
			continue
		}
		for _, f := range item["files"].([]any) {
			out = append(out, f.(map[string]any)["support_id"].(string))
		}
	}
	return out
}

func fieldCodes(body map[string]any) map[string]string {
	out := map[string]string{}
	for _, f := range body["fields"].([]any) {
		field := f.(map[string]any)
		out[field["field"].(string)] = field["code"].(string)
	}
	return out
}

func TestResubmissionAcceptanceCriteria(t *testing.T) {
	requireDocker(t)
	core, g, m := newTermsCore(t), newGateway(t), newMailbox(t)
	svc := startWithDecisions(t, newDatabase(t), core, g, m)
	id := fileClaim(t, svc, "form-reenvio", waterDamageBody(t, svc, "carolina", cents(4_800_000)))
	quote := supportIDsOf(t, svc, id, "repair_quote")
	photos := supportIDsOf(t, svc, id, "damage_photos")
	returnTo(t, svc, id, map[string]any{"reason_code": "illegible_or_incomplete_supports",
		"message":          "La cotización no se lee. Corrige también el lugar.",
		"supports":         []map[string]any{{"requirement_code": "repair_quote", "verdict": "illegible"}},
		"facts_to_correct": []string{"place"}})

	t.Run("CA-1 the insured reads the reason, what was filed and what is asked", func(t *testing.T) {
		code, body := historyOf(t, svc, "carolina", id)
		require.Equal(t, http.StatusOK, *code)
		assert.Equal(t, "returned", body["status"])
		last := body["last_return"].(map[string]any)
		assert.Equal(t, "La cotización no se lee. Corrige también el lugar.", last["message"])
		assert.Equal(t, []any{"place"}, last["facts_to_correct"])
		assert.Empty(t, body["resubmissions"])
		require.Len(t, body["requirements"], 2, "the supports of a water damage, with their rules")
		photosRule := body["requirements"].([]any)[0].(map[string]any)
		assert.Equal(t, "damage_photos", photosRule["code"])
		assert.EqualValues(t, 2, photosRule["min_files"])
		assert.Equal(t, []any{"jpeg", "png"}, photosRule["formats"])
		assert.Equal(t, quote, supportIDsOf(t, svc, id, "repair_quote"), "the files filed are still there")
	})

	t.Run("CA-3 a resubmission names everything still missing", func(t *testing.T) {
		code, body := resubmit(t, svc, "carolina", id, map[string]any{"review_seq": 1, "place": " "})
		require.Equal(t, http.StatusUnprocessableEntity, code)
		assert.Equal(t, map[string]string{"place": "required", "supports.repair_quote": "not_replaced"}, fieldCodes(body))
		assert.Equal(t, "returned", statusFor(t, svc, "carolina", id), "nothing changed")
	})

	t.Run("CA-5 only the insured who filed it sends it again", func(t *testing.T) {
		body := map[string]any{"review_seq": 1}
		code, _ := resubmit(t, svc, "andres", id, body)
		assert.Equal(t, http.StatusNotFound, code, "another insured's claim does not exist for them")
		for _, token := range []string{"advisor", "analista"} {
			code, _ = resubmit(t, svc, token, id, body)
			assert.Equal(t, http.StatusForbidden, code, token)
		}
		code, _ = resubmit(t, svc, "", id, body)
		assert.Equal(t, http.StatusUnauthorized, code)
	})

	newQuote := uploadIDs(t, svc, "carolina", pdfFile, 1)
	var resubmitted map[string]any

	t.Run("CA-2 and CA-4 the correction keeps the rest and the claim is active with the same number", func(t *testing.T) {
		var number string
		_, before := call(t, svc, http.MethodGet, "/v1/claims/"+id, "carolina", nil, nil)
		number = before["filing_number"].(string)

		code, body := resubmit(t, svc, "carolina", id, map[string]any{"review_seq": 1,
			"place":    "Carrera 15 # 93-40, apto 402, torre 2, Bogotá",
			"supports": []map[string]any{{"requirement_code": "repair_quote", "support_ids": newQuote}}})

		require.Equal(t, http.StatusOK, code, body)
		resubmitted = body
		assert.Equal(t, "active", body["status"])
		assert.Equal(t, number, body["filing_number"])
		_, after := call(t, svc, http.MethodGet, "/v1/claims/"+id, "carolina", nil, nil)
		assert.Equal(t, number, after["filing_number"], "the same filing number")
		assert.Equal(t, "Carrera 15 # 93-40, apto 402, torre 2, Bogotá", after["place"])
		assert.Equal(t, before["description"], after["description"], "what did not change is kept")
		assert.EqualValues(t, cents(4_800_000), after["estimated_amount_minor"])
		assert.Equal(t, photos, supportIDsOf(t, svc, id, "damage_photos"), "the photos are kept")
		assert.Equal(t, newQuote, supportIDsOf(t, svc, id, "repair_quote"), "the quote is replaced")
	})

	t.Run("CA-5 the resubmission is recorded with who, when and what changed", func(t *testing.T) {
		r := resubmitted["resubmission"].(map[string]any)
		assert.Equal(t, "sub-carolina", r["resubmitted_by"])
		at, err := time.Parse(time.RFC3339Nano, r["resubmitted_at"].(string))
		require.NoError(t, err)
		assert.WithinDuration(t, time.Now(), at, time.Minute)
		assert.Equal(t, []any{map[string]any{"fact": "place", "before": "Carrera 15 # 93-40, apto 402, Bogotá",
			"after": "Carrera 15 # 93-40, apto 402, torre 2, Bogotá"}}, r["changed_facts"])
		assert.Equal(t, []any{map[string]any{"requirement_code": "repair_quote",
			"label": "Factura o cotización de la reparación", "added": []any{newQuote[0]}, "withdrawn": []any{quote[0]}}},
			r["changed_supports"])

		code, body := historyOf(t, svc, "carolina", id)
		require.Equal(t, http.StatusOK, *code)
		assert.Equal(t, []string{"active", "returned", "active"}, statusesOf(body))
		require.Len(t, body["resubmissions"], 1)
		assert.Nil(t, body["last_return"], "it is not returned any more")
		code, body = historyOf(t, svc, "advisor", id)
		require.Equal(t, http.StatusOK, *code)
		assert.Len(t, body["resubmissions"], 1, "the advisor sees what changed")

		var rows int
		require.NoError(t, svc.db.QueryRow(`SELECT count(*) FROM claim_support_withdrawal WHERE claim_id = $1`, id).Scan(&rows))
		assert.Equal(t, 1, rows)
		for _, stmt := range []string{`UPDATE claim_resubmission SET resubmitted_by = 'x'`, `DELETE FROM claim_resubmission`,
			`DELETE FROM claim_support_withdrawal`, `TRUNCATE claim_support_withdrawal`} {
			_, err := svc.db.Exec(stmt)
			assert.ErrorContains(t, err, "append-only", stmt)
		}
	})

	t.Run("a retry answers with the same resubmission and nothing changes twice", func(t *testing.T) {
		code, body := resubmit(t, svc, "carolina", id, map[string]any{"review_seq": 1,
			"supports": []map[string]any{{"requirement_code": "repair_quote", "support_ids": newQuote}}})
		require.Equal(t, http.StatusOK, code, body)
		assert.Equal(t, resubmitted["resubmission"], body["resubmission"])
		code, body = resubmit(t, svc, "carolina", id, map[string]any{"review_seq": 2})
		assert.Equal(t, http.StatusConflict, code)
		assert.Equal(t, "claim_not_returned", body["error"])
	})

	t.Run("the advisor reviews it again with the new supports", func(t *testing.T) {
		rec, detail := call(t, svc, http.MethodGet, "/v1/claims/"+id+"/review", "advisor", nil, nil)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		assert.Len(t, detail["support_files"], 3, "two photos and the new quote")
		admit(t, svc, id)
		assert.Equal(t, "in_evaluation", statusFor(t, svc, "carolina", id))
		_, body := historyOf(t, svc, "carolina", id)
		require.Len(t, body["reviews"], 2)
		assert.EqualValues(t, 2, body["reviews"].([]any)[1].(map[string]any)["seq"])
	})

	t.Run("a correction of an old return is refused", func(t *testing.T) {
		other := fileClaim(t, svc, "form-reenvio-2", waterDamageBody(t, svc, "carolina", nil))
		returnTo(t, svc, other, map[string]any{"reason_code": "missing_information", "message": "Falta el monto.",
			"facts_to_correct": []string{"estimated_amount"}})
		code, body := resubmit(t, svc, "carolina", other, map[string]any{"review_seq": 1})
		require.Equal(t, http.StatusUnprocessableEntity, code)
		assert.Equal(t, map[string]string{"estimated_amount_minor": "required"}, fieldCodes(body))

		code, body = resubmit(t, svc, "carolina", other, map[string]any{"review_seq": 1, "estimated_amount_minor": cents(900_000)})
		require.Equal(t, http.StatusOK, code, body)
		returnTo(t, svc, other, map[string]any{"reason_code": "other", "message": "Revisa el lugar.",
			"facts_to_correct": []string{"place"}})

		code, body = resubmit(t, svc, "carolina", other, map[string]any{"review_seq": 1})
		assert.Equal(t, http.StatusConflict, code)
		assert.Equal(t, "stale_return", body["error"])
		code, _ = resubmit(t, svc, "carolina", other, map[string]any{"review_seq": 3})
		assert.Equal(t, http.StatusConflict, code)
		code, body = resubmit(t, svc, "carolina", other, map[string]any{"review_seq": 2, "place": "Calle 93 # 15-40, Bogotá"})
		require.Equal(t, http.StatusOK, code, body)
		assert.EqualValues(t, 2, body["resubmission"].(map[string]any)["seq"])
	})

	t.Run("a body that is not the contract is a bad request", func(t *testing.T) {
		for _, body := range []map[string]any{{}, {"review_seq": 0}, {"review_seq": 1, "status": "active"}} {
			code, _ := resubmit(t, svc, "carolina", id, body)
			assert.Equal(t, http.StatusBadRequest, code, body)
		}
	})
}
