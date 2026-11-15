package httpadapter_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	httpadapter "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/http"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/endorsements"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
)

// HU-W1.4-02: the routes of the operations analyst.

var analystAndres = domain.Actor{Subject: "sub-andres", Name: "Andrés Ruiz", Email: "operaciones@example.com"}

type fakeReview struct {
	page      domain.EndorsementInboxPage
	detail    endorsements.Detail
	request   domain.ChangeRequest
	apply     endorsements.ApplyResult
	policy    domain.Policy
	history   []domain.EndorsementHistoryEntry
	err       error
	calls     int
	gotID     string
	gotObs    string
	gotFilter domain.EndorsementInboxFilter
}

func (f *fakeReview) Inbox(_ context.Context, filter domain.EndorsementInboxFilter) (domain.EndorsementInboxPage, error) {
	f.calls++
	f.gotFilter = filter
	return f.page, f.err
}

func (f *fakeReview) Detail(_ context.Context, id string) (endorsements.Detail, error) {
	f.calls++
	f.gotID = id
	return f.detail, f.err
}

func (f *fakeReview) Review(_ context.Context, id string) (domain.ChangeRequest, error) {
	f.calls++
	f.gotID = id
	return f.request, f.err
}

func (f *fakeReview) Apply(_ context.Context, id string) (endorsements.ApplyResult, error) {
	f.calls++
	f.gotID = id
	return f.apply, f.err
}

func (f *fakeReview) Reject(_ context.Context, id, obs string) (domain.ChangeRequest, error) {
	f.calls++
	f.gotID, f.gotObs = id, obs
	return f.request, f.err
}

func (f *fakeReview) Return(_ context.Context, id, obs string) (domain.ChangeRequest, error) {
	f.calls++
	f.gotID, f.gotObs = id, obs
	return f.request, f.err
}

func (f *fakeReview) History(_ context.Context, id string) (domain.Policy, []domain.EndorsementHistoryEntry, error) {
	f.calls++
	f.gotID = id
	return f.policy, f.history, f.err
}

func callEndorsements(t *testing.T, svc *fakeReview, method, path, token, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	httpadapter.NewRouter(&fakeResolver{}, httpadapter.WithObservability("origination"),
		httpadapter.WithEndorsementReview(svc, tokenVerifier{})).ServeHTTP(rec, req)
	return rec
}

const analystToken = "operations:sub-andres"

var endorsementRoutes = []struct{ method, path, body string }{
	{http.MethodGet, "/v1/policies/endorsement-requests", ""},
	{http.MethodGet, "/v1/policies/endorsement-requests/SOL-MOD-2026-0042", ""},
	{http.MethodPost, "/v1/policies/endorsement-requests/SOL-MOD-2026-0042/review", ""},
	{http.MethodPost, "/v1/policies/endorsement-requests/SOL-MOD-2026-0042/application", ""},
	{http.MethodPost, "/v1/policies/endorsement-requests/SOL-MOD-2026-0042/rejection", `{"observations":"No procede el cambio"}`},
	{http.MethodPost, "/v1/policies/endorsement-requests/SOL-MOD-2026-0042/return", `{"observations":"No procede el cambio"}`},
	{http.MethodGet, "/v1/policies/pol-acc-1/endorsements", ""},
}

func TestOnlyOperationsReachesTheEndorsementRoutes(t *testing.T) {
	svc := &fakeReview{}
	for _, r := range endorsementRoutes {
		assert.Equal(t, http.StatusUnauthorized, callEndorsements(t, svc, r.method, r.path, "", r.body).Code, r.path)
		for _, token := range []string{"insured:sub-1", "advisor:laura"} {
			rec := callEndorsements(t, svc, r.method, r.path, token, r.body)
			assert.Equal(t, http.StatusForbidden, rec.Code, "%s %s", token, r.path)
		}
	}
	assert.Zero(t, svc.calls, "a forbidden caller never reaches the use case")
	for _, r := range endorsementRoutes {
		rec := callEndorsements(t, svc, r.method, r.path, analystToken, r.body)
		assert.Equal(t, http.StatusOK, rec.Code, r.path)
		assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"), r.path)
	}
}

