package httpadapter_test

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/security"
	httpadapter "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/audit/internal/adapters/http"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/audit/internal/consents"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/audit/internal/domain"
)

// The 401/403/200 matrix of every human route of audit, copied from the
// template pkg/security/catalog_test.go (HA-S01): the consent evidence of
// HU-W1.2-01 and the holder's routes of HU-M1.3-01, mounted on the real
// router as cmd/audit does.

type humanRoute struct {
	method, pattern, path string
	allowed               []string // callers that get 200
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
		httpadapter.WithHolderConsents(holder, verifier))

	routes := []humanRoute{
		{http.MethodGet, "GET /v1/audit/consents/{consent_id}", "/v1/audit/consents/" + created.ID, []string{"operations", "advisor"}},
		{http.MethodGet, "GET /v1/consents", "/v1/consents", []string{"insured-a"}},
		{http.MethodGet, "GET /v1/consents/{consent_id}", "/v1/consents/cns-a-active", []string{"insured-a"}},
	}
	callers := []string{"", "forged", "insured-a", "operations", "advisor", "partner"}

	for _, rt := range routes {
		for _, caller := range callers {
			t.Run(fmt.Sprintf("%s/%q", rt.pattern, caller), func(t *testing.T) {
				rec := call(router, rt.method, rt.path, caller)

				want := http.StatusForbidden
				switch {
				case caller == "" || caller == "forged":
					want = http.StatusUnauthorized
				case contains(rt.allowed, caller):
					want = http.StatusOK
				}
				assert.Equal(t, want, rec.Code)
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
