package httpadapter_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/security"
	httpadapter "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/adapters/http"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/adapters/memory"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/filing"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/ports"
)

// The routes of the web filing and the reads of the insured's claims
// (HU-W2.1-01).

const claimID = "8f14e45f-ceea-467a-9f3a-5a2d0b1c7e90"

// tokens verifies the tokens of the tests: each one is a caller.
type tokens map[string]security.Claims

func (v tokens) Verify(_ context.Context, raw string) (security.Claims, error) {
	c, ok := v[raw]
	if !ok {
		return security.Claims{}, errors.New("bad token")
	}
	return c, nil
}

var callers = tokens{
	"insured":    {Subject: "sub-carolina", Roles: []security.Role{security.RoleInsured}},
	"other":      {Subject: "sub-someone", Roles: []security.Role{security.RoleInsured}},
	"advisor":    {Subject: "sub-advisor", Roles: []security.Role{security.RoleAdvisor}},
	"operations": {Subject: "sub-ops", Roles: []security.Role{security.RoleOperations}},
	"partner":    {Subject: "sub-partner", Roles: []security.Role{security.RolePartner}},
}

// fakeWeb is the web filing of the tests.
type fakeWeb struct {
	options filing.FilingOptions
	claim   domain.ClaimRecord
	created bool
	err     error
	calls   int

	gotSubject, gotKey string
	gotRequest         domain.WebFilingRequest
}

func (f *fakeWeb) Options(_ context.Context, subject string) (filing.FilingOptions, error) {
	f.calls++
	f.gotSubject = subject
	return f.options, f.err
}

func (f *fakeWeb) File(_ context.Context, subject, key string, r domain.WebFilingRequest) (domain.ClaimRecord, bool, error) {
	f.calls++
	f.gotSubject, f.gotKey, f.gotRequest = subject, key, r
	return f.claim, f.created, f.err
}

// fakeUploads is the supports use case of the tests.
type fakeUploads struct {
	err        error
	gotSubject string
	gotID      string
	gotRequest domain.SupportUploadRequest
	calls      int
}

func (f *fakeUploads) Declare(_ context.Context, subject string, r domain.SupportUploadRequest) (filing.SupportUpload, error) {
	f.calls++
	f.gotSubject, f.gotRequest = subject, r
	if f.err != nil {
		return filing.SupportUpload{}, f.err
	}
	format, _ := domain.FormatOf(r.ContentType)
	return filing.SupportUpload{
		File: domain.SupportFile{ID: "sup-" + strings.Repeat("a", 32), OwnerSubject: subject, FileName: r.FileName,
			Format: format, ByteSize: r.ByteSize, SHA256: r.SHA256, UploadedAt: filedAt},
		URL:       "https://bucket.test/supports/" + subject + "/sup-a?X-Amz-Signature=abc",
		Headers:   map[string]string{"Content-Type": r.ContentType},
		ExpiresAt: filedAt.Add(10 * time.Minute),
	}, nil
}

func (f *fakeUploads) Confirm(_ context.Context, subject, id string) (domain.SupportFile, error) {
	f.calls++
	f.gotSubject, f.gotID = subject, id
	if f.err != nil {
		return domain.SupportFile{}, f.err
	}
	return domain.SupportFile{ID: id, OwnerSubject: subject, FileName: "croquis.pdf", Format: domain.FormatPDF,
		ByteSize: 482113, SHA256: strings.Repeat("b", 64), UploadedAt: filedAt, ConfirmedAt: filedAt.Add(time.Minute)}, nil
}

// fakeReads is the reads of the tests.
type fakeReads struct {
	page    filing.ClaimPage
	claim   domain.ClaimRecord
	pdf     []byte
	err     error
	gotList filing.ClaimQuery
	gotID   string
	links   []filing.EvidenceLink
}

func (f *fakeReads) EvidenceLinks(context.Context, domain.ClaimRecord) []filing.EvidenceLink {
	return f.links
}