func TestTheInboxCarriesTheHolderAndTheIndicators(t *testing.T) {
	mean := 3*time.Hour + 20*time.Minute
	r := beneficiariesRequest()
	r.Status, r.DecisionReason = domain.ChangeRequestNotApplied, "Corrige el documento"
	svc := &fakeReview{page: domain.EndorsementInboxPage{Page: 2, PageSize: 5, Total: 6,
		Items: []domain.EndorsementInboxItem{{Request: r, HolderName: "Carolina Pérez Ruiz", HolderDocument: "1020345678"}},
		Indicators: domain.EndorsementIndicators{Pending: 5, PendingOverdue: 2, AppliedToday: 8, MeanTimeToApply: &mean,
			RejectedRecently: 1, TopReason: "shares_total_100"}}}

	rec := callEndorsements(t, svc, http.MethodGet,
		"/v1/policies/endorsement-requests?change_type=beneficiaries&status=all&filed_from=2026-09-09&page=2&page_size=5", analystToken, "")

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, domain.EndorsementInboxFilter{ChangeType: domain.ChangeBeneficiaries, Status: domain.InboxAll,
		FiledFrom: time.Date(2026, 9, 9, 0, 0, 0, 0, domain.ColombiaTime), Page: 2, PageSize: 5}, svc.gotFilter)
	body := decodeBody(t, rec)
	assert.Equal(t, map[string]any{
		"pending":              map[string]any{"count": 5.0, "overdue": 2.0},
		"applied_today":        map[string]any{"count": 8.0, "mean_time_to_apply_seconds": 12000.0},
		"rejected_last_7_days": map[string]any{"count": 1.0, "top_reason": "shares_total_100"},
	}, body["indicators"])
	assert.EqualValues(t, 2, body["total_pages"])
	item := body["items"].([]any)[0].(map[string]any)
	assert.Equal(t, map[string]any{"full_name": "Carolina Pérez Ruiz", "document_id": "1020345678"}, item["holder"])
	assert.Equal(t, "not_applied", item["status"])
	assert.Equal(t, true, item["returned"])
	assert.NotContains(t, rec.Body.String(), "carolina@example.com", "never the e-mail of the insured")
	assert.NotContains(t, rec.Body.String(), "sub-1", "never the subject")
}

func TestTheInboxNamesEveryQueryParameterItCannotRead(t *testing.T) {
	svc := &fakeReview{}

	rec := callEndorsements(t, svc, http.MethodGet,
		"/v1/policies/endorsement-requests?change_type=otro&status=returned&filed_from=09/09/2026&page=0&page_size=51", analystToken, "")

	require.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Equal(t, []any{
		map[string]any{"field": "change_type", "code": "invalid_format"},
		map[string]any{"field": "status", "code": "invalid_format"},
		map[string]any{"field": "filed_from", "code": "invalid_format"},
		map[string]any{"field": "page", "code": "out_of_range"},
		map[string]any{"field": "page_size", "code": "out_of_range"},
	}, decodeBody(t, rec)["fields"])
	assert.Zero(t, svc.calls)
}

func detailOf(r domain.ChangeRequest) endorsements.Detail {
	p := domain.Policy{ID: "pol-acc-1", CustomerID: "sub-1", Premium: 68_500, Currency: "COP", InsuredAmount: 250_000_000,
		Holder:        domain.Applicant{FullName: "Carolina Pérez Ruiz", DocumentID: "1020345678", Email: "carolina@example.com"},
		CoverageStart: changeFiledAt.AddDate(0, -2, 0), CoverageEnd: changeFiledAt.AddDate(0, 10, 0)}
	last := domain.Endorsement{ID: "END-2026-0099", Type: domain.ChangePersonalData, At: changeFiledAt.Add(-time.Hour)}
	return endorsements.Detail{Request: r, Policy: p, InForce: true,
		Terms: domain.PolicyTerms{InsuredAmount: 250_000_000, Currency: "COP",
			Onerous: &domain.OnerousBeneficiary{PartnerID: "aliado-banco-1", Name: "Banco Aliado", Basis: domain.OnerousBasisOutstandingBalance}},
		Endorsements: ports.PolicyEndorsements{Count: 1, Last: &last},
		Validations: domain.Validations{{Code: domain.ValidationSharesTotal100, Outcome: domain.ValidationPassed,
			Params: map[string]string{"shares": "60|40", "total": "100"}}},
		Source: endorsements.SourcePreview}
}

