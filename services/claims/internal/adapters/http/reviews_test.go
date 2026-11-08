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

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/security"
	httpadapter "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/adapters/http"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/evaluation"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/ports"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/review"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/triage"
)

// HU-W2.1-03: la revisión del asesor (S8) y el historial del siniestro.

var revisadoEn = time.Date(2026, 11, 9, 20, 0, 0, 0, time.UTC)

var devuelta = domain.Review{ClaimID: claimID, FilingNumber: "SIN-2026-01877", Seq: 1, Outcome: domain.ReviewReturned,
	ReviewedBy: "juan@solventa.example", ReviewerName: "Juan Ríos", ReviewedAt: revisadoEn,
	ReasonCode: domain.ReasonIllegibleOrIncompleteSupports, Message: "El informe de tránsito no se puede leer.",
	Supports: []domain.SupportAssessment{
		{RequirementCode: "vehicle_photos", Label: "Fotos del vehículo", Verdict: domain.VerdictValid},
		{RequirementCode: "traffic_report", Label: "Informe de tránsito o croquis", Verdict: domain.VerdictIllegible,
			Note: "borroso, no se lee el número de croquis"},
		{RequirementCode: "plate_photo", Label: "Fotografía de la placa", Verdict: domain.VerdictMissing},
	},
	FactsToCorrect: []domain.Fact{domain.FactEstimatedAmount}}

// reviewCallers adds an advisor with a name in the user pool.
var reviewCallers = func() tokens {
	out := tokens{"juan": {Subject: "sub-juan", Email: "juan@solventa.example", Name: "Juan Ríos",
		Roles: []security.Role{security.RoleAdvisor}}}
	for k, v := range callers {
		out[k] = v
	}
	return out
}()

type fakeReviewer struct {
	review  domain.Review
	detail  review.Detail
	history review.History
	err     error
	calls   int

	gotClaim, gotOwner string
	gotAdvisor         domain.Reviewer
	gotReturn          domain.ReturnRequest
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

func (f *fakeReviewer) Approve(_ context.Context, id string, advisor domain.Reviewer) (domain.Review, error) {
	f.calls++
	f.gotClaim, f.gotAdvisor = id, advisor
	return f.review, f.err
}

func (f *fakeReviewer) Return(_ context.Context, id string, advisor domain.Reviewer, in domain.ReturnRequest) (domain.Review, error) {
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
	return httpadapter.NewRouter(httpadapter.WithReviews(f, reviewCallers))
}

const validReturn = `{"reason_code":"illegible_or_incomplete_supports","message":"El informe no se lee.",
	"supports":[{"requirement_code":"traffic_report","verdict":"illegible","note":"borroso"},
	{"requirement_code":"plate_photo","verdict":"missing"}],"facts_to_correct":["estimated_amount"]}`

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
		code, _ = post(h, "/v1/claims/"+claimID+"/review-return", caller, validReturn)
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
	f := &fakeReviewer{review: domain.Review{ClaimID: claimID, FilingNumber: "SIN-2026-01877", Seq: 1,
		Outcome: domain.ReviewApproved, ReviewedBy: "juan@solventa.example", ReviewerName: "Juan Ríos", ReviewedAt: revisadoEn,
		Supports: []domain.SupportAssessment{{RequirementCode: "vehicle_photos", Label: "Fotos del vehículo",
			Verdict: domain.VerdictValid}}}}

	rec := do(reviewRouter(f), http.MethodPost, "/v1/claims/"+claimID+"/review-approval", "juan", nil, nil)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, claimID, f.gotClaim)
	assert.Equal(t, domain.Reviewer{ID: "juan@solventa.example", Name: "Juan Ríos"}, f.gotAdvisor, "who reviewed, from the token")
	body := decode(t, rec)
	assert.Equal(t, "in_evaluation", body["status"])
	r := body["review"].(map[string]any)
	assert.Equal(t, "approved", r["outcome"])
	assert.Equal(t, "Juan Ríos", r["reviewer_name"])
	assert.Nil(t, r["reason_code"])
	assert.Nil(t, r["message"])
	assert.Equal(t, []any{map[string]any{"requirement_code": "vehicle_photos", "label": "Fotos del vehículo",
		"verdict": "valid", "note": nil}}, r["supports"])
	assert.Equal(t, []any{}, r["facts_to_correct"])
}

func TestAnAdvisorWithoutANameIsShownByTheirId(t *testing.T) {
	f := &fakeReviewer{}

	do(reviewRouter(f), http.MethodPost, "/v1/claims/"+claimID+"/review-approval", "advisor", nil, nil)

	assert.Equal(t, domain.Reviewer{ID: "sub-advisor", Name: "sub-advisor"}, f.gotAdvisor)
}

