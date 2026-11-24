package integration_test

import (
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// queueOf reads the queue by priority as operations and waits until the
// triage has assessed every claim of it.
func queueOf(t *testing.T, svc service, query string, assessed int) map[string]any {
	t.Helper()
	var body map[string]any
	require.Eventually(t, func() bool {
		rec, b := call(t, svc, http.MethodGet, "/v1/claims/evaluations"+query, "analista", nil, nil)
		if rec.Code != http.StatusOK {
			return false
		}
		body = b
		n := 0
		for _, it := range b["items"].([]any) {
			if it.(map[string]any)["triage"].(map[string]any)["state"] == "assessed" {
				n++
			}
		}
		return n == assessed
	}, 60*time.Second, 50*time.Millisecond, "the triage never assessed the queue")
	return body
}

func filingNumbers(body map[string]any) []string {
	var out []string
	for _, it := range body["items"].([]any) {
		out = append(out, it.(map[string]any)["filing_number"].(string))
	}
	return out
}

func TestPriorityQueueAcceptanceCriteria(t *testing.T) {
	requireDocker(t)
	core, g, m := newTermsCore(t), newGateway(t), newMailbox(t)
	svc := startWithDecisions(t, newDatabase(t), core, g, m)

	t.Run("CA-4 an empty queue answers without errors and without made-up indicators", func(t *testing.T) {
		rec, body := call(t, svc, http.MethodGet, "/v1/claims/evaluations", "analista", nil, nil)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		assert.Empty(t, body["items"])
		summary := body["summary"].(map[string]any)
		assert.EqualValues(t, 0, summary["total"])
		assert.Nil(t, summary["mean_resolution_days"])
		for _, l := range summary["levels"].([]any) {
			assert.EqualValues(t, 0, l.(map[string]any)["claims"])
		}
	})

	// Filed in this order: the Triage 2 first, so the order of the queue is
	// not the order of the filing.
	t2old := admit(t, svc, fileClaim(t, svc, "q-t2-old", waterDamageBody(t, svc, "carolina", cents(4_800_000))))
	t3 := admit(t, svc, fileClaim(t, svc, "q-t3", waterDamageBody(t, svc, "carolina", cents(900_000))))
	_, err := svc.db.Exec(`UPDATE claim SET incident_type = 'glass' WHERE id = $1`, t3)
	require.NoError(t, err)
	t1 := admit(t, svc, fileClaim(t, svc, "q-t1", waterDamageBody(t, svc, "carolina", cents(40_000_000))))
	t2new := admit(t, svc, fileClaim(t, svc, "q-t2-new", waterDamageBody(t, svc, "carolina", cents(4_800_000))))
	fileClaim(t, svc, "q-waiting", waterDamageBody(t, svc, "carolina", cents(40_000_000))) // not approved by the advisor
	numbers := map[string]string{}

	t.Run("CA-1 and CA-2 the queue goes by Triage 1, 2 and 3 and in each by the time waited", func(t *testing.T) {
		body := queueOf(t, svc, "", 4)
		items := body["items"].([]any)
		require.Len(t, items, 4, "the filing the advisor has not approved is not in the queue")
		var ids []string
		for _, it := range items {
			item := it.(map[string]any)
			ids = append(ids, item["claim_id"].(string))
			numbers[item["claim_id"].(string)] = item["filing_number"].(string)
			assert.Equal(t, "in_evaluation", item["status"])
			assert.NotEmpty(t, item["filed_at"], "the date and time of the filing")
			assert.Contains(t, item, "estimated_amount_minor")
			assert.Equal(t, "Carolina Pérez Ruiz", item["holder"].(map[string]any)["full_name"], "the insured, from the policy core")
		}
		assert.Equal(t, []string{t1, t2old, t2new, t3}, ids)
		levels := []float64{}
		for _, it := range items {
			levels = append(levels, it.(map[string]any)["triage"].(map[string]any)["level"].(float64))
		}
		assert.Equal(t, []float64{1, 2, 2, 3}, levels)

		summary := body["summary"].(map[string]any)
		assert.EqualValues(t, 4, summary["total"])
		assert.EqualValues(t, 4, summary["matching"])
		assert.Equal(t, []any{
			map[string]any{"level": 1.0, "claims": 1.0}, map[string]any{"level": 2.0, "claims": 2.0},
			map[string]any{"level": 3.0, "claims": 1.0}}, summary["levels"])
	})

	t.Run("the filters of O5 narrow the list, not the indicators", func(t *testing.T) {
		body := queueOf(t, svc, "?level=2", 2)
		assert.Equal(t, []string{numbers[t2old], numbers[t2new]}, filingNumbers(body))
		assert.EqualValues(t, 2, body["summary"].(map[string]any)["matching"])
		assert.EqualValues(t, 4, body["summary"].(map[string]any)["total"])

		body = queueOf(t, svc, "?line=home&status=in_evaluation", 3)
		assert.Len(t, body["items"], 3, "water damage is home; glass is auto")

		rec, _ := call(t, svc, http.MethodGet, "/v1/claims/evaluations?level=7", "analista", nil, nil)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})

	t.Run("the queue is paged in its order", func(t *testing.T) {
		rec, first := call(t, svc, http.MethodGet, "/v1/claims/evaluations?page_size=3", "analista", nil, nil)
		require.Equal(t, http.StatusOK, rec.Code)
		token := first["next_page_token"].(string)
		rec, second := call(t, svc, http.MethodGet, "/v1/claims/evaluations?page_size=3&page_token="+token, "analista", nil, nil)
		require.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, []string{numbers[t1], numbers[t2old], numbers[t2new]}, filingNumbers(first))
		assert.Equal(t, []string{numbers[t3]}, filingNumbers(second))
		assert.Nil(t, second["next_page_token"])
	})

	t.Run("CA-3 a new level moves the claim in the next read, without reordering by hand", func(t *testing.T) {
		_, err := svc.db.Exec(`UPDATE claim SET incident_type = 'water_damage', estimated_amount_minor = $1 WHERE id = $2`,
			cents(40_000_000), t3)
		require.NoError(t, err)
		require.Eventually(t, func() bool {
			_, body := call(t, svc, http.MethodGet, "/v1/claims/evaluations", "analista", nil, nil)
			got := filingNumbers(body)
			// Both are Triage 1 now, and t3 was filed before t1: it waits longer.
			return len(got) == 4 && got[0] == numbers[t3] && got[1] == numbers[t1]
		}, 60*time.Second, 50*time.Millisecond, "the claim recalculated as Triage 1 never moved up")
	})

	t.Run("the mean time of resolution counts the decided claims", func(t *testing.T) {
		rec, _ := postJSON(t, svc, "/v1/claims/"+t2new+"/rejection", "analista", map[string]any{"reason": "No procede."})
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		_, body := call(t, svc, http.MethodGet, "/v1/claims/evaluations", "analista", nil, nil)
		summary := body["summary"].(map[string]any)
		assert.EqualValues(t, 1, summary["decided_in_window"])
		assert.NotNil(t, summary["mean_resolution_days"])
		assert.EqualValues(t, 30, summary["window_days"])
		assert.NotContains(t, filingNumbers(body), numbers[t2new], "a decided claim leaves the queue")
	})

	t.Run("CA-5 only operations reads the queue", func(t *testing.T) {
		for _, token := range []string{"carolina", "advisor"} {
			rec, _ := call(t, svc, http.MethodGet, "/v1/claims/evaluations", token, nil, nil)
			assert.Equal(t, http.StatusForbidden, rec.Code, token)
		}
		rec, _ := call(t, svc, http.MethodGet, "/v1/claims/evaluations", "", nil, nil)
		assert.Equal(t, http.StatusUnauthorized, rec.Code)
	})
}
