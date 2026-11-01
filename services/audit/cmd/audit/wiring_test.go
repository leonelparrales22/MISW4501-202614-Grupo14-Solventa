package main

import (
	"bytes"
	"context"
	"log"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/security"
	httpadapter "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/audit/internal/adapters/http"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/audit/internal/consents"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/audit/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/audit/internal/partnercredentials"
)

// HU-M1.3-01: the catalog of names, the human routes and the consumer.

func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(prev) })
	return &buf
}

func TestLoadConfigReadsThePartnerCatalog(t *testing.T) {
	cfg, err := loadConfig(env(map[string]string{"PARTNER_DISPLAY_NAMES": `{"aliado-banco-1":"Banco Aliado","otro_2":"Otro"}`}))

	require.NoError(t, err)
	assert.Equal(t, domain.EntityNames{"aliado-banco-1": "Banco Aliado", "otro_2": "Otro"}, cfg.partnerNames)
}

func TestLoadConfigWithoutTheCatalogShowsTheCodes(t *testing.T) {
	cfg, err := loadConfig(env(nil))

	require.NoError(t, err)
	assert.Empty(t, cfg.partnerNames)
	assert.Equal(t, domain.Entity{Code: "aliado-banco-1", Name: "aliado-banco-1"}, cfg.partnerNames.Entity("aliado-banco-1"))
}

func TestLoadConfigRefusesAnInvalidCatalog(t *testing.T) {
	for name, raw := range map[string]string{
		"not json":     `aliado-banco-1=Banco`,
		"not object":   `["Banco Aliado"]`,
		"empty name":   `{"aliado-banco-1":""}`,
		"invalid code": `{"aliado banco":"Banco"}`,
		"not a string": `{"aliado-banco-1":7}`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := loadConfig(env(map[string]string{"PARTNER_DISPLAY_NAMES": raw}))
			assert.ErrorContains(t, err, "PARTNER_DISPLAY_NAMES")
		})
	}
}

func TestLoadConfigReadsTheQueue(t *testing.T) {
	cfg, err := loadConfig(env(map[string]string{"EVENTS_QUEUE_URL": "http://localhost:4566/000000000000/solventa-staging-audit"}))

	require.NoError(t, err)
	assert.Equal(t, "http://localhost:4566/000000000000/solventa-staging-audit", cfg.eventsQueueURL)
}

type staticVerifier map[string]security.Claims

func (v staticVerifier) Verify(_ context.Context, raw string) (security.Claims, error) {
	if c, ok := v[raw]; ok {
		return c, nil
	}
	return security.Claims{}, security.ErrInvalidToken
}

type clockAt struct{ t time.Time }

func (c clockAt) Now() time.Time { return c.t }

func status(t *testing.T, h http.Handler, method, target, token string) int {
	t.Helper()
	req := httptest.NewRequest(method, target, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code
}

func TestTheHumanRoutesAreMountedOnlyWithAVerifier(t *testing.T) {
	buf := captureLog(t)
	clk := clockAt{t: time.Date(2026, 10, 24, 20, 0, 0, 0, time.UTC)}
	svc := consents.NewService(nil, clk)
	holders := consents.NewHolderService(nil, nil, nil, clk, nil)
	revocations := consents.NewRevocationService(consents.RevocationDeps{Clock: clk})

	without := httpadapter.NewRouter(consentOptions(svc, holders, revocations, nil)...)
	assert.Equal(t, http.StatusNotFound, status(t, without, http.MethodGet, "/v1/consents", ""), "not mounted")
	assert.Equal(t, http.StatusNotFound, status(t, without, http.MethodGet, "/v1/audit/consents/cns-1", ""))
	assert.Equal(t, http.StatusOK, status(t, without, http.MethodGet, "/v1/audit/consent-terms/current", ""), "the partner routes always")
	assert.Contains(t, buf.String(), "AUTH_ISSUER is not set: the human consent routes")

	with := httpadapter.NewRouter(consentOptions(svc, holders, revocations, staticVerifier{
		"ops": {Subject: "u-ops", Roles: []security.Role{security.RoleOperations}},
	})...)
	assert.Equal(t, http.StatusUnauthorized, status(t, with, http.MethodGet, "/v1/consents", ""), "mounted behind Cognito")
	assert.Equal(t, http.StatusForbidden, status(t, with, http.MethodGet, "/v1/consents", "ops"), "only the insured role")
	assert.Equal(t, http.StatusUnauthorized, status(t, with, http.MethodGet, "/v1/audit/consents/cns-1", ""))
	assert.Contains(t, buf.String(), "insured consents (HU-M1.3-01)")
	assert.Equal(t, http.StatusNotFound, status(t, without, http.MethodPost, "/v1/consents/cns-1/revocations", ""), "not mounted")
	assert.Equal(t, http.StatusUnauthorized, status(t, with, http.MethodPost, "/v1/consents/cns-1/revocations", ""),
		"HU-M1.3-02: mounted behind Cognito")
	assert.Equal(t, http.StatusForbidden, status(t, with, http.MethodGet, "/v1/consents/cns-1/revocation-receipt", "ops"))
	assert.Contains(t, buf.String(), "revocation (HU-M1.3-02)")
}

func TestWithoutAQueueNoEventIsConsumed(t *testing.T) {
	buf := captureLog(t)

	consumer, err := newEventConsumer(context.Background(), config{}, nil, nil)

	require.NoError(t, err)
	assert.Nil(t, consumer)
	assert.Contains(t, buf.String(), "EVENTS_QUEUE_URL is not set: account links (CustomerLinked) are not consumed; insured users get 403 holder_not_linked")
}

func TestWithAQueueTheConsumerIsBuilt(t *testing.T) {
	t.Setenv("AWS_REGION", "us-east-2")
	t.Setenv("AWS_ACCESS_KEY_ID", "test")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "test")
	buf := captureLog(t)
	holders := consents.NewHolderService(nil, nil, nil, clockAt{}, nil)

	consumer, err := newEventConsumer(context.Background(),
		config{eventsQueueURL: "http://localhost:4566/000000000000/solventa-staging-audit"}, holders,
		partnercredentials.NewService(nil))

	require.NoError(t, err)
	assert.NotNil(t, consumer)
	assert.Contains(t, buf.String(), "domain events: consuming CustomerLinked, PartnerCredentialsIssued, PartnerCredentialsRotated, "+
		"PartnerCredentialsRevoked from http://localhost:4566/000000000000/solventa-staging-audit")
}
