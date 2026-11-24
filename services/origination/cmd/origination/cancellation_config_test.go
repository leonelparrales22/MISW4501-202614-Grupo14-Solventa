package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	httpadapter "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/http"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
)

// HU-W1.6-01: the cancellation is mounted where the policies that can be
// cancelled are (with the premium charge), with its refunds through the same
// gateway and configuration.
func TestTheCancellationIsMountedOnlyWithTheCharge(t *testing.T) {
	rules := domain.DefaultInstallmentRules
	without, err := cancellationOptions(context.Background(), billingConfig{}, rules, nil, noVerifier{})
	require.NoError(t, err)
	assert.Empty(t, without, "without the premium charge there are no policies to cancel")

	var cfg billingConfig
	require.NoError(t, loadBillingConfig(envOf(map[string]string{
		"PAYMENT_GATEWAY_URL": "http://payment-gateway:9040", "DATABASE_URL": "postgres://db", "EMAIL_URL": "http://email:9030",
		"AUTH_ISSUER": "https://issuer.example",
	}), &cfg))
	_, err = cancellationOptions(context.Background(), cfg, rules, nil, noVerifier{})
	assert.ErrorContains(t, err, "DATABASE_URL")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	with, err := cancellationOptions(ctx, cfg, rules, lazyDB(t), noVerifier{})
	require.NoError(t, err)
	require.Len(t, with, 1)
	router := httpadapter.NewRouter(nil, append([]httpadapter.Option{httpadapter.WithObservability(serviceName)}, with...)...)
	for _, r := range []struct{ method, path string }{
		{http.MethodGet, "/v1/policies/VH-2026-000101/cancellation/quote"},
		{http.MethodPost, "/v1/policies/VH-2026-000101/cancellation"},
	} {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(r.method, r.path, nil))
		assert.Equal(t, http.StatusUnauthorized, rec.Code, "%s is mounted behind the authentication", r.path)
	}
}