func (f *fakeReads) List(_ context.Context, _ string, q filing.ClaimQuery) (filing.ClaimPage, error) {
	f.gotList = q
	return f.page, f.err
}

func (f *fakeReads) Get(_ context.Context, id string) (domain.ClaimRecord, error) {
	f.gotID = id
	return f.claim, f.err
}

func (f *fakeReads) Receipt(_ context.Context, id string) (domain.ClaimRecord, []byte, error) {
	f.gotID = id
	return f.claim, f.pdf, f.err
}

func webRouter(web *fakeWeb, uploads *fakeUploads, reads *fakeReads) http.Handler {
	return httpadapter.NewRouter(
		httpadapter.WithClaimReads(reads, callers),
		httpadapter.WithWebFilings(web, uploads, callers))
}

func do(h http.Handler, method, path, token string, body io.Reader, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, body)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func decode(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &m), rec.Body.String())
	return m
}

func webClaim() domain.ClaimRecord {
	policy := domain.Policy{ID: "AU-2025-118204", Line: domain.LineAuto, Product: "Autos", InsuredObject: "KJT-482"}
	file := domain.SupportFile{ID: "sup-" + strings.Repeat("c", 32), FileName: "croquis.pdf", Format: domain.FormatPDF, ByteSize: 482113}
	return domain.ClaimRecord{
		Claim: domain.Claim{
			ID: claimID, FilingNumber: "SIN-2026-01712", Subject: "sub-carolina", PolicyID: "AU-2025-118204",
			IncidentType: domain.IncidentOther, OccurredAt: time.Date(2026, 9, 14, 23, 40, 0, 0, time.UTC),
			Place: "Calle 80", Description: "Un golpe por detrás.", Status: domain.StatusActive, FiledAt: filedAt,
		},
		Channel: domain.ChannelWeb, Policy: &policy, CatalogVersion: domain.CatalogVersionV1,
		Supports: []domain.AttachedSupport{{RequirementCode: "supporting_document", File: file}},
	}
}

// --- Authorization matrix ---------------------------------------------------

func TestEveryNewRouteNeedsAnInsured(t *testing.T) {
	routes := []struct{ method, path string }{
		{http.MethodGet, "/v1/claims"},
		{http.MethodGet, "/v1/claims/" + claimID},
		{http.MethodGet, "/v1/claims/" + claimID + "/filing-receipt"},
		{http.MethodGet, "/v1/claims/filing-options"},
		{http.MethodPost, "/v1/claims/supports"},
		{http.MethodPost, "/v1/claims/supports/sup-" + strings.Repeat("a", 32) + "/confirmation"},
		{http.MethodPost, "/v1/claims/web-filings"},
	}
	for _, rt := range routes {
		for token, want := range map[string]int{
			"": http.StatusUnauthorized, "forged": http.StatusUnauthorized,
			"advisor": http.StatusForbidden, "operations": http.StatusForbidden, "partner": http.StatusForbidden,
		} {
			web, uploads, reads := &fakeWeb{}, &fakeUploads{}, &fakeReads{}
			rec := do(webRouter(web, uploads, reads), rt.method, rt.path, token, strings.NewReader("{}"), nil)

			assert.Equal(t, want, rec.Code, "%s %s with %q", rt.method, rt.path, token)
			assert.Zero(t, web.calls+uploads.calls, "the use case must not run")
			assert.Empty(t, reads.gotID)
		}
	}
}

// --- Filing options ---------------------------------------------------------

