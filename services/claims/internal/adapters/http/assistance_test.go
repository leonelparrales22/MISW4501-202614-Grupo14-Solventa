package httpadapter_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/security"
	httpadapter "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/adapters/http"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/assistance"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/ports"
)

// The routes of the request for on-site assistance (HU-M2.3-01).

const (
	assistancePath = "/v1/claims/assistance-requests"
	requestID      = "3b8e2f6a-1c4d-4e7f-9a2b-6c5d8e0f1a2b"
	withFix        = `{"policy_id": "AU-2025-118204",
		"location": {"status": "sent", "latitude": 4.7712, "longitude": -74.0452, "accuracy_m": 8,
			"captured_at": "2026-11-14T21:31:02Z"},
		"place": "Autopista Norte km 12, Bogotá"}`
	withoutFix = `{"policy_id": "AU-2025-118204", "location": {"status": "unavailable", "unavailable_reason": "no_fix"}}`
)

// requesters are the callers of the assistance: the insured has a name in
// the token.
var requesters = tokens{
	"insured": {Subject: "sub-carolina", Name: "Carolina Ruiz", Roles: []security.Role{security.RoleInsured}},
	"advisor": callers["advisor"], "operations": callers["operations"], "partner": callers["partner"],
}

type fakeAssistance struct {
	request domain.AssistanceRequest
	created bool
	err     error
	calls   int

	gotRequester assistance.Requester
	gotKey       string
	gotInput     domain.AssistanceRequestInput
	gotID        string
}

func (f *fakeAssistance) Request(_ context.Context, r assistance.Requester, key string,
	in domain.AssistanceRequestInput) (domain.AssistanceRequest, bool, error) {
	f.calls++
	f.gotRequester, f.gotKey, f.gotInput = r, key, in
	return f.request, f.created, f.err
}

func (f *fakeAssistance) Get(_ context.Context, subject, id string) (domain.AssistanceRequest, error) {
	f.calls++
	f.gotRequester, f.gotID = assistance.Requester{Subject: subject}, id
	return f.request, f.err
}

func recordedWithFix() domain.AssistanceRequest {
	accuracy, captured, place := 8.0, time.Date(2026, 11, 14, 21, 31, 2, 0, time.UTC), "Autopista Norte km 12, Bogotá"
	return domain.AssistanceRequest{
		ID: requestID, Number: "AST-2026-0912", Subject: "sub-carolina", Status: domain.AssistanceNew,
		Policy: domain.AssistancePolicy{ID: "AU-2025-118204", Line: domain.LineAuto, Product: "Autos",
			InsuredObject: "KJT-482"},
		Location: domain.AssistanceLocation{Status: domain.LocationSent,
			Coordinates: &domain.Coordinates{Latitude: 4.7712, Longitude: -74.0452}, AccuracyM: &accuracy,
			CapturedAt: &captured},
		Place: &place, RequestedAt: time.Date(2026, 11, 14, 21, 31, 8, 0, time.UTC),
	}
}

func recordedWithoutFix() domain.AssistanceRequest {
	r := recordedWithFix()
	r.Location = domain.AssistanceLocation{Status: domain.LocationUnavailable, UnavailableReason: domain.UnavailableNoFix}
	r.Place = nil
	return r
}

func assistanceRouter(f *fakeAssistance) http.Handler {
	return httpadapter.NewRouter(httpadapter.WithAssistance(f, requesters))
}

func postAssistance(h http.Handler, caller, key, body string) (int, string, http.Header) {
	headers := map[string]string{"Content-Type": "application/json"}
	if key != "" {
		headers["Idempotency-Key"] = key
	}
	rec := do(h, http.MethodPost, assistancePath, caller, strings.NewReader(body), headers)
	return rec.Code, rec.Body.String(), rec.Header()
}

const recordedWithFixJSON = `{
	"request_id": "` + requestID + `", "request_number": "AST-2026-0912", "status": "new",
	"policy": {"policy_id": "AU-2025-118204", "line": "auto", "product": "Autos", "insured_object": "KJT-482"},
	"location": {"status": "sent", "latitude": 4.7712, "longitude": -74.0452, "accuracy_m": 8,
		"captured_at": "2026-11-14T21:31:02Z", "unavailable_reason": null},
	"place": "Autopista Norte km 12, Bogotá", "requested_at": "2026-11-14T21:31:08Z"}`