func TestTheDetailShowsTheChangeThePolicyAndTheValidations(t *testing.T) {
	svc := &fakeReview{detail: detailOf(beneficiariesRequest())}

	rec := callEndorsements(t, svc, http.MethodGet, "/v1/policies/endorsement-requests/SOL-MOD-2026-0042", analystToken, "")

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "SOL-MOD-2026-0042", svc.gotID)
	body := decodeBody(t, rec)
	assert.Equal(t, "filed", body["request"].(map[string]any)["status"])
	assert.Len(t, body["changes"], 2)
	policy := body["policy"].(map[string]any)
	assert.InDelta(t, 250_000_000, policy["insured_amount"], 0)
	assert.InDelta(t, 68_500, policy["premium"], 0)
	assert.Equal(t, "Banco Aliado", policy["onerous_beneficiary"].(map[string]any)["name"])
	assert.Equal(t, map[string]any{"count": 1.0, "last": map[string]any{"endorsement_id": "END-2026-0099",
		"change_type": "personal_data", "applied_at": "2026-09-16T11:00:00Z"}}, policy["endorsements"])
	assert.Equal(t, []any{map[string]any{"code": "shares_total_100", "outcome": "passed",
		"params": map[string]any{"shares": "60|40", "total": "100"}}}, body["validations"])
	assert.Equal(t, "preview", body["validations_source"])
	assert.Nil(t, body["decision"])
	assert.Nil(t, body["premium"])
	assert.NotContains(t, rec.Body.String(), "carolina@example.com")
	assert.NotContains(t, rec.Body.String(), "sub-1")
}

func TestTheDetailOfANewInsuredValueCarriesThePremium(t *testing.T) {
	r := beneficiariesRequest()
	r.Type = domain.ChangeInsuredAmount
	r.Previous, r.Requested = domain.ChangeContent{InsuredAmount: 250_000_000}, domain.ChangeContent{InsuredAmount: 300_000_000}
	d := detailOf(r)
	d.Premium = &endorsements.PremiumChange{Previous: 68_500, New: 82_200, Priced: true, Currency: "COP", Version: "v1"}
	svc := &fakeReview{detail: d}

	body := decodeBody(t, callEndorsements(t, svc, http.MethodGet, "/v1/policies/endorsement-requests/SOL-MOD-2026-0042", analystToken, ""))

	assert.Equal(t, map[string]any{"previous": 68_500.0, "new": 82_200.0, "currency": "COP", "rating_version": "v1"}, body["premium"])
	d.Premium.Priced = false
	body = decodeBody(t, callEndorsements(t, svc, http.MethodGet, "/v1/policies/endorsement-requests/SOL-MOD-2026-0042", analystToken, ""))
	assert.Nil(t, body["premium"].(map[string]any)["new"], "not priced: the screen shows no new premium")
}