func TestTheFilingOptionsAreServed(t *testing.T) {
	catalog := domain.CatalogV1()
	collision, _ := catalog.Incident(domain.IncidentCollision)
	web := &fakeWeb{options: filing.FilingOptions{CatalogVersion: catalog.Version, Policies: []filing.PolicyOptions{{
		Policy: domain.Policy{ID: "AU-2025-118204", Line: domain.LineAuto, Product: "Autos", InsuredObject: "KJT-482",
			ValidFrom: time.Date(2025, 11, 1, 5, 0, 0, 0, time.UTC), ValidTo: time.Date(2026, 11, 1, 5, 0, 0, 0, time.UTC)},
		Incidents: []domain.IncidentOption{collision},
	}}}}

	rec := do(webRouter(web, &fakeUploads{}, &fakeReads{}), http.MethodGet, "/v1/claims/filing-options", "insured", nil, nil)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
	assert.Equal(t, "sub-carolina", web.gotSubject)
	assert.JSONEq(t, `{"catalog_version":"claim-catalog-v1","policies":[{
		"policy_id":"AU-2025-118204","line":"auto","product":"Autos","insured_object":"KJT-482",
		"valid_from":"2025-11-01T05:00:00Z","valid_to":"2026-11-01T05:00:00Z",
		"incident_types":[{"code":"collision","label":"Colisión con daños al vehículo","requirements":[
			{"code":"vehicle_photos","label":"Fotos del vehículo","required":true,"min_files":4,"max_files":10,"formats":["jpeg","png"]},
			{"code":"driver_document","label":"Documento del conductor","required":true,"min_files":1,"max_files":2,"formats":["pdf","jpeg","png"]},
			{"code":"traffic_report","label":"Informe de tránsito o croquis","required":true,"min_files":1,"max_files":3,"formats":["pdf","jpeg","png"]}]}]}]}`,
		rec.Body.String())
}

func TestNoPoliciesIsAnEmptyListNotNull(t *testing.T) {
	web := &fakeWeb{options: filing.FilingOptions{CatalogVersion: domain.CatalogVersionV1}}

	rec := do(webRouter(web, &fakeUploads{}, &fakeReads{}), http.MethodGet, "/v1/claims/filing-options", "insured", nil, nil)

	assert.JSONEq(t, `{"catalog_version":"claim-catalog-v1","policies":[]}`, rec.Body.String())
}

func TestThePolicyCoreDownIs503(t *testing.T) {
	web := &fakeWeb{err: fmt.Errorf("%w: timeout", ports.ErrPolicyDirectoryUnavailable)}
	h := webRouter(web, &fakeUploads{}, &fakeReads{})

	for _, rec := range []*httptest.ResponseRecorder{
		do(h, http.MethodGet, "/v1/claims/filing-options", "insured", nil, nil),
		do(h, http.MethodPost, "/v1/claims/web-filings", "insured", strings.NewReader(`{}`), map[string]string{"Idempotency-Key": "k"}),
	} {
		assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
		assert.Equal(t, "policy_directory_unavailable", decode(t, rec)["error"])
		assert.NotContains(t, rec.Body.String(), "timeout", "the cause is not detailed")
	}
}

// --- Supports ---------------------------------------------------------------

const declarationBody = `{"file_name":"croquis.pdf","content_type":"application/pdf","byte_size":482113,
	"sha256":"` + "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb" + `"}`

func declare(h http.Handler, body string) *httptest.ResponseRecorder {
	return do(h, http.MethodPost, "/v1/claims/supports", "insured", strings.NewReader(body),
		map[string]string{"Content-Type": "application/json"})
}

func TestASupportIsDeclaredAndTheSignedUploadReturned(t *testing.T) {
	uploads := &fakeUploads{}

	rec := declare(webRouter(&fakeWeb{}, uploads, &fakeReads{}), declarationBody)

	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
	assert.Equal(t, "sub-carolina", uploads.gotSubject)
	assert.Equal(t, "croquis.pdf", uploads.gotRequest.FileName)
	assert.Equal(t, int64(482113), uploads.gotRequest.ByteSize)
	m := decode(t, rec)
	assert.Equal(t, "application/pdf", m["content_type"])
	assert.Equal(t, "sup-"+strings.Repeat("a", 32), m["support_id"])
	assert.Contains(t, m["upload_url"], "https://bucket.test/supports/sub-carolina/")
	assert.Equal(t, map[string]any{"Content-Type": "application/pdf"}, m["upload_headers"])
	assert.Equal(t, "2026-10-23T15:10:00Z", m["expires_at"])
	assert.NotContains(t, m, "uploaded_at", "nothing was uploaded yet")
}

