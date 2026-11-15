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

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/security"
	httpadapter "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/http"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/endorsements"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
)

var changeFiledAt = time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)

type fakeChangeRequests struct {
	current   endorsements.Current
	request   domain.ChangeRequest
	page      domain.ChangeRequestPage
	err       error
	calls     int
	gotPolicy string
	gotID     string
	gotInput  domain.ChangeInput
	gotFilter domain.ChangeRequestFilter
}

func (f *fakeChangeRequests) CurrentTerms(_ context.Context, policyID string) (endorsements.Current, error) {
	f.calls++
	f.gotPolicy = policyID
	return f.current, f.err
}

func (f *fakeChangeRequests) File(_ context.Context, policyID string, in domain.ChangeInput) (domain.ChangeRequest, error) {
	f.calls++
	f.gotPolicy, f.gotInput = policyID, in
	return f.request, f.err
}

func (f *fakeChangeRequests) List(_ context.Context, filter domain.ChangeRequestFilter) (domain.ChangeRequestPage, error) {
	f.calls++
	f.gotFilter = filter
	return f.page, f.err
}

func (f *fakeChangeRequests) Get(_ context.Context, id string) (domain.ChangeRequest, error) {
	f.calls++
	f.gotID = id
	return f.request, f.err
}

func callChangeRequests(t *testing.T, svc *fakeChangeRequests, method, path, token, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	httpadapter.NewRouter(&fakeResolver{}, httpadapter.WithObservability("origination"),
		httpadapter.WithChangeRequests(svc, tokenVerifier{})).ServeHTTP(rec, req)
	return rec
}

func sp(s string) *string { return &s }

func beneficiariesRequest() domain.ChangeRequest {
	return domain.ChangeRequest{ID: "SOL-MOD-2026-0042", PolicyID: "pol-acc-1", CustomerID: "sub-1", Type: domain.ChangeBeneficiaries,
		Channel: domain.ChannelWeb, RequestedBy: domain.Requester{Subject: "sub-1", Email: "carolina@example.com"},
		FiledAt: changeFiledAt, Status: domain.ChangeRequestFiled, StatusChangedAt: changeFiledAt,
		Previous: domain.ChangeContent{Beneficiaries: []domain.Beneficiary{},
			Onerous: &domain.OnerousBeneficiary{PartnerID: "aliado-banco-1", Name: "Banco Aliado", Basis: domain.OnerousBasisOutstandingBalance}},
		Requested: domain.ChangeContent{Beneficiaries: []domain.Beneficiary{
			{FullName: "Mateo Pérez Gómez", Relationship: domain.RelationshipChild, DocumentType: domain.DocumentTI, DocumentNumber: "1140223001", ShareBasisPoints: 6000},
			{FullName: "Lucía Pérez Gómez", Relationship: domain.RelationshipChild, DocumentType: domain.DocumentTI, DocumentNumber: "1142998310", ShareBasisPoints: 4000},
		}}}
}

const beneficiariesJSON = `{"change_type":"beneficiaries","channel":"web","beneficiaries":[
	{"full_name":"Mateo Pérez Gómez","relationship":"child","document_type":"TI","document_number":"1140223001","share_percent":60},
	{"full_name":"Lucía Pérez Gómez","relationship":"child","document_type":"TI","document_number":"1142998310","share_percent":40}]}`

func TestChangeRequestsAreOnlyForTheInsured(t *testing.T) {
	routes := []struct{ method, path, body string }{
		{http.MethodGet, "/v1/policies/pol-acc-1/current-terms", ""},
		{http.MethodPost, "/v1/policies/pol-acc-1/change-requests", beneficiariesJSON},
		{http.MethodGet, "/v1/policies/change-requests", ""},
		{http.MethodGet, "/v1/policies/change-requests/SOL-MOD-2026-0042", ""},
	}
	svc := &fakeChangeRequests{}
	for _, r := range routes {
		assert.Equal(t, http.StatusUnauthorized, callChangeRequests(t, svc, r.method, r.path, "", r.body).Code, r.path)
		for _, token := range []string{"advisor:laura", "operations:ops", "partner:p1"} {
			assert.Equal(t, http.StatusForbidden, callChangeRequests(t, svc, r.method, r.path, token, r.body).Code, "%s %s", token, r.path)
		}
	}
	assert.Zero(t, svc.calls, "nobody but an insured reaches the use case")

	svc.err = security.ErrForbidden
	for _, r := range routes {
		rec := callChangeRequests(t, svc, r.method, r.path, "insured:sub-2", r.body)
		assert.Equal(t, http.StatusForbidden, rec.Code, "another insured's policy or request: %s", r.path)
		assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
	}
}