func TestARequestWithALocationAnswers201WithEverythingRecorded(t *testing.T) {
	f := &fakeAssistance{request: recordedWithFix(), created: true}
	status, body, headers := postAssistance(assistanceRouter(f), "insured", "assist-7c1f", withFix)

	require.Equal(t, http.StatusCreated, status, body)
	assert.Equal(t, "no-store", headers.Get("Cache-Control"))
	assert.Equal(t, "application/json", headers.Get("Content-Type"))
	assert.JSONEq(t, recordedWithFixJSON, body)

	assert.Equal(t, assistance.Requester{Subject: "sub-carolina", Name: "Carolina Ruiz"}, f.gotRequester,
		"the owner and the name come from the token")
	assert.Equal(t, "assist-7c1f", f.gotKey)
	assert.Equal(t, "AU-2025-118204", f.gotInput.PolicyID)
	require.NotNil(t, f.gotInput.Location)
	assert.Equal(t, "sent", f.gotInput.Location.Status)
	assert.Equal(t, 4.7712, *f.gotInput.Location.Latitude)
	assert.Equal(t, -74.0452, *f.gotInput.Location.Longitude)
	assert.Equal(t, 8.0, *f.gotInput.Location.AccuracyM)
	assert.Equal(t, "2026-11-14T21:31:02Z", *f.gotInput.Location.CapturedAt)
	assert.Nil(t, f.gotInput.Location.UnavailableReason)
	assert.Equal(t, "Autopista Norte km 12, Bogotá", *f.gotInput.Place)
}

func TestARequestWithoutALocationAnswersWithNulls(t *testing.T) {
	f := &fakeAssistance{request: recordedWithoutFix(), created: true}
	status, body, _ := postAssistance(assistanceRouter(f), "insured", "assist-7c1f", withoutFix)

	require.Equal(t, http.StatusCreated, status, body)
	assert.JSONEq(t, `{
		"request_id": "`+requestID+`", "request_number": "AST-2026-0912", "status": "new",
		"policy": {"policy_id": "AU-2025-118204", "line": "auto", "product": "Autos", "insured_object": "KJT-482"},
		"location": {"status": "unavailable", "latitude": null, "longitude": null, "accuracy_m": null,
			"captured_at": null, "unavailable_reason": "no_fix"},
		"place": null, "requested_at": "2026-11-14T21:31:08Z"}`, body)
	assert.Nil(t, f.gotInput.Location.Latitude)
	assert.Nil(t, f.gotInput.Place)
	assert.Equal(t, "no_fix", *f.gotInput.Location.UnavailableReason)
}

func TestTheOriginIsAPlaceNotAMissingCoordinate(t *testing.T) {
	f := &fakeAssistance{request: recordedWithFix(), created: true}
	status, _, _ := postAssistance(assistanceRouter(f), "insured", "k", `{"policy_id": "AU-2025-118204",
		"location": {"status": "sent", "latitude": 0, "longitude": 0, "accuracy_m": 8, "captured_at": "2026-11-14T21:31:02Z"}}`)
	require.Equal(t, http.StatusCreated, status)
	require.NotNil(t, f.gotInput.Location.Latitude)
	assert.Zero(t, *f.gotInput.Location.Latitude)
	assert.Zero(t, *f.gotInput.Location.Longitude)
}

func TestARetryAnswers200WithTheSameRequest(t *testing.T) {
	f := &fakeAssistance{request: recordedWithFix(), created: false}
	status, body, headers := postAssistance(assistanceRouter(f), "insured", "assist-7c1f", withFix)
	require.Equal(t, http.StatusOK, status)
	assert.Equal(t, "no-store", headers.Get("Cache-Control"))
	assert.JSONEq(t, recordedWithFixJSON, body)
}

func TestTheIdempotencyKeyIsRequiredAndBounded(t *testing.T) {
	for name, key := range map[string]string{
		"missing":        "",
		"129 characters": strings.Repeat("a", 129),
		"with spaces":    "assist 7c1f",
		"with a slash":   "assist/7c1f",
	} {
		f := &fakeAssistance{}
		status, body, _ := postAssistance(assistanceRouter(f), "insured", key, withFix)
		assert.Equal(t, http.StatusBadRequest, status, name)
		assert.Contains(t, body, `"invalid_request"`, name)
		assert.Zero(t, f.calls, name)
	}
	f := &fakeAssistance{request: recordedWithFix(), created: true}
	status, _, _ := postAssistance(assistanceRouter(f), "insured", strings.Repeat("a", 128), withFix)
	assert.Equal(t, http.StatusCreated, status, "128 characters")
}