func TestTheDeclarationMustBeJSON(t *testing.T) {
	for name, body := range map[string]string{"not JSON": "data", "empty": "", "a list": "[1]"} {
		uploads := &fakeUploads{}
		rec := declare(webRouter(&fakeWeb{}, uploads, &fakeReads{}), body)

		assert.Equal(t, http.StatusBadRequest, rec.Code, name)
		assert.Equal(t, "invalid_request", decode(t, rec)["error"], name)
		assert.Zero(t, uploads.calls, name)
	}
}

func TestTheRefusalsOfADeclarationAreTranslated(t *testing.T) {
	for err, want := range map[error][2]any{
		domain.ErrUnsupportedFileType:    {http.StatusUnsupportedMediaType, "unsupported_file_type"},
		domain.ErrFileTooLarge:           {http.StatusRequestEntityTooLarge, "file_too_large"},
		domain.ErrEmptyFile:              {http.StatusUnprocessableEntity, "empty_file"},
		domain.ErrTooManyPendingSupports: {http.StatusTooManyRequests, "too_many_pending_supports"},
		errors.New("the bucket is down"): {http.StatusServiceUnavailable, "supports_unavailable"},
	} {
		rec := declare(webRouter(&fakeWeb{}, &fakeUploads{err: err}, &fakeReads{}), declarationBody)

		assert.Equal(t, want[0], rec.Code, err)
		assert.Equal(t, want[1], decode(t, rec)["error"], err)
		assert.NotContains(t, rec.Body.String(), "bucket is down", "the cause is not detailed")
	}
}

func TestAnInvalidDigestIsAValidationError(t *testing.T) {
	err := &domain.ValidationError{Fields: []domain.FieldError{{Field: "sha256", Code: domain.CodeInvalidFormat}}}

	rec := declare(webRouter(&fakeWeb{}, &fakeUploads{err: err}, &fakeReads{}), declarationBody)

	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	assert.JSONEq(t, `{"error":"validation_failed","message":"one or more fields are missing or invalid",
		"fields":[{"field":"sha256","code":"invalid_format"}]}`, rec.Body.String())
}

func confirm(h http.Handler, id string) *httptest.ResponseRecorder {
	return do(h, http.MethodPost, "/v1/claims/supports/"+id+"/confirmation", "insured", nil, nil)
}

func TestAnUploadIsConfirmed(t *testing.T) {
	uploads := &fakeUploads{}
	id := "sup-" + strings.Repeat("c", 32)

	rec := confirm(webRouter(&fakeWeb{}, uploads, &fakeReads{}), id)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "sub-carolina", uploads.gotSubject)
	assert.Equal(t, id, uploads.gotID)
	assert.JSONEq(t, `{"support_id":"`+id+`","file_name":"croquis.pdf","content_type":"application/pdf","byte_size":482113,
		"sha256":"`+strings.Repeat("b", 64)+`","confirmed_at":"2026-10-23T15:01:00Z"}`, rec.Body.String())
}

func TestTheRefusalsOfAConfirmationAreTranslated(t *testing.T) {
	for err, want := range map[error][2]any{
		domain.ErrSupportNotFound:        {http.StatusNotFound, "support_not_found"},
		domain.ErrSupportNotUploaded:     {http.StatusUnprocessableEntity, "support_not_uploaded"},
		domain.ErrSupportMismatch:        {http.StatusUnprocessableEntity, "support_mismatch"},
		domain.ErrUnsupportedFileType:    {http.StatusUnsupportedMediaType, "unsupported_file_type"},
		errors.New("the bucket is down"): {http.StatusServiceUnavailable, "supports_unavailable"},
	} {
		rec := confirm(webRouter(&fakeWeb{}, &fakeUploads{err: err}, &fakeReads{}), "sup-"+strings.Repeat("c", 32))

		assert.Equal(t, want[0], rec.Code, err)
		assert.Equal(t, want[1], decode(t, rec)["error"], err)
	}
}