func TestTheCurrentTermsAnswerThePolicyTheTermsTheOpenRequestsAndTheLimits(t *testing.T) {
	start := changeFiledAt.AddDate(0, -2, 0)
	svc := &fakeChangeRequests{current: endorsements.Current{
		Policy: domain.Policy{ID: "pol-acc-1", CustomerID: "sub-1", Holder: domain.Applicant{FullName: "Carolina Pérez"},
			CoverageStart: start, CoverageEnd: start.AddDate(1, 0, 0)},
		InForce: true,
		Terms: domain.PolicyTerms{InsuredAmount: 68_000_000, Currency: "COP", Beneficiaries: []domain.Beneficiary{},
			Onerous: &domain.OnerousBeneficiary{Basis: domain.OnerousBasisOutstandingBalance},
			Contact: domain.ContactData{Email: sp("carolina@example.com")}},
		Open: []domain.ChangeRequest{{ID: "SOL-MOD-2026-0041", Type: domain.ChangeInsuredAmount, Status: domain.ChangeRequestInReview,
			FiledAt: changeFiledAt}},
	}}

	rec := callChangeRequests(t, svc, http.MethodGet, "/v1/policies/pol-acc-1/current-terms", "insured:sub-1", "")

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "pol-acc-1", svc.gotPolicy)
	body := decodeBody(t, rec)
	assert.Equal(t, map[string]any{"policy_id": "pol-acc-1", "line": "life", "holder_name": "Carolina Pérez",
		"coverage_start": "2026-07-16T12:00:00Z", "coverage_end": "2027-07-16T12:00:00Z", "in_force": true}, body["policy"])
	assert.Equal(t, map[string]any{"insured_amount": 68_000_000.0, "currency": "COP", "beneficiaries": []any{},
		"onerous_beneficiary": map[string]any{"partner_id": nil, "name": nil, "basis": "outstanding_balance"},
		"personal_data":       map[string]any{"address": nil, "email": "carolina@example.com", "phone": nil}}, body["terms"])
	assert.Equal(t, []any{map[string]any{"request_id": "SOL-MOD-2026-0041", "change_type": "insured_amount", "status": "in_review",
		"filed_at": "2026-09-16T12:00:00Z"}}, body["open_requests"])
	limits := body["limits"].(map[string]any)
	assert.Equal(t, 10_000_000.0, limits["insured_amount_min"])
	assert.Equal(t, 2_000_000_000.0, limits["insured_amount_max"])
	assert.Equal(t, 10.0, limits["max_beneficiaries"])
	assert.Equal(t, []any{"CC", "TI", "RC", "CE", "PA"}, limits["document_types"])
	assert.Len(t, limits["relationships"], 6)
}

func TestFilingAnswers201WithTheLocationAndTheDifference(t *testing.T) {
	svc := &fakeChangeRequests{request: beneficiariesRequest()}

	rec := callChangeRequests(t, svc, http.MethodPost, "/v1/policies/pol-acc-1/change-requests", "insured:sub-1", beneficiariesJSON)

	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	assert.Equal(t, "/v1/policies/change-requests/SOL-MOD-2026-0042", rec.Header().Get("Location"))
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
	assert.Equal(t, "pol-acc-1", svc.gotPolicy)
	assert.Equal(t, "beneficiaries", svc.gotInput.Type)
	assert.Equal(t, "web", svc.gotInput.Channel)
	require.Len(t, svc.gotInput.Beneficiaries, 2)
	assert.Equal(t, 60.0, *svc.gotInput.Beneficiaries[0].SharePercent)

	body := decodeBody(t, rec)
	assert.Equal(t, "SOL-MOD-2026-0042", body["request_id"])
	assert.Equal(t, "filed", body["status"])
	assert.Nil(t, body["decision_reason"])
	assert.Equal(t, map[string]any{"email": "carolina@example.com", "name": nil}, body["requested_by"])
	assert.Equal(t, map[string]any{"beneficiaries": []any{},
		"onerous_beneficiary": map[string]any{"partner_id": "aliado-banco-1", "name": "Banco Aliado", "basis": "outstanding_balance"}},
		body["previous"])
	requested := body["requested"].(map[string]any)
	assert.Equal(t, map[string]any{"full_name": "Mateo Pérez Gómez", "relationship": "child", "document_type": "TI",
		"document_number": "1140223001", "share_percent": 60.0}, requested["beneficiaries"].([]any)[0])
	assert.NotContains(t, requested, "onerous_beneficiary")
	changes := body["changes"].([]any)
	require.Len(t, changes, 2)
	assert.Equal(t, "beneficiaries", changes[0].(map[string]any)["field"])
	assert.Nil(t, changes[0].(map[string]any)["previous"], "a new beneficiary")
}

