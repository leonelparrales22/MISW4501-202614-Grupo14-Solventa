package httpadapter_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	httpadapter "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/adapters/http"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/evaluation"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/ports"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/review"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/triage"
)

// HU-W2.1-03: la revisión del asesor y el historial del siniestro.

var revisadoEn = time.Date(2026, 11, 9, 20, 0, 0, 0, time.UTC)

var devuelta = domain.Review{ClaimID: claimID, FilingNumber: "SIN-2026-00007", Seq: 1, Outcome: domain.ReviewReturned,
	ReviewedBy: "asesor@solventa.example", ReviewedAt: revisadoEn, Reason: "La factura no es legible.",
	MissingSupports: []string{"repair_quote"}, FactsToCorrect: []domain.Fact{domain.FactEstimatedAmount}}

type fakeReviewer struct {
	review  domain.Review
	detail  review.Detail
	history review.History
	err     error
	calls   int

	gotClaim, gotAdvisor, gotOwner string
	gotReturn                      domain.ReturnRequest
}

func (f *fakeReviewer) Queue(context.Context, int, string) (evaluation.QueuePage, error) {
	f.calls++
	return evaluation.QueuePage{Items: []evaluation.QueueItem{{Claim: domain.ClaimRecord{Claim: domain.Claim{
		ID: claimID, Status: domain.StatusActive, FiledAt: revisadoEn}}}}}, f.err
}

func (f *fakeReviewer) Detail(_ context.Context, id string) (review.Detail, error) {
	f.calls++
	f.gotClaim = id
	return f.detail, f.err
}

func (f *fakeReviewer) Approve(_ context.Context, id, advisor string) (domain.Review, error) {
	f.calls++
	f.gotClaim, f.gotAdvisor = id, advisor
	return f.review, f.err
}

func (f *fakeReviewer) Return(_ context.Context, id, advisor string, in domain.ReturnRequest) (domain.Review, error) {
	f.calls++
	f.gotClaim, f.gotAdvisor, f.gotReturn = id, advisor, in
	return f.review, f.err
}

func (f *fakeReviewer) History(_ context.Context, id, owner string) (review.History, error) {
	f.calls++
	f.gotClaim, f.gotOwner = id, owner
	return f.history, f.err
}

// triageNotApplicable is the priority of a filing waiting for review: none.
func triageNotApplicable() triage.View {
	return triage.View{ClaimID: claimID, State: domain.TriageNotApplicable}
}

func reviewRouter(f *fakeReviewer) http.Handler {
	return httpadapter.NewRouter(httpadapter.WithReviews(f, callers))
}

func TestOnlyTheAdvisorReviews(t *testing.T) {
	for _, caller := range []string{"insured", "operations", "partner", ""} {
		f := &fakeReviewer{}
		h := reviewRouter(f)
		want := http.StatusForbidden
		if caller == "" {
			want = http.StatusUnauthorized
		}
		assert.Equal(t, want, do(h, http.MethodGet, "/v1/claims/reviews", caller, nil, nil).Code, caller)
		assert.Equal(t, want, do(h, http.MethodGet, "/v1/claims/"+claimID+"/review", caller, nil, nil).Code, caller)
		code, _ := post(h, "/v1/claims/"+claimID+"/review-approval", caller, ``)
		assert.Equal(t, want, code, caller)
		code, _ = post(h, "/v1/claims/"+claimID+"/review-return", caller, `{"reason":"x","facts_to_correct":["place"]}`)
		assert.Equal(t, want, code, caller)
		assert.Zero(t, f.calls, "%s never reaches the service", caller)
	}
}

func TestTheAdvisorListsTheFilingsToReview(t *testing.T) {
	f := &fakeReviewer{}

	rec := do(reviewRouter(f), http.MethodGet, "/v1/claims/reviews", "advisor", nil, nil)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
	items := decode(t, rec)["items"].([]any)
	require.Len(t, items, 1)
	assert.Equal(t, claimID, items[0].(map[string]any)["claim_id"])
}

func TestTheApprovalAnswersTheClaimInEvaluation(t *testing.T) {
	f := &fakeReviewer{review: domain.Review{ClaimID: claimID, FilingNumber: "SIN-2026-00007", Seq: 1,
		Outcome: domain.ReviewApproved, ReviewedBy: "asesor@solventa.example", ReviewedAt: revisadoEn}}

	rec := do(reviewRouter(f), http.MethodPost, "/v1/claims/"+claimID+"/review-approval", "advisor", nil, nil)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, claimID, f.gotClaim)
	assert.Equal(t, "sub-advisor", f.gotAdvisor, "who reviewed, from the token")
	body := decode(t, rec)
	assert.Equal(t, "in_evaluation", body["status"])
	assert.Equal(t, "SIN-2026-00007", body["filing_number"])
	r := body["review"].(map[string]any)
	assert.Equal(t, "approved", r["outcome"])
	assert.Nil(t, r["reason"])
	assert.Equal(t, []any{}, r["missing_supports"])
	assert.Equal(t, []any{}, r["facts_to_correct"])
}

