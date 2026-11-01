package main

import (
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/security"
	httpadapter "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/audit/internal/adapters/http"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/audit/internal/consents"
)

// HU-W4.2-02 and HU-W4.2-01: the routes of the compliance portal (the
// revocation and the list of consents) are mounted only with a verifier
// (AUTH_ISSUER) and only the operations role reaches them.

var complianceRoutes = []struct{ method, path string }{
	{http.MethodGet, "/v1/audit/consents/cns-1/revocation"},
	{http.MethodPost, "/v1/audit/consents/cns-1/revocations"},
	{http.MethodPost, "/v1/audit/consents/cns-1/revocation-retries"},
	{http.MethodGet, "/v1/audit/consents"},
}

func TestTheComplianceRoutesAreMountedOnlyWithAVerifier(t *testing.T) {
	buf := captureLog(t)
	clk := clockAt{t: time.Date(2026, 10, 25, 16, 0, 0, 0, time.UTC)}
	svc := consents.NewComplianceService(consents.ComplianceDeps{Clock: clk})

	without := httpadapter.NewRouter(complianceOptions(svc, nil)...)
	for _, rt := range complianceRoutes {
		assert.Equal(t, http.StatusNotFound, status(t, without, rt.method, rt.path, ""), "not mounted: %s %s", rt.method, rt.path)
	}
	assert.Contains(t, buf.String(), "AUTH_ISSUER is not set: the revocation routes of the compliance portal (HU-W4.2-02) are not mounted")

	with := httpadapter.NewRouter(complianceOptions(svc, staticVerifier{
		"ops":     {Subject: "u-ops", Roles: []security.Role{security.RoleOperations}},
		"advisor": {Subject: "u-adv", Roles: []security.Role{security.RoleAdvisor}},
		"insured": {Subject: "sub-a", Roles: []security.Role{security.RoleInsured}},
	})...)
	for _, rt := range complianceRoutes {
		assert.Equal(t, http.StatusUnauthorized, status(t, with, rt.method, rt.path, ""), "behind Cognito: %s %s", rt.method, rt.path)
		assert.Equal(t, http.StatusUnauthorized, status(t, with, rt.method, rt.path, "forged"), "%s %s", rt.method, rt.path)
		assert.Equal(t, http.StatusForbidden, status(t, with, rt.method, rt.path, "advisor"), "%s %s", rt.method, rt.path)
		assert.Equal(t, http.StatusForbidden, status(t, with, rt.method, rt.path, "insured"), "%s %s", rt.method, rt.path)
	}
	assert.Contains(t, buf.String(), "compliance revocation (HU-W4.2-02)")
	assert.Contains(t, buf.String(), "compliance list (HU-W4.2-01): GET /v1/audit/consents for the operations role")
	assert.Contains(t, buf.String(), "nor the list of consents (HU-W4.2-01)")
}