func TestTheOtherTypesAnswerOnlyTheirMembers(t *testing.T) {
	amount := domain.ChangeRequest{ID: "SOL-MOD-2026-0043", Type: domain.ChangeInsuredAmount, Status: domain.ChangeRequestFiled,
		Previous: domain.ChangeContent{InsuredAmount: 68_000_000, Currency: "COP"}, Requested: domain.ChangeContent{InsuredAmount: 80_000_000, Currency: "COP"},
		RequestedBy: domain.Requester{Email: "carolina@example.com", Name: "Carolina Pérez"}}
	svc := &fakeChangeRequests{request: amount}
	body := decodeBody(t, callChangeRequests(t, svc, http.MethodGet, "/v1/policies/change-requests/SOL-MOD-2026-0043", "insured:sub-1", ""))
	assert.Equal(t, map[string]any{"insured_amount": 68_000_000.0, "currency": "COP"}, body["previous"])
	assert.Equal(t, []any{map[string]any{"field": "insured_amount", "previous": 68_000_000.0, "requested": 80_000_000.0}}, body["changes"])
	assert.Equal(t, "Carolina Pérez", body["requested_by"].(map[string]any)["name"])

	svc.request = domain.ChangeRequest{ID: "SOL-MOD-2026-0044", Type: domain.ChangePersonalData, Status: domain.ChangeRequestNotApplied,
		DecisionReason: "El teléfono no es del titular",
		Previous:       domain.ChangeContent{Contact: domain.ContactData{Email: sp("carolina@example.com")}},
		Requested:      domain.ChangeContent{Contact: domain.ContactData{Phone: sp("3001234567")}}}
	body = decodeBody(t, callChangeRequests(t, svc, http.MethodGet, "/v1/policies/change-requests/SOL-MOD-2026-0044", "insured:sub-1", ""))
	assert.Equal(t, map[string]any{"personal_data": map[string]any{"address": nil, "email": nil, "phone": "3001234567"}}, body["requested"])
	assert.Equal(t, "not_applied", body["status"], "the stored status; the client shows it as rejected with the reason")
	assert.Equal(t, "El teléfono no es del titular", body["decision_reason"])
	assert.Equal(t, []any{map[string]any{"field": "personal_data.phone", "previous": nil, "requested": "3001234567"}}, body["changes"])
	assert.Equal(t, "SOL-MOD-2026-0044", svc.gotID)
}

func TestAMalformedBodyIsRejectedBeforeTheUseCase(t *testing.T) {
	svc := &fakeChangeRequests{}
	for name, body := range map[string]string{
		"not json":        `{"change_type":`,
		"onerous":         `{"change_type":"beneficiaries","channel":"web","beneficiaries":[],"onerous_beneficiary":{"partner_id":"x"}}`,
		"unknown member":  `{"change_type":"insured_amount","channel":"web","insured_amount":80000000,"premium":1}`,
		"nested unknown":  `{"change_type":"beneficiaries","channel":"web","beneficiaries":[{"full_name":"Ana","nit":"1"}]}`,
		"another type":    `{"change_type":"beneficiaries","channel":"web","beneficiaries":[],"insured_amount":80000000}`,
		"wrong json type": `{"change_type":"insured_amount","channel":"web","insured_amount":"80000000"}`,
	} {
		rec := callChangeRequests(t, svc, http.MethodPost, "/v1/policies/pol-acc-1/change-requests", "insured:sub-1", body)
		assert.Equal(t, http.StatusBadRequest, rec.Code, name)
		assert.Equal(t, "invalid_request", decodeBody(t, rec)["error"], name)
	}
	assert.Zero(t, svc.calls)
}

