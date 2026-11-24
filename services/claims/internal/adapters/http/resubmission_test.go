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
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/ports"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/review"
)

// HU-W2.1-04: el asegurado corrige la radicación devuelta y la reenvía (A9).

var reenviadoEn = time.Date(2026, 11, 10, 15, 0, 0, 0, time.UTC)

var reenvio = domain.Resubmission{ClaimID: claimID, FilingNumber: "SIN-2026-01877", Seq: 1, ReviewSeq: 1,
	ResubmittedBy: "sub-carolina", ResubmittedAt: reenviadoEn,
	Facts: []domain.FactChange{{Fact: domain.FactEstimatedAmount, Before: "", After: "480000000"}},
	Supports: []domain.SupportChange{{RequirementCode: "traffic_report", Label: "Informe de tránsito o croquis",
		Added: []string{"sup-0000000000000000000000000000000a"}, Withdrawn: []string{"sup-00000000000000000000000000000008"}}}}

type fakeResubmitter struct {
	r       domain.Resubmission
	created bool
	err     error
	calls   int

	gotClaim, gotSubject string
	got                  domain.ResubmissionRequest
}

func (f *fakeResubmitter) Resubmit(_ context.Context, id, subject string,
	in domain.ResubmissionRequest) (domain.Resubmission, bool, error) {
	f.calls++
	f.gotClaim, f.gotSubject, f.got = id, subject, in
	return f.r, f.created, f.err
}

// postMap posts the body and decodes the answer.
func postMap(h http.Handler, path, caller, body string) (int, map[string]any) {
	code, out, _ := send(h, http.MethodPost, path, caller, body)
	return code, out
}

func resubmissionRouter(f *fakeResubmitter) http.Handler {
	return httpadapter.NewRouter(httpadapter.WithResubmission(f, callers))
}

const validResubmission = `{"review_seq":1,"place":"Calle 80 con carrera 30, Bogotá","estimated_amount_minor":480000000,
	"supports":[{"requirement_code":"traffic_report","support_ids":["sup-0000000000000000000000000000000a"]}]}`

func TestOnlyTheInsuredResubmits(t *testing.T) {
	for _, caller := range []string{"advisor", "operations", "partner", ""} {
		f := &fakeResubmitter{}
		want := http.StatusForbidden
		if caller == "" {
			want = http.StatusUnauthorized
		}
		code, _ := postMap(resubmissionRouter(f), "/v1/claims/"+claimID+"/resubmission", caller, validResubmission)
		assert.Equal(t, want, code, caller)
		assert.Zero(t, f.calls, "%s never reaches the service", caller)
	}
}

func TestTheResubmissionAnswersTheClaimActiveAndWhatChanged(t *testing.T) {
	r := reenvio
	r.Claim.Status = domain.StatusActive
	f := &fakeResubmitter{r: r, created: true}

	code, body := postMap(resubmissionRouter(f), "/v1/claims/"+claimID+"/resubmission", "insured", validResubmission)

	require.Equal(t, http.StatusOK, code, body)
	assert.Equal(t, claimID, f.gotClaim)
	assert.Equal(t, "sub-carolina", f.gotSubject, "the subject of the token, never one of the body")
	assert.Equal(t, 1, f.got.ReviewSeq)
	require.NotNil(t, f.got.Place)
	assert.Equal(t, "Calle 80 con carrera 30, Bogotá", *f.got.Place)
	assert.Nil(t, f.got.OccurredAt, "a fact that is not sent is kept")
	assert.Nil(t, f.got.Description)
	require.NotNil(t, f.got.EstimatedAmountMinor)
	assert.Equal(t, "480000000", *f.got.EstimatedAmountMinor)
	assert.Equal(t, []domain.SupportGroup{{RequirementCode: "traffic_report",
		SupportIDs: []string{"sup-0000000000000000000000000000000a"}}}, f.got.Supports)

	assert.Equal(t, "active", body["status"])
	assert.Equal(t, "SIN-2026-01877", body["filing_number"])
	got := body["resubmission"].(map[string]any)
	assert.EqualValues(t, 1, got["seq"])
	assert.EqualValues(t, 1, got["review_seq"])
	assert.Equal(t, "sub-carolina", got["resubmitted_by"])
	assert.Equal(t, reenviadoEn.Format(time.RFC3339), got["resubmitted_at"])
	assert.Equal(t, []any{map[string]any{"fact": "estimated_amount", "before": nil, "after": "480000000"}},
		got["changed_facts"], "an amount that was not declared is null")
	assert.Equal(t, []any{map[string]any{"requirement_code": "traffic_report", "label": "Informe de tránsito o croquis",
		"added": []any{"sup-0000000000000000000000000000000a"}, "withdrawn": []any{"sup-00000000000000000000000000000008"}}},
		got["changed_supports"])
}

func TestARetryAnswersTheClaimAsItIsNow(t *testing.T) {
	r := reenvio
	r.Claim.Status = domain.StatusInEvaluation
	r.Supports = []domain.SupportChange{{RequirementCode: "plate_photo", Label: "Fotografía de la placa",
		Added: []string{"sup-0000000000000000000000000000000b"}}}
	f := &fakeResubmitter{r: r}

	code, body := postMap(resubmissionRouter(f), "/v1/claims/"+claimID+"/resubmission", "insured", `{"review_seq":1}`)

	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, "in_evaluation", body["status"], "the advisor already approved it")
	change := body["resubmission"].(map[string]any)["changed_supports"].([]any)[0].(map[string]any)
	assert.Equal(t, []any{}, change["withdrawn"], "never null")
}

