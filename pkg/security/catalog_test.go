package security

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// This file is the template the consuming stories copy: a sample route catalog
// with human routes (Cognito JWT, role and ownership checks), partner routes
// (API key at the gateway, never Cognito) and the public health check, plus
// the 401/403/200 matrix every service must satisfy.

type routeKind int

const (
	humanRoute   routeKind = iota // security: [{cognitoJwt: []}]
	partnerRoute                  // security: [{partnerApiKey: []}]
	publicRoute                   // security: []
)

type sampleRoute struct {
	method, pattern, path string
	kind                  routeKind
	roles                 []Role // roles allowed by RequireRole (human routes)
}

// Owners of the sample resources used by the ownership checks.
const (
	ownPolicyOwner = "insured-1"
	ownPartnerID   = "partner-7"
)

var sampleCatalog = []sampleRoute{
	{http.MethodGet, "GET /health", "/health", publicRoute, nil},
	{http.MethodPost, "POST /v1/quotes", "/v1/quotes", humanRoute, []Role{RoleAdvisor}},
	{http.MethodGet, "GET /v1/claims/queue", "/v1/claims/queue", humanRoute, []Role{RoleOperations}},
	{http.MethodGet, "GET /v1/policies/{policyID}", "/v1/policies/pol-1", humanRoute, []Role{RoleInsured, RoleOperations}},
	{http.MethodGet, "GET /v1/partners/{partnerID}/dashboard", "/v1/partners/" + ownPartnerID + "/dashboard", humanRoute, []Role{RolePartner}},
	{http.MethodPost, "POST /v1/embedded/quotes", "/v1/embedded/quotes", partnerRoute, nil},
}

// businessCounter counts how many times each business operation ran.
type businessCounter map[string]int

// newSampleRouter mounts the catalog the way a service adapter would:
// Authenticate + RequireRole on human routes, ownership checks inside the use
// case (translated with WriteForbidden), nothing on partner and public routes.
func newSampleRouter(v Verifier, ran businessCounter) http.Handler {
	mux := http.NewServeMux()
	for _, rt := range sampleCatalog {
		business := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if err := sampleUseCase(r.Context(), rt, r); err != nil {
				switch {
				case errors.Is(err, ErrUnauthenticated):
					WriteUnauthorized(w)
				default:
					WriteForbidden(w)
				}
				return
			}
			ran[rt.pattern]++
			w.WriteHeader(http.StatusOK)
		})

		var h http.Handler = business
		if rt.kind == humanRoute {
			h = Authenticate(v)(RequireRole(rt.roles...)(business))
		}
		mux.Handle(rt.pattern, h)
	}
	return mux
}

// sampleUseCase stands for the application layer: it checks ownership before
// running the operation.
func sampleUseCase(ctx context.Context, rt sampleRoute, r *http.Request) error {
	switch rt.pattern {
	case "GET /v1/policies/{policyID}":
		owner := map[string]string{"pol-1": ownPolicyOwner}[r.PathValue("policyID")]
		return RequireOwnerOrRole(ctx, owner, RoleOperations)
	case "GET /v1/partners/{partnerID}/dashboard":
		return RequireSamePartner(ctx, r.PathValue("partnerID"))
	default:
		return nil
	}
}