func TestAnIdThatIsNotASupportIdIsNotLookedUp(t *testing.T) {
	uploads := &fakeUploads{}
	for _, id := range []string{"sup-1", "123", "sup-" + strings.Repeat("Z", 32), "..%2f"} {
		rec := confirm(webRouter(&fakeWeb{}, uploads, &fakeReads{}), id)

		assert.Equal(t, http.StatusNotFound, rec.Code, id)
	}
	assert.Zero(t, uploads.calls)
}

// --- Web filing -------------------------------------------------------------

const webFilingBody = `{"policy_id":"AU-2025-118204","incident_type":"collision","occurred_at":"2026-09-14T23:40:00Z",
	"place":"Calle 80","description":"Un golpe por detrás en el semáforo.",
	"supports":[{"requirement_code":"traffic_report","support_ids":["sup-1","sup-2"]}]}`

func fileWeb(h http.Handler, token, key, body string) *httptest.ResponseRecorder {
	headers := map[string]string{"Content-Type": "application/json"}
	if key != "" {
		headers["Idempotency-Key"] = key
	}
	return do(h, http.MethodPost, "/v1/claims/web-filings", token, strings.NewReader(body), headers)
}

func TestAWebFilingAnswersWithTheClaimDetail(t *testing.T) {
	web := &fakeWeb{claim: webClaim(), created: true}

	rec := fileWeb(webRouter(web, &fakeUploads{}, &fakeReads{}), "insured", "form-1", webFilingBody)

	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	assert.Equal(t, "sub-carolina", web.gotSubject)
	assert.Equal(t, "form-1", web.gotKey)
	assert.Equal(t, []domain.SupportGroup{{RequirementCode: "traffic_report", SupportIDs: []string{"sup-1", "sup-2"}}},
		web.gotRequest.Supports)
	assert.Equal(t, "2026-09-14T23:40:00Z", web.gotRequest.OccurredAt)
	assert.JSONEq(t, `{"claim_id":"`+claimID+`","filing_number":"SIN-2026-01712","status":"active","channel":"web",
		"policy":{"policy_id":"AU-2025-118204","line":"auto","product":"Autos","insured_object":"KJT-482"},
		"incident_type":"other","occurred_at":"2026-09-14T23:40:00Z","filed_at":"2026-10-23T15:00:00Z",
		"place":"Calle 80","description":"Un golpe por detrás.","evidence":[],
		"supports":{"required_total":1,"required_attached":1,"files":1,"items":[{"requirement_code":"supporting_document",
			"label":"Documento de soporte","files":[{"support_id":"sup-`+strings.Repeat("c", 32)+`","file_name":"croquis.pdf",
			"content_type":"application/pdf","byte_size":482113}]}]}}`, rec.Body.String())
}

