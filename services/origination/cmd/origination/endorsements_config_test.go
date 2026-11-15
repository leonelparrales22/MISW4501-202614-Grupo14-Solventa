package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

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
