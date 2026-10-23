package httpadapter_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/security"
	httpadapter "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/adapters/http"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/ports"
)

// The filing route (HU-M2.2-01). It is the only write route of the service so
// far, and it always acts on behalf of whoever holds the token.

var filedAt = time.Date(2026, 10, 23, 15, 0, 0, 0, time.UTC)

type fakeFiling struct {
	claim   domain.Claim
	created bool
	err     error

	gotSubject string
	gotKey     string
	gotRequest domain.FilingRequest
}

func (f *fakeFiling) File(_ context.Context, subject, key string,
	r domain.FilingRequest) (domain.Claim, bool, error) {
	f.gotSubject, f.gotKey, f.gotRequest = subject, key, r
	return f.claim, f.created, f.err
}

type fixedVerifier struct{ roles []security.Role }

func (v fixedVerifier) Verify(_ context.Context, raw string) (security.Claims, error) {
	if raw != "good" {
		return security.Claims{}, errors.New("bad token")
	}
	return security.Claims{Subject: "sub-carolina", Roles: v.roles}, nil
}

func router(svc httpadapter.FilingService, roles ...security.Role) http.Handler {
	if len(roles) == 0 {
		roles = []security.Role{security.RoleInsured}
	}
	return httpadapter.NewRouter(httpadapter.WithFilings(svc, fixedVerifier{roles: roles}))
}

func call(h http.Handler, token, key, body string) (*httptest.ResponseRecorder, map[string]any) {
	req := httptest.NewRequest(http.MethodPost, "/v1/claims", strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var m map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &m)
	return rec, m
}

const validBody = `{"policy_id":"AU-2025-118204","incident_type":"collision",` +
	`"occurred_at":"2026-10-20T23:30:00Z","place":"Calle 80 con Carrera 15, Bogotá",` +
	`"description":"Choque por alcance en el semáforo.","details":{"third_party":"yes"},` +
	`"declaration_accepted":true}`

func filedClaim() domain.Claim {
	return domain.Claim{
		ID:           "cl-1",
		FilingNumber: "SIN-2026-00001",
		Subject:      "sub-carolina",
		FilingKey:    "draft-1",
		PolicyID:     "AU-2025-118204",
		IncidentType: domain.IncidentCollision,
		OccurredAt:   time.Date(2026, 10, 20, 23, 30, 0, 0, time.UTC),
		Place:        "Calle 80 con Carrera 15, Bogotá",
		Description:  "Choque por alcance en el semáforo.",
		Status:       domain.StatusActive,
		FiledAt:      filedAt,
	}
}

func TestFilingReturnsTheNumberOfTheCase(t *testing.T) {
	svc := &fakeFiling{claim: filedClaim(), created: true}

	rec, body := call(router(svc), "good", "draft-1", validBody)

	require.Equal(t, http.StatusCreated, rec.Code)
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
	assert.Equal(t, "SIN-2026-00001", body["filing_number"])
	assert.Equal(t, "cl-1", body["claim_id"])
	assert.Equal(t, "active", body["status"])
	assert.Equal(t, "collision", body["incident_type"])
	assert.Equal(t, "2026-10-20T23:30:00Z", body["occurred_at"])
}

// The body travels whole to the use case, and the subject comes from the
// token, not from the body.
func TestTheFilingArrivesCompleteAtTheUseCase(t *testing.T) {
	svc := &fakeFiling{claim: filedClaim(), created: true}

	call(router(svc), "good", "draft-1", validBody)

	assert.Equal(t, "sub-carolina", svc.gotSubject)
	assert.Equal(t, "draft-1", svc.gotKey)
	assert.Equal(t, "AU-2025-118204", svc.gotRequest.PolicyID)
	assert.Equal(t, "collision", svc.gotRequest.IncidentType)
	assert.Equal(t, "yes", svc.gotRequest.Details["third_party"])
	assert.True(t, svc.gotRequest.DeclarationAccepted)
}

// A retry is not a new filing: the answer is the case that already exists.
func TestARepeatedFilingAnswersWithoutCreating(t *testing.T) {
	svc := &fakeFiling{claim: filedClaim(), created: false}

	rec, body := call(router(svc), "good", "draft-1", validBody)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "SIN-2026-00001", body["filing_number"])
}

func TestWithoutTheKeyTheFilingIsRefused(t *testing.T) {
	svc := &fakeFiling{claim: filedClaim(), created: true}

	rec, body := call(router(svc), "good", "", validBody)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Equal(t, "invalid_request", body["error"])
	assert.Empty(t, svc.gotKey, "the use case is not called")
}

func TestAKeyThatIsTooLongIsRefused(t *testing.T) {
	rec, body := call(router(&fakeFiling{}), "good", strings.Repeat("k", 129), validBody)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Equal(t, "invalid_request", body["error"])
}

func TestABodyThatIsNotJSONIsRefused(t *testing.T) {
	rec, body := call(router(&fakeFiling{}), "good", "draft-1", "{")

	require.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Equal(t, "invalid_request", body["error"])
}

func TestWithoutASessionTheRouteDoesNotAnswer(t *testing.T) {
	rec, _ := call(router(&fakeFiling{}), "", "draft-1", validBody)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestAnInvalidTokenIsRefused(t *testing.T) {
	rec, _ := call(router(&fakeFiling{}), "expired", "draft-1", validBody)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

// Only the insured files their own claim; the advisor and the analyst act on
// their own routes.
func TestOnlyTheInsuredFiles(t *testing.T) {
	for _, role := range []security.Role{security.RoleAdvisor, security.RoleOperations, security.RolePartner} {
		t.Run(string(role), func(t *testing.T) {
			rec, _ := call(router(&fakeFiling{}, role), "good", "draft-1", validBody)

			assert.Equal(t, http.StatusForbidden, rec.Code)
		})
	}
}

func TestTheInvalidFieldsAreReturnedTogether(t *testing.T) {
	svc := &fakeFiling{err: &domain.ValidationError{Fields: []domain.FieldError{
		{Field: "occurred_at", Code: domain.CodeOutOfRange},
		{Field: "declaration_accepted", Code: domain.CodeRequired},
	}}}

	rec, body := call(router(svc), "good", "draft-1", validBody)

	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	assert.Equal(t, "validation_failed", body["error"])
	fields, _ := body["fields"].([]any)
	require.Len(t, fields, 2)
}

func TestAKeyUsedForAnotherFilingIsRefused(t *testing.T) {
	svc := &fakeFiling{err: ports.ErrFilingKeyReused}

	rec, body := call(router(svc), "good", "draft-1", validBody)

	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	assert.Equal(t, "idempotency_key_reused", body["error"])
}

// The detail of the failure describes the inside of the platform.
func TestAnInternalFailureIsNotDetailed(t *testing.T) {
	svc := &fakeFiling{err: errors.New("dial tcp 10.0.3.14:5432: connect: connection refused")}

	rec, body := call(router(svc), "good", "draft-1", validBody)

	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	assert.Equal(t, "filing_unavailable", body["error"])
	assert.NotContains(t, rec.Body.String(), "10.0.3.14")
}

// The load balancer checks it without a token.
func TestHealthAnswersWithoutASession(t *testing.T) {
	rec := httptest.NewRecorder()
	router(&fakeFiling{}).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))

	assert.Equal(t, http.StatusOK, rec.Code)
}