func TestARepeatedWebFilingIs200(t *testing.T) {
	rec := fileWeb(webRouter(&fakeWeb{claim: webClaim()}, &fakeUploads{}, &fakeReads{}), "insured", "form-1", webFilingBody)
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestAWebFilingWithoutKeyOrJSONIsRefused(t *testing.T) {
	h := webRouter(&fakeWeb{}, &fakeUploads{}, &fakeReads{})
	for name, rec := range map[string]*httptest.ResponseRecorder{
		"no key":       fileWeb(h, "insured", "", webFilingBody),
		"key too long": fileWeb(h, "insured", strings.Repeat("k", 129), webFilingBody),
		"not json":     fileWeb(h, "insured", "form-1", "{"),
	} {
		assert.Equal(t, http.StatusBadRequest, rec.Code, name)
		assert.Equal(t, "invalid_request", decode(t, rec)["error"], name)
	}
}

func TestTheRefusalsOfAWebFilingAreTranslated(t *testing.T) {
	from, to := time.Date(2025, 11, 1, 5, 0, 0, 0, time.UTC), time.Date(2026, 11, 1, 5, 0, 0, 0, time.UTC)
	policy := &domain.Policy{ValidFrom: from, ValidTo: to}

	rec := fileWeb(webRouter(&fakeWeb{err: &domain.ValidationError{Fields: []domain.FieldError{
		{Field: "supports.traffic_report", Code: domain.CodeMissing}, {Field: "place", Code: domain.CodeRequired},
	}}}, &fakeUploads{}, &fakeReads{}), "insured", "form-1", webFilingBody)
	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	assert.JSONEq(t, `{"error":"validation_failed","message":"one or more fields are missing or invalid","fields":[
		{"field":"supports.traffic_report","code":"missing"},{"field":"place","code":"required"}]}`, rec.Body.String())

	rec = fileWeb(webRouter(&fakeWeb{err: &filing.PolicyNotEligibleError{Reason: filing.ReasonOccurredOutsideValidity, Policy: policy}},
		&fakeUploads{}, &fakeReads{}), "insured", "form-1", webFilingBody)
	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	assert.JSONEq(t, `{"error":"policy_not_eligible","message":"the incident happened outside the validity of the policy",
		"reason":"occurred_outside_validity","valid_from":"2025-11-01T05:00:00Z","valid_to":"2026-11-01T05:00:00Z"}`, rec.Body.String())

	rec = fileWeb(webRouter(&fakeWeb{err: &filing.PolicyNotEligibleError{Reason: filing.ReasonPolicyNotFound}},
		&fakeUploads{}, &fakeReads{}), "insured", "form-1", webFilingBody)
	assert.JSONEq(t, `{"error":"policy_not_eligible","message":"the policy is not one of the insured's",
		"reason":"policy_not_found","valid_from":null,"valid_to":null}`, rec.Body.String())

	rec = fileWeb(webRouter(&fakeWeb{err: ports.ErrFilingKeyReused}, &fakeUploads{}, &fakeReads{}), "insured", "form-1", webFilingBody)
	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	assert.Equal(t, "idempotency_key_reused", decode(t, rec)["error"])

	rec = fileWeb(webRouter(&fakeWeb{err: errors.New("pq: connection refused")}, &fakeUploads{}, &fakeReads{}), "insured", "form-1", webFilingBody)
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	assert.Equal(t, "filing_unavailable", decode(t, rec)["error"])
	assert.NotContains(t, rec.Body.String(), "pq:")
}

// --- Reads ------------------------------------------------------------------

func TestTheListOfClaims(t *testing.T) {
	mobile := domain.ClaimRecord{Claim: domain.Claim{ID: "m1", FilingNumber: "SIN-2026-00001", PolicyID: "AU-2025-118204",
		IncidentType: domain.IncidentTheft, Status: domain.StatusActive, OccurredAt: filedAt, FiledAt: filedAt},
		Channel: domain.ChannelMobile}
	reads := &fakeReads{page: filing.ClaimPage{Claims: []domain.ClaimRecord{webClaim(), mobile}, NextPageToken: "next"}}

	rec := do(webRouter(&fakeWeb{}, &fakeUploads{}, reads), http.MethodGet,
		"/v1/claims?status=active&policy_id=AU-2025-118204&from=2026-10-01&page_size=2&page_token=abc", "insured", nil, nil)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, filing.ClaimQuery{Status: "active", PolicyID: "AU-2025-118204", From: "2026-10-01", PageSize: 2, PageToken: "abc"},
		reads.gotList)
	m := decode(t, rec)
	assert.Equal(t, "next", m["next_page_token"])
	items := m["items"].([]any)
	require.Len(t, items, 2)
	second := items[1].(map[string]any)
	assert.Equal(t, "mobile", second["channel"])
	assert.Equal(t, map[string]any{"policy_id": "AU-2025-118204", "line": nil, "product": nil, "insured_object": nil}, second["policy"])
	assert.NotContains(t, items[0].(map[string]any), "supports", "the list does not carry the supports")
}