func TestTheResubmissionIsValidatedBeforeTheService(t *testing.T) {
	for name, body := range map[string]string{
		"without the return":   `{"place":"Calle 80"}`,
		"a return of none":     `{"review_seq":0}`,
		"a field of no one":    `{"review_seq":1,"status":"active"}`,
		"not json":             `{"review_seq":1`,
		"a policy is not sent": `{"review_seq":1,"policy_id":"AU-2025-118204"}`,
	} {
		f := &fakeResubmitter{}
		code, out := postMap(resubmissionRouter(f), "/v1/claims/"+claimID+"/resubmission", "insured", body)
		assert.Equal(t, http.StatusBadRequest, code, name)
		assert.Equal(t, "invalid_request", out["error"], name)
		assert.Zero(t, f.calls, name)
	}
	code, _ := postMap(resubmissionRouter(&fakeResubmitter{}), "/v1/claims/no-es-un-id/resubmission", "insured",
		`{"review_seq":1}`)
	assert.Equal(t, http.StatusNotFound, code, "an id that is not one is a claim that does not exist")
}

func TestAnAmountThatIsNullIsKept(t *testing.T) {
	f := &fakeResubmitter{r: reenvio}

	code, _ := postMap(resubmissionRouter(f), "/v1/claims/"+claimID+"/resubmission", "insured",
		`{"review_seq":1,"estimated_amount_minor":null}`)

	require.Equal(t, http.StatusOK, code)
	assert.Nil(t, f.got.EstimatedAmountMinor)
}

func TestTheErrorsOfTheResubmissionAreAnswered(t *testing.T) {
	for _, tc := range []struct {
		err   error
		code  int
		error string
	}{
		{&domain.ValidationError{Fields: []domain.FieldError{{Field: "supports.traffic_report", Code: domain.CodeNotReplaced}}},
			http.StatusUnprocessableEntity, "validation_failed"},
		{ports.ErrClaimNotFound, http.StatusNotFound, "claim_not_found"},
		{review.ErrNotOwner, http.StatusNotFound, "claim_not_found"},
		{domain.ErrNotReturned, http.StatusConflict, "claim_not_returned"},
		{domain.ErrStaleReturn, http.StatusConflict, "stale_return"},
		{errors.New("la base no responde"), http.StatusServiceUnavailable, "claims_unavailable"},
	} {
		f := &fakeResubmitter{err: tc.err}
		code, body := postMap(resubmissionRouter(f), "/v1/claims/"+claimID+"/resubmission", "insured", `{"review_seq":1}`)
		assert.Equal(t, tc.code, code, tc.error)
		assert.Equal(t, tc.error, body["error"])
	}
	f := &fakeResubmitter{err: &domain.ValidationError{Fields: []domain.FieldError{
		{Field: "supports.traffic_report", Code: domain.CodeNotReplaced}}}}
	_, body := postMap(resubmissionRouter(f), "/v1/claims/"+claimID+"/resubmission", "insured", `{"review_seq":1}`)
	assert.Equal(t, []any{map[string]any{"field": "supports.traffic_report", "code": "not_replaced"}}, body["fields"])
}

func TestTheHistoryCarriesTheResubmissions(t *testing.T) {
	f := &fakeReviewer{history: review.History{
		Claim: domain.ClaimRecord{Claim: domain.Claim{ID: claimID, FilingNumber: "SIN-2026-01877",
			Status: domain.StatusActive, FiledAt: revisadoEn.Add(-time.Hour)}},
		Reviews:       []domain.Review{devuelta},
		Resubmissions: []domain.Resubmission{reenvio},
		Requirements: []domain.Requirement{{Code: "traffic_report", Label: "Informe de tránsito o croquis", Required: true,
			MinFiles: 1, MaxFiles: 3, Formats: []domain.FileFormat{domain.FormatPDF}}},
	}}

	rec := do(reviewRouter(f), http.MethodGet, "/v1/claims/"+claimID+"/history", "insured", nil, nil)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	body := decode(t, rec)
	require.Len(t, body["resubmissions"], 1)
	got := body["resubmissions"].([]any)[0].(map[string]any)
	assert.EqualValues(t, 1, got["review_seq"])
	assert.Nil(t, body["last_return"])
	assert.Equal(t, []any{map[string]any{"code": "traffic_report", "label": "Informe de tránsito o croquis",
		"required": true, "min_files": 1.0, "max_files": 3.0, "formats": []any{"pdf"}}}, body["requirements"])

	f.history.Resubmissions, f.history.Requirements = nil, nil
	body = decode(t, do(reviewRouter(f), http.MethodGet, "/v1/claims/"+claimID+"/history", "insured", nil, nil))
	assert.Equal(t, []any{}, body["resubmissions"], "never null")
	assert.Equal(t, []any{}, body["requirements"], "never null")
}
