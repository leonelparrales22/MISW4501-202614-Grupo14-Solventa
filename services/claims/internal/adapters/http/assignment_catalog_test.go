package httpadapter_test

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	httpadapter "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/adapters/http"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/triage"
)

// Who reaches each route of the assignment and of the registry of providers
// (HU-W2.3-01, assumption S14): operations only.

type assignmentRoute struct {
	method, path, body string
	ok                 int
}

var assignmentRoutes = []assignmentRoute{
	{http.MethodGet, "/v1/claims/" + claimID + "/assignment", "", http.StatusOK},
	{http.MethodPost, "/v1/claims/" + claimID + "/incident-location",
		`{"address": "Calle 1 # 2-3", "latitude": 4, "longitude": -74}`, http.StatusCreated},
	{http.MethodPost, "/v1/claims/" + claimID + "/provider-searches", "", http.StatusCreated},
	{http.MethodPost, "/v1/claims/" + claimID + "/assignment", `{"provider_id": "prv-abc", "search_seq": 1}`,
		http.StatusCreated},
	{http.MethodGet, assignmentBatch(claimID), "", http.StatusOK},
	{http.MethodGet, "/v1/claims/assignment-policy", "", http.StatusOK},
	{http.MethodGet, "/v1/claims/providers", "", http.StatusOK},
	{http.MethodPost, "/v1/claims/providers", profileJSON, http.StatusCreated},
	{http.MethodGet, "/v1/claims/providers/prv-servicios-hogar-norte", "", http.StatusOK},
	{http.MethodPost, "/v1/claims/providers/prv-servicios-hogar-norte/profile", profileJSON, http.StatusOK},
	{http.MethodPost, "/v1/claims/providers/prv-servicios-hogar-norte/availability", `{"availability": "available"}`,
		http.StatusOK},
}

func assignmentAndRegistry(a *fakeAssigner, r *fakeRegistry) http.Handler {
	return httpadapter.NewRouter(httpadapter.WithAssignment(a, 300*time.Second, analysts),
		httpadapter.WithProviders(r, analysts))
}

func TestEveryAssignmentRouteIsOfOperationsOnly(t *testing.T) {
	for _, route := range assignmentRoutes {
		for token, status := range map[string]int{"": http.StatusUnauthorized, "unknown-token": http.StatusUnauthorized,
			"insured": http.StatusForbidden, "advisor": http.StatusForbidden, "partner": http.StatusForbidden,
			"operations": route.ok} {
			a, r := &fakeAssigner{view: proposed()}, &fakeRegistry{provider: hogarNorte()}
			got, _, _ := send(assignmentAndRegistry(a, r), route.method, route.path, token, route.body)
			assert.Equal(t, status, got, "%s %s as %q", route.method, route.path, token)
			if status != route.ok {
				assert.Zero(t, a.calls+r.calls, "%s %s as %q reached the service", route.method, route.path, token)
			}
		}
	}
}

// The routes of the assignment hang from /v1/claims next to those of the
// other stories, and none of them takes the place of another.
func TestTheAssignmentRoutesDoNotClashWithTheOtherRoutesOfTheClaims(t *testing.T) {
	reads := &fakeReads{claim: webClaim()}
	tri := &fakeTriage{view: triage.View{ClaimID: claimID, State: domain.TriagePending}}
	eval := &fakeEvaluation{}
	rev := &fakeReviewer{}
	a, r := &fakeAssigner{view: proposed()}, &fakeRegistry{provider: hogarNorte()}
	h := httpadapter.NewRouter(
		httpadapter.WithClaimReads(reads, analysts),
		httpadapter.WithWebFilings(&fakeWeb{}, &fakeUploads{}, analysts),
		httpadapter.WithTriage(tri, analysts),
		httpadapter.WithEvaluation(eval, analysts),
		httpadapter.WithReviews(rev, analysts),
		httpadapter.WithAssignment(a, 300*time.Second, analysts),
		httpadapter.WithProviders(r, analysts))

	for _, route := range assignmentRoutes {
		calls := a.calls + r.calls
		got, _, _ := send(h, route.method, route.path, "operations", route.body)
		assert.Equal(t, route.ok, got, "%s %s", route.method, route.path)
		assert.Equal(t, calls+1, a.calls+r.calls, "%s %s reached the assignment", route.method, route.path)
	}
	assert.Empty(t, reads.gotID, "no route of the assignment was read as a claim")
	assert.Zero(t, tri.calls+eval.callCount+rev.calls)

	// And the routes of the other stories still reach their own.
	calls := a.calls + r.calls
	for _, path := range []string{"/v1/claims/" + claimID + "/triage", "/v1/claims/triage-policy",
		"/v1/claims/" + claimID + "/evaluation", "/v1/claims/" + claimID + "/history"} {
		rec := do(h, http.MethodGet, path, "operations", nil, nil)
		assert.NotEqual(t, http.StatusNotFound, rec.Code, path)
	}
	rec := do(h, http.MethodGet, "/v1/claims/"+claimID, "insured", nil, nil)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, claimID, reads.gotID)
	assert.Equal(t, calls, a.calls+r.calls, "the assignment was not reached by the routes of others")

	// A path that looks like both is never one of the assignment.
	for _, path := range []string{"/v1/claims/providers/" + claimID + "/assignment",
		"/v1/claims/" + strings.Repeat("a", 3) + "/assignment"} {
		rec := do(h, http.MethodGet, path, "operations", nil, nil)
		assert.Equal(t, http.StatusNotFound, rec.Code, path)
	}
}