func TestTheLastPageHasANullToken(t *testing.T) {
	rec := do(webRouter(&fakeWeb{}, &fakeUploads{}, &fakeReads{}), http.MethodGet, "/v1/claims", "insured", nil, nil)

	assert.JSONEq(t, `{"items":[],"next_page_token":null}`, rec.Body.String())
}

func TestAnInvalidListQueryIs400(t *testing.T) {
	h := webRouter(&fakeWeb{}, &fakeUploads{}, &fakeReads{err: filing.ErrInvalidQuery})
	for _, query := range []string{"?page_size=0", "?page_size=x", "?policy_id=a%20b", "?page_token=" + strings.Repeat("t", 513), "?from=ayer"} {
		rec := do(h, http.MethodGet, "/v1/claims"+query, "insured", nil, nil)
		assert.Equal(t, http.StatusBadRequest, rec.Code, query)
		assert.Equal(t, "invalid_request", decode(t, rec)["error"], query)
	}

	rec := do(webRouter(&fakeWeb{}, &fakeUploads{}, &fakeReads{err: errors.New("down")}), http.MethodGet, "/v1/claims", "insured", nil, nil)
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	assert.Equal(t, "claims_unavailable", decode(t, rec)["error"])
}

func TestTheDetailAndTheReceipt(t *testing.T) {
	reads := &fakeReads{claim: webClaim(), pdf: []byte("%PDF-1.3 receipt")}
	h := webRouter(&fakeWeb{}, &fakeUploads{}, reads)

	rec := do(h, http.MethodGet, "/v1/claims/"+claimID, "insured", nil, nil)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, claimID, reads.gotID)
	assert.Equal(t, "SIN-2026-01712", decode(t, rec)["filing_number"])

	rec = do(h, http.MethodGet, "/v1/claims/"+claimID+"/filing-receipt", "insured", nil, nil)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "application/pdf", rec.Header().Get("Content-Type"))
	assert.Equal(t, `attachment; filename="radicado-SIN-2026-01712.pdf"`, rec.Header().Get("Content-Disposition"))
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
	assert.Equal(t, "%PDF-1.3 receipt", rec.Body.String())
}

func TestTheDetailOfAMobileClaimListsItsEvidenceWithSignedReads(t *testing.T) {
	mobile := webClaim()
	mobile.Channel, mobile.Policy, mobile.CatalogVersion, mobile.Supports = domain.ChannelMobile, nil, "", nil
	altitude := 2640.0
	mobile.Evidence = []domain.Evidence{
		{ObjectKey: "evidence/sub-carolina/a.jpg", Kind: domain.KindPhoto, CapturedAt: time.Date(2026, 9, 14, 23, 50, 0, 0, time.UTC),
			Latitude: 4.711, Longitude: -74.072, AltitudeM: &altitude, AccuracyM: 8, LocationTrusted: true, SHA256: strings.Repeat("a", 64), ByteSize: 310000},
		{ObjectKey: "evidence/sub-carolina/b.mp4", Kind: domain.KindVideo, CapturedAt: time.Date(2026, 9, 14, 23, 55, 0, 0, time.UTC),
			Latitude: 4.711, Longitude: -74.072, AccuracyM: 80, SHA256: strings.Repeat("b", 64), ByteSize: 5_000_000, DurationS: 12},
	}
	expires := filedAt.Add(10 * time.Minute)
	reads := &fakeReads{claim: mobile, links: []filing.EvidenceLink{
		{Evidence: mobile.Evidence[0], URL: "https://bucket.test/a.jpg?sig=1", ExpiresAt: expires},
		{Evidence: mobile.Evidence[1]},
	}}

	rec := do(webRouter(&fakeWeb{}, &fakeUploads{}, reads), http.MethodGet, "/v1/claims/"+claimID, "insured", nil, nil)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	m := decode(t, rec)
	assert.Nil(t, m["policy"].(map[string]any)["line"], "a mobile claim has no policy copy")
	assert.JSONEq(t, `[
		{"kind":"photo","content_type":"image/jpeg","captured_at":"2026-09-14T23:50:00Z","latitude":4.711,"longitude":-74.072,
		 "accuracy_m":8,"location_trusted":true,"byte_size":310000,"duration_s":0,
		 "download_url":"https://bucket.test/a.jpg?sig=1","download_expires_at":"2026-10-23T15:10:00Z"},
		{"kind":"video","content_type":"video/mp4","captured_at":"2026-09-14T23:55:00Z","latitude":4.711,"longitude":-74.072,
		 "accuracy_m":80,"location_trusted":false,"byte_size":5000000,"duration_s":12,
		 "download_url":null,"download_expires_at":null}]`, mustJSON(t, m["evidence"]))
	assert.NotContains(t, rec.Body.String(), "sha256", "the digest is not part of the detail")
	assert.NotContains(t, rec.Body.String(), "object_key", "the key of the store is internal")
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	require.NoError(t, err)
	return string(raw)
}