func TestCatalog_HumanRoutesMatrix(t *testing.T) {
	iss := newTestIssuer(t)
	v := iss.verifier(t)

	tokenFor := func(sub string, role Role) string {
		c := iss.idClaims(sub, string(role))
		if role == RolePartner {
			c["custom:partner_id"] = ownPartnerID
		}
		return "Bearer " + iss.sign(t, "k1", c)
	}
	expired := func() string {
		c := iss.idClaims("advisor-1", "advisor")
		c["exp"] = testNow.Add(-time.Hour).Unix()
		return "Bearer " + iss.sign(t, "k1", c)
	}()

	type caller struct {
		name   string
		header string
	}
	callers := []caller{
		{"no token", ""},
		{"invalid token", "Bearer not.a.jwt"},
		{"expired token", expired},
		{"insured owner", tokenFor(ownPolicyOwner, RoleInsured)},
		{"insured not owner", tokenFor("insured-2", RoleInsured)},
		{"advisor", tokenFor("advisor-1", RoleAdvisor)},
		{"operations", tokenFor("ops-1", RoleOperations)},
		{"partner same", tokenFor("partner-user-1", RolePartner)},
	}

	// Expected status per route and caller; 200 means the business operation ran.
	want := map[string]map[string]int{
		"POST /v1/quotes": {
			"insured owner": 403, "insured not owner": 403, "advisor": 200, "operations": 403, "partner same": 403,
		},
		"GET /v1/claims/queue": {
			"insured owner": 403, "insured not owner": 403, "advisor": 403, "operations": 200, "partner same": 403,
		},
		"GET /v1/policies/{policyID}": {
			"insured owner": 200, "insured not owner": 403, "advisor": 403, "operations": 200, "partner same": 403,
		},
		"GET /v1/partners/{partnerID}/dashboard": {
			"insured owner": 403, "insured not owner": 403, "advisor": 403, "operations": 403, "partner same": 200,
		},
	}

	for _, rt := range sampleCatalog {
		if rt.kind != humanRoute {
			continue
		}
		for _, c := range callers {
			t.Run(fmt.Sprintf("%s/%s", rt.pattern, c.name), func(t *testing.T) {
				ran := businessCounter{}
				router := newSampleRouter(v, ran)
				req := httptest.NewRequest(rt.method, rt.path, nil)
				if c.header != "" {
					req.Header.Set("Authorization", c.header)
				}
				rec := httptest.NewRecorder()

				router.ServeHTTP(rec, req)

				expected, ok := want[rt.pattern][c.name]
				if !ok {
					expected = http.StatusUnauthorized // no, invalid or expired token
				}
				assert.Equal(t, expected, rec.Code)
				if expected == http.StatusOK {
					assert.Equal(t, 1, ran[rt.pattern])
				} else {
					assert.Equal(t, 0, ran[rt.pattern], "the business operation must not run")
				}
			})
		}
	}
}

func TestCatalog_PartnerOfAnotherPartnerIsForbidden(t *testing.T) {
	iss := newTestIssuer(t)
	ran := businessCounter{}
	router := newSampleRouter(iss.verifier(t), ran)

	c := iss.idClaims("partner-user-8", "partner")
	c["custom:partner_id"] = "partner-8"
	req := httptest.NewRequest(http.MethodGet, "/v1/partners/"+ownPartnerID+"/dashboard", nil)
	req.Header.Set("Authorization", "Bearer "+iss.sign(t, "k1", c))
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Empty(t, ran)
}

// countingVerifier records every verification so the test can prove that
// partner and public routes never reach the user identity provider.
type countingVerifier struct {
	inner Verifier
	calls int
}

func (c *countingVerifier) Verify(ctx context.Context, raw string) (Claims, error) {
	c.calls++
	return c.inner.Verify(ctx, raw)
}

func TestCatalog_PartnerRoutesNeverUseVerifier(t *testing.T) {
	iss := newTestIssuer(t)
	v := &countingVerifier{inner: iss.verifier(t)}
	ran := businessCounter{}
	router := newSampleRouter(v, ran)
	bearer := "Bearer " + iss.sign(t, "k1", iss.idClaims("advisor-1", "advisor"))

	for _, rt := range sampleCatalog {
		if rt.kind == humanRoute {
			continue
		}
		for _, header := range []string{"", bearer, "Bearer not.a.jwt"} {
			req := httptest.NewRequest(rt.method, rt.path, nil)
			req.Header.Set("x-api-key", "partner-test-key")
			if header != "" {
				req.Header.Set("Authorization", header)
			}
			rec := httptest.NewRecorder()

			router.ServeHTTP(rec, req)
			assert.Equal(t, http.StatusOK, rec.Code, rt.pattern)
		}
	}
	assert.Equal(t, 0, v.calls, "machine-to-machine and public routes never use the Cognito verifier")
	assert.Equal(t, 3, ran["POST /v1/embedded/quotes"])
}

func TestCatalog_HumanRouteRejectsAPIKeyOnly(t *testing.T) {
	iss := newTestIssuer(t)
	ran := businessCounter{}
	router := newSampleRouter(iss.verifier(t), ran)

	req := httptest.NewRequest(http.MethodPost, "/v1/quotes", nil)
	req.Header.Set("x-api-key", "partner-test-key")
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Empty(t, ran)
}

func TestCatalog_OwnershipCheckWithoutAuthenticateIs401(t *testing.T) {
	rec := httptest.NewRecorder()
	rt := sampleCatalog[3]
	req := httptest.NewRequest(rt.method, rt.path, nil)
	req.SetPathValue("policyID", "pol-1")

	err := sampleUseCase(req.Context(), rt, req)
	require.ErrorIs(t, err, ErrUnauthenticated)
	WriteUnauthorized(rec)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}