func TestTheReturnCarriesTheReasonAndWhatIsMissing(t *testing.T) {
	f := &fakeReviewer{review: devuelta}

	code, raw := post(reviewRouter(f), "/v1/claims/"+claimID+"/review-return", "advisor",
		`{"reason":"La factura no es legible.","missing_supports":["repair_quote"],"facts_to_correct":["estimated_amount"]}`)

	require.Equal(t, http.StatusOK, code, raw)
	assert.Equal(t, domain.ReturnRequest{Reason: "La factura no es legible.", MissingSupports: []string{"repair_quote"},
		FactsToCorrect: []domain.Fact{domain.FactEstimatedAmount}}, f.gotReturn)
	assert.Contains(t, raw, `"status":"returned"`)
	assert.Contains(t, raw, `"reason":"La factura no es legible."`)
	assert.Contains(t, raw, `"missing_supports":["repair_quote"]`)
}

func TestTheReturnIsValidatedBeforeTheService(t *testing.T) {
	for name, body := range map[string]string{
		"not json":          `reason`,
		"no reason":         `{"facts_to_correct":["place"]}`,
		"unknown field":     `{"reason":"x","facts_to_correct":["place"],"extra":1}`,
		"unknown fact":      `{"reason":"x","facts_to_correct":["policy_id"]}`,
		"repeated fact":     `{"reason":"x","facts_to_correct":["place","place"]}`,
		"repeated support":  `{"reason":"x","missing_supports":["repair_quote","repair_quote"]}`,
		"long support code": `{"reason":"x","missing_supports":["` + strings.Repeat("a", 41) + `"]}`,
		"too many supports": `{"reason":"x","missing_supports":["a","b","c","d","e","f","g","h","i","j","k"]}`,
		"too many facts":    `{"reason":"x","facts_to_correct":["place","description","occurred_at","estimated_amount","place"]}`,
		"huge reason":       `{"reason":"` + strings.Repeat("a", 4001) + `","facts_to_correct":["place"]}`,
		"two objects":       `{"reason":"x","facts_to_correct":["place"]}{}`,
	} {
		f := &fakeReviewer{}
		code, raw := post(reviewRouter(f), "/v1/claims/"+claimID+"/review-return", "advisor", body)
		assert.Equal(t, http.StatusBadRequest, code, "%s: %s", name, raw)
		assert.Zero(t, f.calls, name)
	}
	code, _ := post(reviewRouter(&fakeReviewer{}), "/v1/claims/not-an-id/review-return", "advisor", `{}`)
	assert.Equal(t, http.StatusNotFound, code)
}

func TestTheErrorsOfTheReviewAreAnswered(t *testing.T) {
	for err, want := range map[error]struct {
		status int
		code   string
	}{
		ports.ErrClaimNotFound:         {http.StatusNotFound, "claim_not_found"},
		domain.ErrNotReviewable:        {http.StatusConflict, "claim_not_reviewable"},
		domain.ErrReturnReasonRequired: {http.StatusUnprocessableEntity, "reason_required"},
		domain.ErrReturnReasonTooLong:  {http.StatusUnprocessableEntity, "reason_too_long"},
		domain.ErrNothingRequested:     {http.StatusUnprocessableEntity, "nothing_requested"},
		domain.ErrSupportNotApplicable: {http.StatusUnprocessableEntity, "support_not_applicable"},
		errors.New("database down"):    {http.StatusServiceUnavailable, "claims_unavailable"},
	} {
		f := &fakeReviewer{err: err}
		code, raw := post(reviewRouter(f), "/v1/claims/"+claimID+"/review-return", "advisor",
			`{"reason":"x","facts_to_correct":["place"]}`)
		assert.Equal(t, want.status, code, err)
		assert.Contains(t, raw, `"error":"`+want.code+`"`, err)
		code, _ = post(reviewRouter(f), "/v1/claims/"+claimID+"/review-approval", "advisor", ``)
		assert.Equal(t, want.status, code, err)
	}
}