func TestTheReadsOfOneClaimTranslateTheirErrors(t *testing.T) {
	for err, want := range map[error][2]any{
		ports.ErrClaimNotFound:                            {http.StatusNotFound, "claim_not_found"},
		security.ErrForbidden:                             {http.StatusForbidden, "forbidden"},
		errors.New("db is gone"):                          {http.StatusServiceUnavailable, "claims_unavailable"},
		fmt.Errorf("wrapped: %w", ports.ErrClaimNotFound): {http.StatusNotFound, "claim_not_found"},
	} {
		h := webRouter(&fakeWeb{}, &fakeUploads{}, &fakeReads{err: err})
		for _, path := range []string{"/v1/claims/" + claimID, "/v1/claims/" + claimID + "/filing-receipt"} {
			rec := do(h, http.MethodGet, path, "insured", nil, nil)
			assert.Equal(t, want[0], rec.Code, "%s: %v", path, err)
			assert.Equal(t, want[1], decode(t, rec)["error"], "%s: %v", path, err)
		}
	}
}

func TestAnIDThatIsNotAClaimIDIsNotFoundWithoutReading(t *testing.T) {
	reads := &fakeReads{}
	h := webRouter(&fakeWeb{}, &fakeUploads{}, reads)
	for _, path := range []string{"/v1/claims/supports", "/v1/claims/not-an-id", "/v1/claims/not-an-id/filing-receipt"} {
		rec := do(h, http.MethodGet, path, "insured", nil, nil)
		assert.Equal(t, http.StatusNotFound, rec.Code, path)
	}
	assert.Empty(t, reads.gotID)
}

// The routes of HU-W2.1-01 live next to the mobile filing of HU-M2.2-01 on the
// same paths without taking its place.
func TestTheMobileFilingStillAnswersNextToTheReads(t *testing.T) {
	store := memory.NewCaseStore()
	h := httpadapter.NewRouter(
		httpadapter.WithFilings(filing.NewService(store, almacenDeEvidencia{}, fixedClock{}), callers),
		httpadapter.WithClaimReads(filing.NewQueryService(store, nil), callers))

	rec := do(h, http.MethodPost, "/v1/claims", "insured", strings.NewReader(validBody),
		map[string]string{"Idempotency-Key": "draft-1"})
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())

	rec = do(h, http.MethodGet, "/v1/claims", "insured", nil, nil)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Len(t, decode(t, rec)["items"], 1)
}

type fixedClock struct{}

func (fixedClock) Now() time.Time { return time.Now() }