func TestTheDetailSaysWhatWasDecidedAndByWhom(t *testing.T) {
	applied := beneficiariesRequest()
	applied.Status = domain.ChangeRequestApplied
	e := domain.Endorsement{ID: "END-2026-0102", By: analystAndres, At: changeFiledAt.Add(time.Hour)}
	returned := beneficiariesRequest()
	returned.Status, returned.DecisionReason = domain.ChangeRequestNotApplied, "Corrige el documento de Lucía"
	attempt := domain.EndorsementAttempt{Outcome: domain.OutcomeNotApplied, By: analystAndres, At: changeFiledAt.Add(time.Hour),
		Validations: domain.Validations{{Code: domain.ValidationIdentityDocuments, Outcome: domain.ValidationFailed}}}
	notApplied := returned
	notApplied.DecisionReason = ""
	rejected := beneficiariesRequest()
	rejected.Status, rejected.DecisionReason = domain.ChangeRequestRejected, "No procede"
	for _, tc := range []struct {
		r    domain.ChangeRequest
		rec  domain.RequestRecord
		want map[string]any
	}{
		{applied, domain.RequestRecord{Endorsement: &e}, map[string]any{"outcome": "applied", "endorsement_id": "END-2026-0102",
			"by": map[string]any{"name": "Andrés Ruiz", "role": "operations"}, "at": "2026-09-16T13:00:00Z", "observations": nil,
			"failed": []any{}}},
		{notApplied, domain.RequestRecord{LastAttempt: &attempt}, map[string]any{"outcome": "not_applied", "endorsement_id": nil,
			"by": map[string]any{"name": "Andrés Ruiz", "role": "operations"}, "at": "2026-09-16T13:00:00Z", "observations": nil,
			"failed": []any{"identity_documents_valid"}}},
		{returned, domain.RequestRecord{LastAttempt: &attempt, Actions: []domain.RequestAction{{Type: domain.ActionReturn,
			By: domain.Actor{Email: "otro@example.com"}, At: changeFiledAt.Add(2 * time.Hour)}}},
			map[string]any{"outcome": "returned", "endorsement_id": nil, "by": map[string]any{"name": "otro@example.com", "role": "operations"},
				"at": "2026-09-16T14:00:00Z", "observations": "Corrige el documento de Lucía", "failed": []any{"identity_documents_valid"}}},
		{rejected, domain.RequestRecord{Actions: []domain.RequestAction{{Type: domain.ActionRejection, By: analystAndres,
			At: changeFiledAt.Add(3 * time.Hour)}}}, map[string]any{"outcome": "rejected", "endorsement_id": nil,
			"by": map[string]any{"name": "Andrés Ruiz", "role": "operations"}, "at": "2026-09-16T15:00:00Z", "observations": "No procede",
			"failed": []any{}}},
	} {
		d := detailOf(tc.r)
		d.Record = tc.rec
		svc := &fakeReview{detail: d}

		body := decodeBody(t, callEndorsements(t, svc, http.MethodGet, "/v1/policies/endorsement-requests/SOL-MOD-2026-0042", analystToken, ""))

		assert.Equal(t, tc.want, body["decision"], tc.r.Status)
	}
}

func TestApplyingAnswersTheEndorsementOrTheAttempt(t *testing.T) {
	applied := beneficiariesRequest()
	applied.Status = domain.ChangeRequestApplied
	svc := &fakeReview{apply: endorsements.ApplyResult{Outcome: domain.OutcomeApplied, Request: applied,
		Endorsement: &domain.Endorsement{ID: "END-2026-0102", Version: 2, By: analystAndres, At: changeFiledAt},
		Validations: domain.Validations{{Code: domain.ValidationRequesterIsHolder, Outcome: domain.ValidationPassed}}}}

	rec := callEndorsements(t, svc, http.MethodPost, "/v1/policies/endorsement-requests/SOL-MOD-2026-0042/application", analystToken, "")

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	body := decodeBody(t, rec)
	assert.Equal(t, "applied", body["outcome"])
	assert.Equal(t, map[string]any{"endorsement_id": "END-2026-0102", "version": 2.0, "applied_at": "2026-09-16T12:00:00Z",
		"by": map[string]any{"name": "Andrés Ruiz", "role": "operations"}}, body["endorsement"])
	assert.Nil(t, body["attempt"])

	notApplied := beneficiariesRequest()
	notApplied.Status = domain.ChangeRequestNotApplied
	svc.apply = endorsements.ApplyResult{Outcome: domain.OutcomeNotApplied, Request: notApplied,
		Attempt: &domain.EndorsementAttempt{By: analystAndres, At: changeFiledAt,
			Validations: domain.Validations{{Code: domain.ValidationSharesTotal100, Outcome: domain.ValidationFailed}}}}
	body = decodeBody(t, callEndorsements(t, svc, http.MethodPost, "/v1/policies/endorsement-requests/SOL-MOD-2026-0042/application",
		analystToken, ""))
	assert.Equal(t, "not_applied", body["outcome"])
	assert.Equal(t, []any{"shares_total_100"}, body["attempt"].(map[string]any)["failed"])
	assert.Nil(t, body["endorsement"])
}