func TestTheReturnCarriesTheVerdictOfEachSupport(t *testing.T) {
	f := &fakeReviewer{review: devuelta}

	code, raw := post(reviewRouter(f), "/v1/claims/"+claimID+"/review-return", "juan", validReturn)

	require.Equal(t, http.StatusOK, code, raw)
	assert.Equal(t, domain.ReturnRequest{ReasonCode: domain.ReasonIllegibleOrIncompleteSupports, Message: "El informe no se lee.",
		Supports: []domain.SupportVerdict{
			{RequirementCode: "traffic_report", Verdict: domain.VerdictIllegible, Note: "borroso"},
			{RequirementCode: "plate_photo", Verdict: domain.VerdictMissing},
		},
		FactsToCorrect: []domain.Fact{domain.FactEstimatedAmount}}, f.gotReturn)
	assert.Contains(t, raw, `"status":"returned"`)
	assert.Contains(t, raw, `"reason_code":"illegible_or_incomplete_supports"`)
	assert.Contains(t, raw, `{"requirement_code":"traffic_report","label":"Informe de tránsito o croquis","verdict":"illegible","note":"borroso, no se lee el número de croquis"}`)
	assert.Contains(t, raw, `{"requirement_code":"plate_photo","label":"Fotografía de la placa","verdict":"missing","note":null}`)
}

func TestTheReturnIsValidatedBeforeTheService(t *testing.T) {
	for name, body := range map[string]string{
		"not json":          `reason`,
		"no reason":         `{"message":"x","facts_to_correct":["place"]}`,
		"no message":        `{"reason_code":"other","facts_to_correct":["place"]}`,
		"reason of no list": `{"reason_code":"porque","message":"x","facts_to_correct":["place"]}`,
		"unknown field":     `{"reason_code":"other","message":"x","facts_to_correct":["place"],"extra":1}`,
		"unknown verdict":   `{"reason_code":"other","message":"x","supports":[{"requirement_code":"a","verdict":"maybe"}]}`,
		"no code":           `{"reason_code":"other","message":"x","supports":[{"verdict":"missing"}]}`,
		"long code":         `{"reason_code":"other","message":"x","supports":[{"requirement_code":"` + strings.Repeat("a", 41) + `","verdict":"missing"}]}`,
		"huge note":         `{"reason_code":"other","message":"x","supports":[{"requirement_code":"a","verdict":"missing","note":"` + strings.Repeat("a", 1201) + `"}]}`,
		"repeated support":  `{"reason_code":"other","message":"x","supports":[{"requirement_code":"a","verdict":"missing"},{"requirement_code":"a","verdict":"missing"}]}`,
		"unknown fact":      `{"reason_code":"other","message":"x","facts_to_correct":["policy_id"]}`,
		"repeated fact":     `{"reason_code":"other","message":"x","facts_to_correct":["place","place"]}`,
		"too many facts":    `{"reason_code":"other","message":"x","facts_to_correct":["place","description","occurred_at","estimated_amount","place"]}`,
		"too many supports": `{"reason_code":"other","message":"x","supports":[` + strings.TrimSuffix(strings.Repeat(`{"requirement_code":"a","verdict":"missing"},`, 13), ",") + `]}`,
		"huge message":      `{"reason_code":"other","message":"` + strings.Repeat("a", 4001) + `","facts_to_correct":["place"]}`,
		"two objects":       validReturn + `{}`,
	} {
		f := &fakeReviewer{}
		code, raw := post(reviewRouter(f), "/v1/claims/"+claimID+"/review-return", "advisor", body)
		assert.Equal(t, http.StatusBadRequest, code, "%s: %s", name, raw)
		assert.Zero(t, f.calls, name)
	}
	code, _ := post(reviewRouter(&fakeReviewer{}), "/v1/claims/not-an-id/review-return", "advisor", validReturn)
	assert.Equal(t, http.StatusNotFound, code)
}