func TestTheErrorsOfTheUseCaseHaveTheirAnswers(t *testing.T) {
	for _, tc := range []struct {
		err    error
		status int
		code   string
	}{
		{&domain.ValidationError{Fields: []domain.FieldError{{Field: "beneficiaries", Code: domain.CodeShareTotalNot100},
			{Field: "beneficiaries[0].document_number", Code: domain.CodeInvalidFormat}}}, http.StatusUnprocessableEntity, "validation_failed"},
		{domain.ErrPolicyNotInForce, http.StatusConflict, "policy_not_in_force"},
		{ports.ErrPolicyNotFound, http.StatusNotFound, "policy_not_found"},
		{ports.ErrChangeRequestNotFound, http.StatusNotFound, "change_request_not_found"},
		{domain.ErrInvalidChangeRequestQuery, http.StatusBadRequest, "invalid_request"},
		{errors.New("database down"), http.StatusServiceUnavailable, "change_request_unavailable"},
	} {
		svc := &fakeChangeRequests{err: tc.err}
		rec := callChangeRequests(t, svc, http.MethodPost, "/v1/policies/pol-acc-1/change-requests", "insured:sub-1", beneficiariesJSON)
		assert.Equal(t, tc.status, rec.Code, tc.code)
		assert.Equal(t, tc.code, decodeBody(t, rec)["error"])
	}

	svc := &fakeChangeRequests{err: &domain.ValidationError{Fields: []domain.FieldError{{Field: "beneficiaries", Code: domain.CodeShareTotalNot100},
		{Field: "beneficiaries[0].document_number", Code: domain.CodeInvalidFormat}}}}
	body := decodeBody(t, callChangeRequests(t, svc, http.MethodPost, "/v1/policies/pol-acc-1/change-requests", "insured:sub-1", beneficiariesJSON))
	assert.Len(t, body["fields"], 2, "every field at once")

	svc = &fakeChangeRequests{err: &ports.ChangeRequestOpenError{RequestID: "SOL-MOD-2026-0041"}}
	rec := callChangeRequests(t, svc, http.MethodPost, "/v1/policies/pol-acc-1/change-requests", "insured:sub-1", beneficiariesJSON)
	assert.Equal(t, http.StatusConflict, rec.Code)
	body = decodeBody(t, rec)
	assert.Equal(t, "change_request_open", body["error"])
	assert.Equal(t, "SOL-MOD-2026-0041", body["request_id"], "the client links to the open request")
}

func TestTheListReadsItsQueryAndAnswersTheIndicators(t *testing.T) {
	r := beneficiariesRequest()
	r.Status, r.DecisionReason = domain.ChangeRequestRejected, "Falta el documento"
	svc := &fakeChangeRequests{page: domain.ChangeRequestPage{Items: []domain.ChangeRequest{r}, Total: 21, Page: 2, PageSize: 10,
		Indicators: domain.ChangeRequestIndicators{Open: 1, LatestOpen: &domain.ChangeRequestRef{ID: "SOL-MOD-2026-0041",
			Type: domain.ChangeInsuredAmount, FiledAt: changeFiledAt, StatusChangedAt: changeFiledAt}, Rejected: 1},
		Policies: []string{"pol-acc-1"}}}

	rec := callChangeRequests(t, svc, http.MethodGet,
		"/v1/policies/change-requests?status=rejected&policy_id=pol-acc-1&filed_from=2026-09-01&page=2&page_size=10", "insured:sub-1", "")

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, domain.ChangeRequestRejected, svc.gotFilter.Status)
	assert.Equal(t, "pol-acc-1", svc.gotFilter.PolicyID)
	assert.Equal(t, "2026-09-01", svc.gotFilter.FiledFrom.Format(domain.CivilDayLayout))
	assert.Equal(t, 2, svc.gotFilter.Page)
	assert.Equal(t, 10, svc.gotFilter.PageSize)
	body := decodeBody(t, rec)
	assert.Equal(t, 21.0, body["total_items"])
	assert.Equal(t, 3.0, body["total_pages"])
	assert.Equal(t, []any{"pol-acc-1"}, body["policies"])
	item := body["items"].([]any)[0].(map[string]any)
	assert.Equal(t, "rejected", item["status"])
	assert.Equal(t, "Falta el documento", item["decision_reason"])
	assert.NotContains(t, item, "requested", "the list carries no beneficiary data")
	ind := body["indicators"].(map[string]any)
	assert.Equal(t, map[string]any{"count": 1.0, "latest": map[string]any{"request_id": "SOL-MOD-2026-0041", "change_type": "insured_amount",
		"filed_at": "2026-09-16T12:00:00Z", "status_changed_at": "2026-09-16T12:00:00Z"}}, ind["open"])
	assert.Equal(t, map[string]any{"count": 0.0, "latest": nil}, ind["applied_this_year"])
	assert.Equal(t, map[string]any{"count": 1.0, "latest": nil}, ind["rejected"])
}

