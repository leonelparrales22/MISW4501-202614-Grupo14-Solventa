package httpadapter_test

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	httpadapter "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/adapters/http"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/triage"
)

// Who reaches each route of the request for assistance (HU-M2.3-01,
// assumption S1): the insured only, and the routes never take the place of
// the routes of a claim.

type assistanceRoute struct {
	method, path, body string
	ok                 int
}

var assistanceRoutes = []assistanceRoute{
	{http.MethodPost, assistancePath, withFix, http.StatusCreated},
	{http.MethodGet, assistancePath + "/" + requestID, "", http.StatusOK},
}

func TestEveryAssistanceRouteIsOfTheInsuredOnly(t *testing.T) {
	for _, route := range assistanceRoutes {
		for token, status := range map[string]int{"": http.StatusUnauthorized, "unknown-token": http.StatusUnauthorized,
			"advisor": http.StatusForbidden, "operations": http.StatusForbidden, "partner": http.StatusForbidden,
			"insured": route.ok} {
			f := &fakeAssistance{request: recordedWithFix(), created: true}
			rec := do(assistanceRouter(f), route.method, route.path, token, strings.NewReader(route.body),
				map[string]string{"Content-Type": "application/json", "Idempotency-Key": "assist-7c1f"})
			assert.Equal(t, status, rec.Code, "%s %s as %q", route.method, route.path, token)
			assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"), "%s %s as %q", route.method, route.path, token)
			if status != route.ok {
				assert.Zero(t, f.calls, "%s %s as %q reached the service", route.method, route.path, token)
			}
		}
	}
}

// claimsRouter mounts the assistance next to every route of the claims of
// the other stories that hangs from /v1/claims.
func claimsRouter(f *fakeAssistance, filings *fakeFiling, reads *fakeReads, web *fakeWeb, tri *fakeTriage,
	a *fakeAssigner, r *fakeRegistry) http.Handler {
	return httpadapter.NewRouter(
		httpadapter.WithFilings(filings, requesters),
		httpadapter.WithClaimReads(reads, requesters),
		httpadapter.WithWebFilings(web, &fakeUploads{}, requesters),
		httpadapter.WithTriage(tri, requesters),
		httpadapter.WithAssignment(a, 300*time.Second, requesters),
		httpadapter.WithProviders(r, requesters),
		httpadapter.WithAssistance(f, requesters))
}

func TestTheAssistanceRoutesDoNotClashWithTheRoutesOfTheClaims(t *testing.T) {
	f := &fakeAssistance{request: recordedWithFix(), created: true}
	filings := &fakeFiling{}
	reads := &fakeReads{claim: webClaim()}
	web := &fakeWeb{}
	tri := &fakeTriage{view: triage.View{ClaimID: claimID, State: domain.TriagePending}}
	a, r := &fakeAssigner{view: proposed()}, &fakeRegistry{provider: hogarNorte()}
	h := claimsRouter(f, filings, reads, web, tri, a, r)

	for _, route := range assistanceRoutes {
		calls := f.calls
		rec := do(h, route.method, route.path, "insured", strings.NewReader(route.body),
			map[string]string{"Content-Type": "application/json", "Idempotency-Key": "assist-7c1f"})
		assert.Equal(t, route.ok, rec.Code, "%s %s", route.method, route.path)
		assert.Equal(t, calls+1, f.calls, "%s %s reached the assistance", route.method, route.path)
	}
	assert.Empty(t, reads.gotID, "no route of the assistance was read as a claim")
	assert.Empty(t, filings.gotKey, "no request for assistance was filed as a claim")
	assert.Zero(t, web.calls+tri.calls+a.calls+r.calls)

	// There is no list of requests: GET without an id is never a claim.
	rec := do(h, http.MethodGet, assistancePath, "insured", nil, nil)
	assert.Contains(t, []int{http.StatusNotFound, http.StatusMethodNotAllowed}, rec.Code)
	assert.NotContains(t, rec.Body.String(), "filing_number")
	assert.Empty(t, reads.gotID, "the path was not read as the id of a claim")

	// And the routes of the claims still reach their own.
	calls := f.calls
	rec = do(h, http.MethodGet, "/v1/claims/"+claimID, "insured", nil, nil)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, claimID, reads.gotID)
	rec = do(h, http.MethodGet, "/v1/claims", "insured", nil, nil)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, calls, f.calls, "the assistance was not reached by the routes of others")

	// A path that looks like both is never one of the assistance.
	for _, path := range []string{"/v1/claims/" + requestID + "/assistance-requests",
		"/v1/claims/providers/assistance-requests"} {
		rec := do(h, http.MethodGet, path, "insured", nil, nil)
		assert.NotEqual(t, http.StatusOK, rec.Code, path)
	}
	assert.Equal(t, calls, f.calls)
}

// Mounted alone, as when the policy core is configured but nothing else is,
// the routes answer the same.
func TestTheAssistanceRoutesAloneAnswerTheSame(t *testing.T) {
	f := &fakeAssistance{request: recordedWithFix(), created: true}
	h := assistanceRouter(f)
	rec := do(h, http.MethodGet, assistancePath, "insured", nil, nil)
	assert.Contains(t, []int{http.StatusNotFound, http.StatusMethodNotAllowed}, rec.Code)
	rec = do(h, http.MethodGet, "/v1/claims/"+claimID, "insured", nil, nil)
	assert.Equal(t, http.StatusNotFound, rec.Code, "the claims are not mounted")
	assert.Zero(t, f.calls)
}
