package httpadapter_test

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"

	httpadapter "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/audit/internal/adapters/http"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/audit/internal/consents"
)

// The 401/403/2xx matrix of the three routes of the compliance portal
// (HU-W4.2-02, criterion 6), template of catalog_test.go (HA-S01). Only the
// operations role reaches them; every other caller is refused before the use
// case is called. The routes are mounted next to the other human routes of
// audit, as cmd/audit does, so a clash of paths would show here.

func TestEveryComplianceRouteNeedsOperations(t *testing.T) {
	svc := consents.NewService(&memStore{}, &fixedClock{now: granted})
	holder := newHolderFake()
	verifier := complianceVerifier()

	routes := []struct {
		method, pattern, path string
		success               int
		body, key             string
	}{
		{method: http.MethodGet, pattern: "GET /v1/audit/consents/{consent_id}/revocation",
			path: "/v1/audit/consents/cns-c-active/revocation", success: http.StatusOK},
		{method: http.MethodPost, pattern: "POST /v1/audit/consents/{consent_id}/revocations",
			path: "/v1/audit/consents/cns-c-active/revocations", success: http.StatusCreated, body: portalBody, key: validKey},
		{method: http.MethodPost, pattern: "POST /v1/audit/consents/{consent_id}/revocation-retries",
			path: "/v1/audit/consents/cns-c-partial/revocation-retries", success: http.StatusAccepted},
	}
	callers := []string{"", "forged", "insured-a", "operations", "advisor", "partner"}

	for _, rt := range routes {
		for _, caller := range callers {
			t.Run(fmt.Sprintf("%s/%q", rt.pattern, caller), func(t *testing.T) {
				fake := newComplianceFake()
				router := httpadapter.NewRouter(httpadapter.WithConsents(svc), httpadapter.WithConsentReads(svc, verifier),
					httpadapter.WithHolderConsents(holder, verifier), httpadapter.WithHolderRevocations(holder, newRevocationFake(), verifier),
					httpadapter.WithComplianceRevocations(fake, verifier))

				rec := complianceDo(router, rt.method, rt.path, caller, rt.body, withKey(rt.key))

				switch caller {
				case "", "forged":
					assert.Equal(t, http.StatusUnauthorized, rec.Code)
					assert.Zero(t, fake.calls(), "nothing is executed")
				case "operations":
					assert.Equal(t, rt.success, rec.Code, rec.Body.String())
					assert.Equal(t, 1, fake.calls())
				default:
					assert.Equal(t, http.StatusForbidden, rec.Code)
					assert.Zero(t, fake.calls(), "criterion 6: the operation is not executed")
				}
			})
		}
	}
}

// The evidence route of HU-W1.2-01 and the new routes share the prefix: each
// one reaches its own handler.
func TestTheEvidenceAndTheComplianceRoutesDoNotClash(t *testing.T) {
	svc := consents.NewService(&memStore{}, &fixedClock{now: granted})
	verifier := complianceVerifier()
	fake := newComplianceFake()
	router := httpadapter.NewRouter(httpadapter.WithConsentReads(svc, verifier), httpadapter.WithComplianceRevocations(fake, verifier))

	evidence := complianceDo(router, http.MethodGet, "/v1/audit/consents/cns-c-active", "advisor", "", nil)
	assert.Equal(t, http.StatusNotFound, evidence.Code, "the evidence handler answers (the store is empty)")
	assert.Zero(t, fake.calls())

	state := complianceDo(router, http.MethodGet, "/v1/audit/consents/cns-c-active/revocation", "advisor", "", nil)
	assert.Equal(t, http.StatusForbidden, state.Code, "the advisor reads the evidence but not the revocation")
}