func TestAnUnreadableQueryNamesEveryParameter(t *testing.T) {
	svc := &fakeChangeRequests{}

	rec := callChangeRequests(t, svc, http.MethodGet,
		"/v1/policies/change-requests?status=not_applied&filed_from=16/09/2026&page=0&page_size=x&policy_id="+strings.Repeat("p", 129),
		"insured:sub-1", "")

	require.Equal(t, http.StatusBadRequest, rec.Code)
	body := decodeBody(t, rec)
	assert.Equal(t, "validation_failed", body["error"])
	assert.ElementsMatch(t, []any{
		map[string]any{"field": "status", "code": "invalid_format"},
		map[string]any{"field": "policy_id", "code": "invalid_format"},
		map[string]any{"field": "filed_from", "code": "invalid_format"},
		map[string]any{"field": "page", "code": "out_of_range"},
		map[string]any{"field": "page_size", "code": "invalid_format"},
	}, body["fields"])
	assert.Zero(t, svc.calls)

	rec = callChangeRequests(t, svc, http.MethodGet, "/v1/policies/change-requests?page_size=51", "insured:sub-1", "")
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestTheFixedSegmentDoesNotCoverTheRoutesOfAPolicy(t *testing.T) {
	changes := &fakeChangeRequests{page: domain.ChangeRequestPage{Page: 1, PageSize: 20}}
	renewals := &fakeRenewals{err: ports.ErrPolicyNotFound}
	expiring := &fakeExpiringPolicies{page: expiringPage(t)}
	router := httpadapter.NewRouter(&fakeResolver{}, httpadapter.WithObservability("origination"),
		httpadapter.WithPolicies(renewals, tokenVerifier{}), httpadapter.WithExpiringPolicies(expiring, tokenVerifier{}),
		httpadapter.WithChangeRequests(changes, tokenVerifier{}))
	call := func(method, path, token string) int {
		req := httptest.NewRequest(method, path, strings.NewReader(`{}`))
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec.Code
	}

	assert.Equal(t, http.StatusOK, call(http.MethodGet, "/v1/policies/change-requests", "insured:sub-1"))
	assert.Equal(t, http.StatusOK, call(http.MethodGet, "/v1/policies/expiring", "advisor:laura"))
	assert.Equal(t, http.StatusNotFound, call(http.MethodGet, "/v1/policies/pol-1/renewal", "insured:sub-1"), "reaches the renewal")
	assert.Equal(t, "pol-1", renewals.gotID)
	changes.err = ports.ErrChangeRequestNotFound
	assert.Equal(t, http.StatusNotFound, call(http.MethodGet, "/v1/policies/change-requests/SOL-MOD-2026-0001", "insured:sub-1"))
	assert.Equal(t, "SOL-MOD-2026-0001", changes.gotID)
	changes.err = ports.ErrPolicyNotFound
	assert.Equal(t, http.StatusNotFound, call(http.MethodGet, "/v1/policies/pol-1/current-terms", "insured:sub-1"))
	assert.Equal(t, "pol-1", changes.gotPolicy)
}
