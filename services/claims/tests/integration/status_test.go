package integration_test

import (
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// HU-W2.1-02: the insured follows the stage of their claims without a phone
// call, over PostgreSQL and through the routes of the portal (screens A7 and
// A8).

func listOf(t *testing.T, svc service, token string) map[string]any {
	t.Helper()
	rec, body := call(t, svc, http.MethodGet, "/v1/claims", token, nil, nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	return body
}

func itemOf(t *testing.T, list map[string]any, id string) map[string]any {
	t.Helper()
	for _, it := range list["items"].([]any) {
		if item := it.(map[string]any); item["claim_id"] == id {
			return item
		}
	}
	t.Fatalf("the claim %s is not in the list", id)
	return nil
}

func instant(t *testing.T, v any) time.Time {
	t.Helper()
	at, err := time.Parse(time.RFC3339Nano, v.(string))
	require.NoError(t, err)
	return at
}

func TestClaimStatusAcceptanceCriteria(t *testing.T) {
	requireDocker(t)
	core, g, m := newTermsCore(t), newGateway(t), newMailbox(t)
	svc := startWithDecisions(t, newDatabase(t), core, g, m)
	paid := fileClaim(t, svc, "form-estado-1", waterDamageBody(t, svc, "carolina", cents(4_800_000)))
	returned := fileClaim(t, svc, "form-estado-2", waterDamageBody(t, svc, "carolina", nil))

	t.Run("CA-1 the list says the stage and when it last changed, and tallies the claims", func(t *testing.T) {
		list := listOf(t, svc, "carolina")
		item := itemOf(t, list, paid)
		assert.Equal(t, "active", item["status"])
		assert.True(t, instant(t, item["last_changed_at"]).Equal(instant(t, item["filed_at"])),
			"a claim never moved last changed when it was filed")
		summary := list["summary"].(map[string]any)
		assert.EqualValues(t, 2, summary["in_progress"])
		assert.Equal(t, map[string]any{"active": 2.0}, summary["by_status"])
		assert.Empty(t, summary["pending_action"])
	})

	returnTo(t, svc, returned, map[string]any{"reason_code": "missing_information",
		"message": "Falta el monto estimado.", "facts_to_correct": []string{"estimated_amount"}})

	t.Run("CA-2 a change of stage is in the very next read: a returned claim waits for the insured", func(t *testing.T) {
		list := listOf(t, svc, "carolina")
		item := itemOf(t, list, returned)
		assert.Equal(t, "returned", item["status"])
		assert.True(t, instant(t, item["last_changed_at"]).After(instant(t, item["filed_at"])))
		assert.WithinDuration(t, time.Now(), instant(t, item["last_changed_at"]), time.Minute)
		summary := list["summary"].(map[string]any)
		assert.Equal(t, []any{map[string]any{"claim_id": returned, "filing_number": item["filing_number"]}},
			summary["pending_action"])

		rec, detail := call(t, svc, http.MethodGet, "/v1/claims/"+returned, "carolina", nil, nil)
		require.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, item["last_changed_at"], detail["last_changed_at"])
	})

	t.Run("CA-1 the detail follows every stage up to the payout, with the amount", func(t *testing.T) {
		admit(t, svc, paid)
		rec, _ := postJSON(t, svc, "/v1/claims/"+paid+"/approval", "analista",
			map[string]any{"gross_amount_minor": cents(4_350_000)})
		require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
		waitForStatus(t, svc, "carolina", paid, "paid")

		_, history := historyOf(t, svc, "carolina", paid)
		assert.Equal(t, []string{"active", "in_evaluation", "approved", "paid"}, statusesOf(history))
		decision := history["decision"].(map[string]any)
		assert.Equal(t, "approved", decision["outcome"])
		assert.Equal(t, "confirmed", decision["payout_status"])
		assert.NotNil(t, decision["paid_at"])
		assert.NotNil(t, decision["amount_minor"])
		assert.NotContains(t, decision, "account_number")
		assert.Nil(t, history["provider"], "water damage paid without an on-site visit")

		summary := listOf(t, svc, "carolina")["summary"].(map[string]any)
		thisYear := summary["paid_this_year"].(map[string]any)
		assert.EqualValues(t, 1, thisYear["claims"])
		assert.Equal(t, decision["amount_minor"], thisYear["amount_minor"])
		assert.Equal(t, "COP", thisYear["currency"])
		assert.EqualValues(t, 1, summary["in_progress"], "the paid claim is closed")
	})

	t.Run("the insured downloads the supports the claim shows, and only those", func(t *testing.T) {
		quote := supportIDsOf(t, svc, paid, "repair_quote")[0]
		rec, body := call(t, svc, http.MethodGet, "/v1/claims/"+paid+"/supports/"+quote+"/download", "carolina", nil, nil)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		assert.Contains(t, body["download_url"], quote)
		assert.Equal(t, "application/pdf", body["content_type"])
		assert.WithinDuration(t, time.Now().Add(10*time.Minute), instant(t, body["expires_at"]), time.Minute)

		rec, _ = call(t, svc, http.MethodGet, "/v1/claims/"+paid+"/supports/"+quote+"/download", "andres", nil, nil)
		assert.Equal(t, http.StatusForbidden, rec.Code, "another insured's claim")
		other := supportIDsOf(t, svc, returned, "repair_quote")[0]
		rec, body = call(t, svc, http.MethodGet, "/v1/claims/"+paid+"/supports/"+other+"/download", "carolina", nil, nil)
		assert.Equal(t, http.StatusNotFound, rec.Code, "a support of another claim")
		assert.Equal(t, "support_not_found", body["error"])
	})

	t.Run("a support replaced by a resubmission is not downloaded any more", func(t *testing.T) {
		returnedQuote := supportIDsOf(t, svc, returned, "repair_quote")[0]
		code, body := resubmit(t, svc, "carolina", returned, map[string]any{"review_seq": 1,
			"estimated_amount_minor": cents(900_000),
			"supports":               []map[string]any{{"requirement_code": "repair_quote", "support_ids": uploadIDs(t, svc, "carolina", pdfFile, 1)}}})
		require.Equal(t, http.StatusOK, code, body)

		rec, _ := call(t, svc, http.MethodGet, "/v1/claims/"+returned+"/supports/"+returnedQuote+"/download", "carolina", nil, nil)
		assert.Equal(t, http.StatusNotFound, rec.Code)
		assert.Empty(t, listOf(t, svc, "carolina")["summary"].(map[string]any)["pending_action"],
			"sent again, nothing waits for the insured")
	})
}