func TestABrokenBodyIsABadRequest(t *testing.T) {
	for name, body := range map[string]string{
		"not JSON":                  `{"policy_id": `,
		"a list":                    `[]`,
		"a text":                    `"AU-2025-118204"`,
		"an unknown field":          `{"policy_id": "AU-2025-118204", "location": {"status": "no_fix"}, "priority": "high"}`,
		"the owner in the body":     `{"policy_id": "AU-2025-118204", "sub": "sub-andres", "location": {"status": "unavailable", "unavailable_reason": "no_fix"}}`,
		"the subject in the body":   `{"policy_id": "AU-2025-118204", "cognito_sub": "x", "location": {"status": "unavailable", "unavailable_reason": "no_fix"}}`,
		"an unknown location field": `{"policy_id": "AU-2025-118204", "location": {"status": "sent", "altitude_m": 2600}}`,
		"coordinates as text":       `{"policy_id": "AU-2025-118204", "location": {"status": "sent", "latitude": "4.77"}}`,
		"NaN":                       `{"policy_id": "AU-2025-118204", "location": {"status": "sent", "latitude": NaN}}`,
		"Infinity":                  `{"policy_id": "AU-2025-118204", "location": {"status": "sent", "latitude": Infinity}}`,
		"beyond a double":           `{"policy_id": "AU-2025-118204", "location": {"status": "sent", "latitude": 1e400}}`,
		"a policy that is a number": `{"policy_id": 118204, "location": {"status": "unavailable", "unavailable_reason": "no_fix"}}`,
		"two objects":               withoutFix + withoutFix,
		"over 16 KB": `{"policy_id": "AU-2025-118204", "location": {"status": "unavailable", "unavailable_reason": "no_fix"},
			"place": "` + strings.Repeat("a", 1<<14) + `"}`,
	} {
		f := &fakeAssistance{}
		status, got, _ := postAssistance(assistanceRouter(f), "insured", "k", body)
		assert.Equal(t, http.StatusBadRequest, status, name)
		assert.Contains(t, got, `"invalid_request"`, name)
		assert.Zero(t, f.calls, name)
	}
}

func TestAnInvalidBodyListsEveryField(t *testing.T) {
	f := &fakeAssistance{err: &domain.ValidationError{Fields: []domain.FieldError{
		{Field: "location.latitude", Code: domain.CodeNotApplicable},
		{Field: "location.unavailable_reason", Code: domain.CodeRequired},
	}}}
	status, body, _ := postAssistance(assistanceRouter(f), "insured", "k", `{"policy_id": "AU-2025-118204",
		"location": {"status": "unavailable", "latitude": 4.7}}`)
	require.Equal(t, http.StatusUnprocessableEntity, status)
	assert.JSONEq(t, `{"error": "validation_failed", "message": "one or more fields are missing or invalid",
		"fields": [{"field": "location.latitude", "code": "not_applicable"},
			{"field": "location.unavailable_reason", "code": "required"}]}`, body)
}

func TestAPolicyThatDoesNotTakeTheRequestIs422(t *testing.T) {
	notFound := &fakeAssistance{err: &assistance.PolicyNotEligibleError{Reason: assistance.ReasonPolicyNotFound}}
	status, body, _ := postAssistance(assistanceRouter(notFound), "insured", "k", withFix)
	require.Equal(t, http.StatusUnprocessableEntity, status)
	assert.JSONEq(t, `{"error": "policy_not_eligible", "message": "the policy is not one of the insured's",
		"reason": "policy_not_found", "valid_from": null, "valid_to": null}`, body)

	expired := domain.Policy{ID: "AU-2024-000001", ValidFrom: time.Date(2024, 10, 15, 0, 0, 0, 0, time.UTC),
		ValidTo: time.Date(2025, 10, 15, 0, 0, 0, 0, time.FixedZone("COT", -5*3600))}
	notActive := &fakeAssistance{err: &assistance.PolicyNotEligibleError{Reason: assistance.ReasonPolicyNotActive,
		Policy: &expired}}
	status, body, _ = postAssistance(assistanceRouter(notActive), "insured", "k", withFix)
	require.Equal(t, http.StatusUnprocessableEntity, status)
	assert.JSONEq(t, `{"error": "policy_not_eligible", "message": "the policy is not active",
		"reason": "policy_not_active", "valid_from": "2024-10-15T00:00:00Z", "valid_to": "2025-10-15T05:00:00Z"}`, body)
}

