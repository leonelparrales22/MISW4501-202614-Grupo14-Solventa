package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/security"
	httpadapter "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/adapters/http"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/adapters/memory"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/adapters/postgres"
)

// The composition of the request for assistance (HU-M2.3-01): mounted only
// with the policy core (assumption S12), over the store of the claims.

// insuredTokens is the verifier of the tests: one insured.
type insuredTokens struct{}

func (insuredTokens) Verify(_ context.Context, raw string) (security.Claims, error) {
	if raw != "insured" {
		return security.Claims{}, errors.New("bad token")
	}
	return security.Claims{Subject: "sub-carolina", Roles: []security.Role{security.RoleInsured}}, nil
}

func TestTheRequestsLiveWhereTheClaimsLive(t *testing.T) {
	store, err := assistanceStoreOf(postgres.NewClaimRecordStore(nil))
	require.NoError(t, err)
	assert.IsType(t, &postgres.AssistanceStore{}, store)

	store, err = assistanceStoreOf(memory.NewCaseStore())
	require.NoError(t, err)
	assert.IsType(t, &memory.AssistanceStore{}, store)

	_, err = assistanceStoreOf(fakeRecords{})
	assert.Error(t, err)
}

func TestWithoutThePolicyCoreTheAssistanceIsNotMounted(t *testing.T) {
	opts, err := assistanceRoutes(memory.NewCaseStore(), env(nil), insuredTokens{})
	require.NoError(t, err)
	assert.Empty(t, opts)

	// Not left open without the check of the policy: the route does not exist.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/claims/assistance-requests", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer insured")
	httpadapter.NewRouter(opts...).ServeHTTP(rec, req)
	assert.Contains(t, []int{http.StatusNotFound, http.StatusMethodNotAllowed}, rec.Code)
}

func TestWithThePolicyCoreTheAssistanceIsMounted(t *testing.T) {
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"policies": []}`))
	}))
	t.Cleanup(core.Close)

	opts, err := assistanceRoutes(memory.NewCaseStore(),
		env(map[string]string{"POLICY_CORE_URL": core.URL, "POLICY_CORE_TIMEOUT_MS": "500"}), insuredTokens{})
	require.NoError(t, err)
	require.Len(t, opts, 1)

	h := httpadapter.NewRouter(opts...)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/claims/assistance-requests", strings.NewReader(
		`{"policy_id": "AU-2025-118204", "location": {"status": "unavailable", "unavailable_reason": "no_fix"}}`))
	req.Header.Set("Authorization", "Bearer insured")
	req.Header.Set("Idempotency-Key", "assist-7c1f")
	h.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code, "the core says the insured has no such policy")
	assert.Contains(t, rec.Body.String(), `"policy_not_found"`)

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/claims/assistance-requests", nil))
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestAnInvalidTimeoutOfThePolicyCoreIsAnError(t *testing.T) {
	_, err := assistanceRoutes(memory.NewCaseStore(),
		env(map[string]string{"POLICY_CORE_URL": "http://policy-core:9060", "POLICY_CORE_TIMEOUT_MS": "abc"}),
		insuredTokens{})
	assert.ErrorContains(t, err, "POLICY_CORE_TIMEOUT_MS")
}

func TestTheAssistanceNeedsAStoreForTheClaims(t *testing.T) {
	_, err := assistanceRoutes(fakeRecords{}, env(map[string]string{"POLICY_CORE_URL": "http://policy-core:9060"}),
		insuredTokens{})
	assert.Error(t, err)
}
