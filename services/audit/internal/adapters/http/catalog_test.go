package httpadapter_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/security"
	httpadapter "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/audit/internal/adapters/http"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/audit/internal/consents"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/audit/internal/domain"
)

// The 401/403/200 matrix of every human route of audit, copied from the
// template pkg/security/catalog_test.go (HA-S01): the consent evidence of
// HU-W1.2-01, the holder's routes of HU-M1.3-01 and the revocation of
// HU-M1.3-02, mounted on the real router as cmd/audit does.

type humanRoute struct {
	method, pattern, path string
	allowed               []string // callers that get the success status
	// success is the status of an allowed caller; zero means 200.
	success int
	// body and key are sent with the request (the revocation).
	body, key string
}

func TestCatalogOfTheHumanRoutes(t *testing.T) {
	store := &memStore{}
	svc := consents.NewService(store, &fixedClock{now: granted})
	created, _, err := svc.Grant(context.Background(), domain.GrantRequest{
		CustomerID: "cust-1", DocumentID: "1020304050", FullName: "Ana María Pérez", TermsVersion: "v1",
		AcceptedSources: []string{"open_finance", "open_data", "kyc"}, AcceptedDataPolicy: true, Channel: "embedded",
		PartnerID: "aliado-banco-1", ApplicationID: "app-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	holder := newHolderFake()
	verifier := tokenVerifier{
		"insured-a":  {Subject: "sub-a", Roles: []security.Role{security.RoleInsured}},
		"operations": {Subject: "u-ops", Roles: []security.Role{security.RoleOperations}},
		"advisor":    {Subject: "u-adv", Roles: []security.Role{security.RoleAdvisor}},
		"partner":    {Subject: "u-par", Roles: []security.Role{security.RolePartner}},
	}
	router := httpadapter.NewRouter(httpadapter.WithConsents(svc), httpadapter.WithConsentReads(svc, verifier),
		httpadapter.WithHolderConsents(holder, verifier), httpadapter.WithHolderRevocations(holder, newRevocationFake(), verifier))

	routes := []humanRoute{
		{method: http.MethodGet, pattern: "GET /v1/audit/consents/{consent_id}", path: "/v1/audit/consents/" + created.ID,
			allowed: []string{"operations", "advisor"}},
		{method: http.MethodGet, pattern: "GET /v1/consents", path: "/v1/consents", allowed: []string{"insured-a"}},
		{method: http.MethodGet, pattern: "GET /v1/consents/{consent_id}", path: "/v1/consents/cns-a-active", allowed: []string{"insured-a"}},
		// The same key on every call: the first allowed one gets 201 and the
		// others the same revocation with 200; both are a success.
		{method: http.MethodPost, pattern: "POST /v1/consents/{consent_id}/revocations", path: "/v1/consents/cns-a-active/revocations",
			allowed: []string{"insured-a"}, success: http.StatusCreated, body: revokeBody, key: validKey},
		{method: http.MethodGet, pattern: "GET /v1/consents/{consent_id}/revocation-receipt",
			path: "/v1/consents/cns-a-revoked/revocation-receipt", allowed: []string{"insured-a"}},
	}
	callers := []string{"", "forged", "insured-a", "operations", "advisor", "partner"}

	for _, rt := range routes {
		for _, caller := range callers {
			t.Run(fmt.Sprintf("%s/%q", rt.pattern, caller), func(t *testing.T) {
				var rec *httptest.ResponseRecorder
				if rt.method == http.MethodPost {
					rec = post(router, rt.path, caller, rt.key, rt.body)
				} else {
					rec = call(router, rt.method, rt.path, caller)
				}

				switch {
				case caller == "" || caller == "forged":
					assert.Equal(t, http.StatusUnauthorized, rec.Code)
				case contains(rt.allowed, caller):
					success := rt.success
					if success == 0 {
						success = http.StatusOK
					}
					assert.Contains(t, []int{success, http.StatusOK}, rec.Code)
				default:
					assert.Equal(t, http.StatusForbidden, rec.Code)
				}
			})
		}
	}
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