func TestTheErrorsOfTheReviewHaveTheirCodes(t *testing.T) {
	inReview := beneficiariesRequest()
	inReview.Status = domain.ChangeRequestApplied
	for _, tc := range []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{"decided", &endorsements.DecidedError{Request: inReview, Cause: ports.ErrRequestAlreadyDecided}, http.StatusConflict,
			"request_already_decided"},
		{"not returnable", &endorsements.DecidedError{Request: inReview, Cause: ports.ErrNotReturnable}, http.StatusConflict, "not_returnable"},
		{"policy changed", ports.ErrPolicyChanged, http.StatusConflict, "policy_changed"},
		{"unverifiable", &endorsements.UnverifiableError{Codes: []domain.ValidationCode{domain.ValidationIdentityDocuments}},
			http.StatusServiceUnavailable, "validation_unverifiable"},
		{"observations", domain.ErrEndorsementObservations, http.StatusUnprocessableEntity, "observations_required"},
		{"no request", ports.ErrChangeRequestNotFound, http.StatusNotFound, "change_request_not_found"},
		{"no policy", ports.ErrPolicyNotFound, http.StatusNotFound, "policy_not_found"},
		{"query", domain.ErrInvalidInboxQuery, http.StatusBadRequest, "out_of_range"},
		{"anything else", errors.New("database down"), http.StatusServiceUnavailable, "endorsement_unavailable"},
	} {
		svc := &fakeReview{err: tc.err}

		rec := callEndorsements(t, svc, http.MethodPost, "/v1/policies/endorsement-requests/SOL-MOD-2026-0042/rejection", analystToken,
			`{"observations":"No procede el cambio"}`)

		assert.Equal(t, tc.status, rec.Code, tc.name)
		body := decodeBody(t, rec)
		assert.Equal(t, tc.code, body["error"], tc.name)
		switch tc.name {
		case "decided":
			assert.Equal(t, "applied", body["status"], "the client shows the status the winner left")
		case "unverifiable":
			assert.Equal(t, []any{"identity_documents_valid"}, body["unverifiable"])
		}
	}
}

func TestARejectionCarriesOnlyItsObservations(t *testing.T) {
	svc := &fakeReview{request: beneficiariesRequest()}

	rec := callEndorsements(t, svc, http.MethodPost, "/v1/policies/endorsement-requests/SOL-MOD-2026-0042/rejection", analystToken,
		`{"observations":"El parentesco no permite la designación"}`)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "El parentesco no permite la designación", svc.gotObs)

	for _, body := range []string{`{not json`, `{"observations":"x","otro":1}`} {
		rec := callEndorsements(t, svc, http.MethodPost, "/v1/policies/endorsement-requests/SOL-MOD-2026-0042/return", analystToken, body)
		assert.Equal(t, http.StatusBadRequest, rec.Code, body)
	}
	for _, path := range []string{"/v1/policies/endorsement-requests/SOL%00", "/v1/policies/pol%0A1/endorsements"} {
		assert.Equal(t, http.StatusBadRequest, callEndorsements(t, svc, http.MethodGet, path, analystToken, "").Code, path)
	}
}