func TestAReusedKeyIs422(t *testing.T) {
	f := &fakeAssistance{err: ports.ErrAssistanceKeyReused}
	status, body, _ := postAssistance(assistanceRouter(f), "insured", "k", withFix)
	require.Equal(t, http.StatusUnprocessableEntity, status)
	assert.JSONEq(t, `{"error": "idempotency_key_reused",
		"message": "that key was used for another request for assistance"}`, body)
}

func TestWithThePolicyCoreDownTheAnswerIs503AndTheLogDoesNotNameTheInsured(t *testing.T) {
	logs := captureLogs(t)
	f := &fakeAssistance{err: errors.Join(ports.ErrPolicyDirectoryUnavailable,
		errors.New(`Get "http://policy-core:9060/holders/sub-carolina/policies": context deadline exceeded`))}
	status, body, _ := postAssistance(assistanceRouter(f), "insured", "k", withFix)
	require.Equal(t, http.StatusServiceUnavailable, status)
	assert.Contains(t, body, `"policy_directory_unavailable"`)
	assert.Contains(t, logs.String(), "the policy core did not answer")
	assert.NotContains(t, logs.String(), "sub-carolina")
}

func TestAFailureOfTheStoreIs503(t *testing.T) {
	f := &fakeAssistance{err: errors.New("connection refused")}
	status, body, _ := postAssistance(assistanceRouter(f), "insured", "k", withFix)
	require.Equal(t, http.StatusServiceUnavailable, status)
	assert.Contains(t, body, `"assistance_unavailable"`)

	rec := do(assistanceRouter(f), http.MethodGet, assistancePath+"/"+requestID, "insured", nil, nil)
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	assert.Contains(t, rec.Body.String(), `"assistance_unavailable"`)
}

func TestTheOwnerReadsTheRequest(t *testing.T) {
	f := &fakeAssistance{request: recordedWithFix()}
	rec := do(assistanceRouter(f), http.MethodGet, assistancePath+"/"+requestID, "insured", nil, nil)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
	assert.JSONEq(t, recordedWithFixJSON, rec.Body.String())
	assert.Equal(t, "sub-carolina", f.gotRequester.Subject)
	assert.Equal(t, requestID, f.gotID)
}

func TestARequestOfSomeoneElseIsNotFound(t *testing.T) {
	f := &fakeAssistance{err: ports.ErrAssistanceRequestNotFound}
	rec := do(assistanceRouter(f), http.MethodGet, assistancePath+"/"+requestID, "insured", nil, nil)
	require.Equal(t, http.StatusNotFound, rec.Code)
	assert.JSONEq(t, `{"error": "assistance_request_not_found", "message": "there is no such request for assistance"}`,
		rec.Body.String())
}

func TestAnIDThatIsNotOneIsNotFoundWithoutReachingTheService(t *testing.T) {
	for _, id := range []string{
		"..%2F..%2Fetc", "%2F", "%00", strings.ToUpper(requestID), requestID + "a", requestID[:35],
		"3b8e2f6a-1c4d-1e7f-9a2b-6c5d8e0f1a2b", strings.Repeat("a", 10<<10), "AST-2026-0912",
	} {
		f := &fakeAssistance{request: recordedWithFix()}
		rec := do(assistanceRouter(f), http.MethodGet, assistancePath+"/"+id, "insured", nil, nil)
		assert.Equal(t, http.StatusNotFound, rec.Code, id)
		assert.Contains(t, rec.Body.String(), `"assistance_request_not_found"`, id)
		assert.Zero(t, f.calls, id)
	}
	f := &fakeAssistance{}
	rec := do(assistanceRouter(f), http.MethodGet, assistancePath+"/../../etc/passwd", "insured", nil, nil)
	assert.NotEqual(t, http.StatusOK, rec.Code)
	assert.Zero(t, f.calls)
}

func TestTheLogsOfARequestCarryNoLocationNorPerson(t *testing.T) {
	logs := captureLogs(t)
	f := &fakeAssistance{request: recordedWithFix(), created: true}
	f.request.RequesterName = "Carolina Ruiz"
	status, _, _ := postAssistance(assistanceRouter(f), "insured", "k", withFix)
	require.Equal(t, http.StatusCreated, status)

	out := logs.String()
	assert.Contains(t, out, requestID)
	assert.Contains(t, out, "AST-2026-0912")
	assert.Contains(t, out, "location_status=sent")
	for _, secret := range []string{"4.7712", "-74.0452", "Autopista", "Carolina", "sub-carolina"} {
		assert.NotContains(t, out, secret)
	}
}

// captureLogs sends the default logger to a buffer for the test.
func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return &buf
}
