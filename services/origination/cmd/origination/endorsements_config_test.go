package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	httpadapter "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/http"
)

func TestChangeRequestsAreMountedOnlyWithTheCharge(t *testing.T) {
	without, err := endorsementOptions(false, endorsementConfig{}, nil, nil, noVerifier{})
	require.NoError(t, err)
	assert.Empty(t, without, "without the premium charge there are no policies to change")

	with, err := endorsementOptions(true, endorsementConfig{partnerNames: map[string]string{"aliado-banco-1": "Banco Aliado"}},
		lazyDB(t), nil, noVerifier{})
	require.NoError(t, err)
	require.Len(t, with, 1)

	router := httpadapter.NewRouter(nil, append([]httpadapter.Option{httpadapter.WithObservability(serviceName)}, with...)...)
	for _, r := range []struct{ method, path string }{
		{http.MethodGet, "/v1/policies/pol-1/current-terms"},
		{http.MethodPost, "/v1/policies/pol-1/change-requests"},
		{http.MethodGet, "/v1/policies/change-requests"},
		{http.MethodGet, "/v1/policies/change-requests/SOL-MOD-2026-0001"},
	} {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(r.method, r.path, nil))
		assert.Equal(t, http.StatusUnauthorized, rec.Code, "%s is mounted behind the authentication", r.path)
	}
}

func TestChangeRequestsNeedTheDatabase(t *testing.T) {
	_, err := endorsementOptions(true, endorsementConfig{}, nil, nil, noVerifier{})
	assert.ErrorContains(t, err, "DATABASE_URL")
}

func TestPartnerDisplayNamesAreOptionalButMustBeValid(t *testing.T) {
	var cfg endorsementConfig
	require.NoError(t, loadEndorsementConfig(envOf(nil), &cfg))
	assert.Empty(t, cfg.partnerNames, "without the catalog the code of the partner is shown")

	require.NoError(t, loadEndorsementConfig(envOf(map[string]string{
		"PARTNER_DISPLAY_NAMES": `{"aliado-banco-1":"Banco Aliado","aliado_2":"Cooperativa Dos"}`,
	}), &cfg))
	assert.Equal(t, map[string]string{"aliado-banco-1": "Banco Aliado", "aliado_2": "Cooperativa Dos"}, cfg.partnerNames)

	for _, bad := range []string{`not json`, `{"aliado banco":"Banco"}`, `{"aliado-1":""}`, `["Banco Aliado"]`} {
		assert.Error(t, loadEndorsementConfig(envOf(map[string]string{"PARTNER_DISPLAY_NAMES": bad}), &cfg), bad)
	}

	_, err := loadConfig(env(map[string]string{"REDIS_ADDR": "redis:6379", "OPEN_FINANCE_URL": "http://of:9000",
		"PARTNER_DISPLAY_NAMES": `{"aliado-1":""}`}))
	assert.ErrorContains(t, err, "PARTNER_DISPLAY_NAMES", "the service does not start with an invalid catalog")
}

// HU-W1.4-02: the review of the operations analyst is mounted where the change
// requests are, with its validations configured or unverifiable.
func TestTheEndorsementReviewIsMountedOnlyWithTheCharge(t *testing.T) {
	without, err := endorsementReviewOptions(context.Background(), false, endorsementConfig{}, nil, nil, nil, noVerifier{})
	require.NoError(t, err)
	assert.Empty(t, without)
	_, err = endorsementReviewOptions(context.Background(), true, endorsementConfig{}, nil, nil, nil, noVerifier{})
	assert.ErrorContains(t, err, "DATABASE_URL")

	for name, cfg := range map[string]endorsementConfig{
		"without claims nor KYC": {},
		"with claims and KYC": {claimsURL: "http://claims:8080", claimsTimeout: 300 * time.Millisecond,
			kycURL: "http://kyc:9020", documentCheckTimeout: 2 * time.Second},
	} {
		ctx, cancel := context.WithCancel(context.Background())
		with, err := endorsementReviewOptions(ctx, true, cfg, lazyDB(t), nil, mailSink{}, noVerifier{})
		require.NoError(t, err, name)
		require.Len(t, with, 1, name)
		router := httpadapter.NewRouter(nil, append([]httpadapter.Option{httpadapter.WithObservability(serviceName)}, with...)...)
		for _, r := range []struct{ method, path string }{
			{http.MethodGet, "/v1/policies/endorsement-requests"},
			{http.MethodGet, "/v1/policies/endorsement-requests/SOL-MOD-2026-0001"},
			{http.MethodPost, "/v1/policies/endorsement-requests/SOL-MOD-2026-0001/review"},
			{http.MethodPost, "/v1/policies/endorsement-requests/SOL-MOD-2026-0001/application"},
			{http.MethodPost, "/v1/policies/endorsement-requests/SOL-MOD-2026-0001/rejection"},
			{http.MethodPost, "/v1/policies/endorsement-requests/SOL-MOD-2026-0001/return"},
			{http.MethodGet, "/v1/policies/pol-1/endorsements"},
		} {
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, httptest.NewRequest(r.method, r.path, nil))
			assert.Equal(t, http.StatusUnauthorized, rec.Code, "%s %s is mounted behind the authentication", name, r.path)
		}
		cancel()
	}
}

func TestTheValidationsOfTheEndorsementsReadTheirVariables(t *testing.T) {
	var cfg endorsementConfig
	require.NoError(t, loadEndorsementConfig(envOf(nil), &cfg))
	assert.Empty(t, cfg.claimsURL)
	assert.Equal(t, 300*time.Millisecond, cfg.claimsTimeout)
	assert.Equal(t, 2*time.Second, cfg.documentCheckTimeout)

	require.NoError(t, loadEndorsementConfig(envOf(map[string]string{"CLAIMS_URL": "http://claims.staging.local:8080",
		"CLAIMS_TIMEOUT_MS": "500", "KYC_URL": "http://kyc:9020", "DOCUMENT_CHECK_TIMEOUT_MS": "1500"}), &cfg))
	assert.Equal(t, endorsementConfig{partnerNames: map[string]string{}, claimsURL: "http://claims.staging.local:8080",
		claimsTimeout: 500 * time.Millisecond, kycURL: "http://kyc:9020", documentCheckTimeout: 1500 * time.Millisecond}, cfg)

	for _, bad := range []map[string]string{{"CLAIMS_TIMEOUT_MS": "0"}, {"DOCUMENT_CHECK_TIMEOUT_MS": "mucho"}} {
		assert.Error(t, loadEndorsementConfig(envOf(bad), &cfg), "%v", bad)
	}
}