func TestTheHistoryListsEachEntryWithItsChangeAndAuthor(t *testing.T) {
	amount := beneficiariesRequest()
	amount.Type = domain.ChangeInsuredAmount
	amount.Previous, amount.Requested = domain.ChangeContent{InsuredAmount: 250_000_000}, domain.ChangeContent{InsuredAmount: 300_000_000}
	svc := &fakeReview{policy: domain.Policy{ID: "pol-acc-1", Holder: domain.Applicant{FullName: "Carolina Pérez Ruiz", DocumentID: "1020345678"}},
		history: []domain.EndorsementHistoryEntry{
			{Outcome: domain.OutcomeNotApplied, At: changeFiledAt.Add(time.Hour), Request: beneficiariesRequest(),
				Failed: []domain.ValidationCode{domain.ValidationIdentityDocuments}, By: analystAndres},
			{Outcome: domain.OutcomeApplied, At: changeFiledAt, EndorsementID: "END-2026-0102", Request: amount,
				PreviousPremium: 68_500, NewPremium: 82_200, Currency: "COP", By: analystAndres},
		}}

	rec := callEndorsements(t, svc, http.MethodGet, "/v1/policies/pol-acc-1/endorsements", analystToken, "")

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	body := decodeBody(t, rec)
	entries := body["entries"].([]any)
	require.Len(t, entries, 2)
	first, second := entries[0].(map[string]any), entries[1].(map[string]any)
	assert.Equal(t, "not_applied", first["outcome"])
	assert.Nil(t, first["endorsement_id"])
	assert.Equal(t, []any{"identity_documents_valid"}, first["failed"])
	assert.Len(t, first["changes"], 2)
	assert.Equal(t, "END-2026-0102", second["endorsement_id"])
	assert.Equal(t, map[string]any{"previous": 68_500.0, "new": 82_200.0, "currency": "COP"}, second["premium"])
	assert.Equal(t, []any{map[string]any{"field": "insured_amount", "previous": 250_000_000.0, "requested": 300_000_000.0}}, second["changes"])
	assert.Equal(t, map[string]any{"name": "Andrés Ruiz", "role": "operations"}, second["by"])

	svc.history = []domain.EndorsementHistoryEntry{}
	body = decodeBody(t, callEndorsements(t, svc, http.MethodGet, "/v1/policies/pol-acc-1/endorsements", analystToken, ""))
	assert.Equal(t, []any{}, body["entries"], "a policy without endorsements answers an empty history")
}

func TestTheEndorsementSegmentDoesNotCoverTheRoutesOfAPolicy(t *testing.T) {
	review := &fakeReview{page: domain.EndorsementInboxPage{Page: 1, PageSize: 20}}
	changes := &fakeChangeRequests{page: domain.ChangeRequestPage{Page: 1, PageSize: 20}}
	renewals := &fakeRenewals{err: ports.ErrPolicyNotFound}
	expiring := &fakeExpiringPolicies{page: expiringPage(t)}
	router := httpadapter.NewRouter(&fakeResolver{}, httpadapter.WithObservability("origination"),
		httpadapter.WithPolicies(renewals, tokenVerifier{}), httpadapter.WithExpiringPolicies(expiring, tokenVerifier{}),
		httpadapter.WithChangeRequests(changes, tokenVerifier{}), httpadapter.WithEndorsementReview(review, tokenVerifier{}))
	call := func(method, path, token string) int {
		req := httptest.NewRequest(method, path, strings.NewReader(`{}`))
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec.Code
	}

	assert.Equal(t, http.StatusOK, call(http.MethodGet, "/v1/policies/endorsement-requests", analystToken))
	assert.Equal(t, http.StatusOK, call(http.MethodGet, "/v1/policies/change-requests", "insured:sub-1"))
	assert.Equal(t, http.StatusOK, call(http.MethodGet, "/v1/policies/expiring", "advisor:laura"))
	assert.Equal(t, http.StatusNotFound, call(http.MethodGet, "/v1/policies/pol-1/renewal", "insured:sub-1"), "reaches the renewal")
	assert.Equal(t, http.StatusOK, call(http.MethodGet, "/v1/policies/pol-1/endorsements", analystToken))
	assert.Equal(t, "pol-1", review.gotID)
	assert.Equal(t, http.StatusOK, call(http.MethodGet, "/v1/policies/endorsement-requests/SOL-MOD-2026-0001", analystToken))
	assert.Equal(t, "SOL-MOD-2026-0001", review.gotID)
	assert.Equal(t, http.StatusForbidden, call(http.MethodGet, "/v1/policies/change-requests/SOL-MOD-2026-0001", analystToken),
		"the routes of the insured stay theirs")
}