func TestTheDetailOfTheReviewCarriesTheReviews(t *testing.T) {
	f := &fakeReviewer{detail: review.Detail{
		Evaluation: evaluation.Evaluation{Claim: domain.ClaimRecord{Claim: domain.Claim{ID: claimID,
			FilingNumber: "SIN-2026-00007", Status: domain.StatusActive, FiledAt: revisadoEn}},
			Triage: triageNotApplicable()},
		Reviews: []domain.Review{devuelta},
	}}

	rec := do(reviewRouter(f), http.MethodGet, "/v1/claims/"+claimID+"/review", "advisor", nil, nil)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	body := decode(t, rec)
	assert.Equal(t, claimID, body["claim"].(map[string]any)["claim_id"])
	require.Len(t, body["reviews"], 1)
	assert.Equal(t, "returned", body["reviews"].([]any)[0].(map[string]any)["outcome"])

	for err, want := range map[error]int{ports.ErrClaimNotFound: http.StatusNotFound, errors.New("x"): http.StatusServiceUnavailable} {
		rec = do(reviewRouter(&fakeReviewer{err: err}), http.MethodGet, "/v1/claims/"+claimID+"/review", "advisor", nil, nil)
		assert.Equal(t, want, rec.Code, err)
	}
}

func TestTheHistoryOfTheInsuredIsReadOnlyOfTheirClaims(t *testing.T) {
	f := &fakeReviewer{history: review.History{
		Claim: domain.ClaimRecord{Claim: domain.Claim{ID: claimID, FilingNumber: "SIN-2026-00007",
			Status: domain.StatusReturned, FiledAt: revisadoEn.Add(-time.Hour)}},
		Changes: []domain.StatusChange{
			{ID: 7, ClaimID: claimID, Status: domain.StatusActive, ChangedAt: revisadoEn.Add(-time.Hour)},
			{ID: 9, ClaimID: claimID, Status: domain.StatusReturned, PreviousStatus: domain.StatusActive, ChangedAt: revisadoEn},
		},
		LastReturn: &devuelta,
	}}

	rec := do(reviewRouter(f), http.MethodGet, "/v1/claims/"+claimID+"/history", "insured", nil, nil)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "sub-carolina", f.gotOwner, "the insured reads only their own claims")
	body := decode(t, rec)
	assert.Equal(t, "returned", body["status"])
	assert.Equal(t, revisadoEn.Format(time.RFC3339), body["last_changed_at"])
	changes := body["changes"].([]any)
	require.Len(t, changes, 2)
	assert.Nil(t, changes[0].(map[string]any)["previous_status"])
	assert.Equal(t, "active", changes[1].(map[string]any)["previous_status"])
	assert.EqualValues(t, 9, changes[1].(map[string]any)["id"])
	assert.Equal(t, "La factura no es legible.", body["last_return"].(map[string]any)["reason"])

	for _, caller := range []string{"advisor", "operations"} {
		do(reviewRouter(f), http.MethodGet, "/v1/claims/"+claimID+"/history", caller, nil, nil)
		assert.Empty(t, f.gotOwner, "%s reads any claim", caller)
	}
	assert.Equal(t, http.StatusForbidden,
		do(reviewRouter(f), http.MethodGet, "/v1/claims/"+claimID+"/history", "partner", nil, nil).Code)
}

func TestTheHistoryWithoutChangesIsAsOfTheFiling(t *testing.T) {
	f := &fakeReviewer{history: review.History{Claim: domain.ClaimRecord{Claim: domain.Claim{ID: claimID,
		Status: domain.StatusActive, FiledAt: revisadoEn}}}}

	body := decode(t, do(reviewRouter(f), http.MethodGet, "/v1/claims/"+claimID+"/history", "advisor", nil, nil))

	assert.Equal(t, revisadoEn.Format(time.RFC3339), body["last_changed_at"])
	assert.Equal(t, []any{}, body["changes"])
	assert.Nil(t, body["last_return"])
}

func TestTheHistoryOfAClaimOfSomeoneElseIsNotFound(t *testing.T) {
	for err, want := range map[error]int{
		review.ErrNotOwner:     http.StatusNotFound,
		ports.ErrClaimNotFound: http.StatusNotFound,
		errors.New("x"):        http.StatusServiceUnavailable,
	} {
		rec := do(reviewRouter(&fakeReviewer{err: err}), http.MethodGet, "/v1/claims/"+claimID+"/history", "other", nil, nil)
		assert.Equal(t, want, rec.Code, err)
	}
	assert.Equal(t, http.StatusNotFound,
		do(reviewRouter(&fakeReviewer{}), http.MethodGet, "/v1/claims/x/history", "insured", nil, nil).Code)
}