func TestTheErrorsOfTheReviewAreAnswered(t *testing.T) {
	for err, want := range map[error]struct {
		status int
		code   string
	}{
		ports.ErrClaimNotFound:         {http.StatusNotFound, "claim_not_found"},
		domain.ErrNotReviewable:        {http.StatusConflict, "claim_not_reviewable"},
		domain.ErrSupportsIncomplete:   {http.StatusUnprocessableEntity, "supports_incomplete"},
		domain.ErrReasonCodeInvalid:    {http.StatusUnprocessableEntity, "reason_code_invalid"},
		domain.ErrMessageRequired:      {http.StatusUnprocessableEntity, "message_required"},
		domain.ErrMessageTooLong:       {http.StatusUnprocessableEntity, "message_too_long"},
		domain.ErrVerdictNoteTooLong:   {http.StatusUnprocessableEntity, "note_too_long"},
		domain.ErrNothingRequested:     {http.StatusUnprocessableEntity, "nothing_requested"},
		domain.ErrSupportNotApplicable: {http.StatusUnprocessableEntity, "support_not_applicable"},
		domain.ErrVerdictNotApplicable: {http.StatusUnprocessableEntity, "verdict_not_applicable"},
		errors.New("database down"):    {http.StatusServiceUnavailable, "claims_unavailable"},
	} {
		f := &fakeReviewer{err: err}
		code, raw := post(reviewRouter(f), "/v1/claims/"+claimID+"/review-return", "advisor", validReturn)
		assert.Equal(t, want.status, code, err)
		assert.Contains(t, raw, `"error":"`+want.code+`"`, err)
		code, _ = post(reviewRouter(f), "/v1/claims/"+claimID+"/review-approval", "advisor", ``)
		assert.Equal(t, want.status, code, err)
	}
}

func TestTheDetailOfTheReviewCarriesTheChecklistThePriorClaimsAndTheReviews(t *testing.T) {
	f := &fakeReviewer{detail: review.Detail{
		Evaluation: evaluation.Evaluation{Claim: domain.ClaimRecord{Claim: domain.Claim{ID: claimID,
			FilingNumber: "SIN-2026-01877", Status: domain.StatusActive, FiledAt: revisadoEn}},
			Triage: triageNotApplicable()},
		Checklist: []domain.ChecklistItem{
			{RequirementCode: "vehicle_photos", Label: "Fotos del vehículo", Required: true, Files: 6},
			{RequirementCode: "plate_photo", Label: "Fotografía de la placa"},
		},
		PriorClaims: []domain.ClaimRecord{{Claim: domain.Claim{ID: "otro", FilingNumber: "SIN-2026-01500",
			IncidentType: domain.IncidentGlass, Status: domain.StatusRejected, FiledAt: revisadoEn.AddDate(0, -3, 0)}}},
		Reviews: []domain.Review{devuelta},
	}}

	rec := do(reviewRouter(f), http.MethodGet, "/v1/claims/"+claimID+"/review", "advisor", nil, nil)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	body := decode(t, rec)
	assert.Equal(t, claimID, body["claim"].(map[string]any)["claim_id"])
	assert.Equal(t, []any{
		map[string]any{"requirement_code": "vehicle_photos", "label": "Fotos del vehículo", "required": true, "files": 6.0},
		map[string]any{"requirement_code": "plate_photo", "label": "Fotografía de la placa", "required": false, "files": 0.0},
	}, body["checklist"])
	require.Len(t, body["prior_claims"], 1)
	assert.Equal(t, "rejected", body["prior_claims"].([]any)[0].(map[string]any)["status"])
	require.Len(t, body["reviews"], 1)
	assert.Equal(t, "returned", body["reviews"].([]any)[0].(map[string]any)["outcome"])

	for err, want := range map[error]int{ports.ErrClaimNotFound: http.StatusNotFound, errors.New("x"): http.StatusServiceUnavailable} {
		rec = do(reviewRouter(&fakeReviewer{err: err}), http.MethodGet, "/v1/claims/"+claimID+"/review", "advisor", nil, nil)
		assert.Equal(t, want, rec.Code, err)
	}
}

func TestTheHistoryOfTheInsuredIsReadOnlyOfTheirClaims(t *testing.T) {
	f := &fakeReviewer{history: review.History{
		Claim: domain.ClaimRecord{Claim: domain.Claim{ID: claimID, FilingNumber: "SIN-2026-01877",
			Status: domain.StatusReturned, FiledAt: revisadoEn.Add(-time.Hour)}},
		Changes: []domain.StatusChange{
			{ID: 7, ClaimID: claimID, Status: domain.StatusActive, ChangedAt: revisadoEn.Add(-time.Hour)},
			{ID: 9, ClaimID: claimID, Status: domain.StatusReturned, PreviousStatus: domain.StatusActive, ChangedAt: revisadoEn},
		},
		Reviews:    []domain.Review{devuelta},
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
	require.Len(t, body["reviews"], 1)
	last := body["last_return"].(map[string]any)
	assert.Equal(t, "Juan Ríos", last["reviewer_name"])
	assert.Equal(t, "El informe de tránsito no se puede leer.", last["message"])

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
	assert.Equal(t, []any{}, body["reviews"])
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
