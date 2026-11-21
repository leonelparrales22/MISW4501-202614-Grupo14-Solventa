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

// Who reaches each route of the inbox of assistance (HU-W2.3-03, assumption
// S1): operations only, and none of them takes the place of another route of
// /v1/claims.

type inboxRoute struct {
	method, path, body string
}

var inboxRoutes = []inboxRoute{
	{http.MethodGet, inboxPath, ""},
	{http.MethodGet, "/v1/claims/assistance-policy", ""},
	{http.MethodGet, inboxPath + "/" + requestID, ""},
	{http.MethodPost, inboxPath + "/" + requestID + "/attend", ""},
	{http.MethodPost, inboxPath + "/" + requestID + "/need", `{"need": "towing"}`},
	{http.MethodPost, inboxPath + "/" + requestID + "/voice-location",
		`{"address": "Vía Cali–Palmira km 4", "latitude": 3.4921, "longitude": -76.4203}`},
	{http.MethodPost, inboxPath + "/" + requestID + "/dispatch", `{"provider_id": "prv-gruas-autopista-norte"}`},
	{http.MethodPost, inboxPath + "/" + requestID + "/close", `{"reason": "duplicate"}`},
}

func TestEveryInboxRouteIsOfOperationsOnly(t *testing.T) {
	for _, route := range inboxRoutes {
		for token, status := range map[string]int{"": http.StatusUnauthorized, "unknown-token": http.StatusUnauthorized,
			"insured": http.StatusForbidden, "advisor": http.StatusForbidden, "partner": http.StatusForbidden,
			"operations": http.StatusOK} {
			f := &fakeInbox{view: dispatchedView()}
			got, _, header := send(inboxRouter(f), route.method, route.path, token, route.body)
			assert.Equal(t, status, got, "%s %s as %q", route.method, route.path, token)
			assert.Equal(t, "no-store", header.Get("Cache-Control"), "%s %s as %q", route.method, route.path, token)
			if status != http.StatusOK {
				assert.Zero(t, f.calls, "%s %s as %q reached the service", route.method, route.path, token)
			}
		}
	}
}

// everyClaimsRoute mounts the inbox next to every route of the claims of the
// other stories that hangs from /v1/claims, the assistance of the phone among
// them.
func everyClaimsRoute(f *fakeInbox, phone *fakeAssistance, reads *fakeReads, tri *fakeTriage, a *fakeAssigner,
	r *fakeRegistry, rev *fakeReviewer) http.Handler {
	both := tokens{"operations": analysts["operations"], "insured": requesters["insured"],
		"advisor": callers["advisor"], "partner": callers["partner"]}
	return httpadapter.NewRouter(
		httpadapter.WithClaimReads(reads, both),
		httpadapter.WithWebFilings(&fakeWeb{}, &fakeUploads{}, both),
		httpadapter.WithTriage(tri, both),
		httpadapter.WithEvaluation(&fakeEvaluation{}, both),
		httpadapter.WithReviews(rev, both),
		httpadapter.WithAssignment(a, 300*time.Second, both),
		httpadapter.WithProviders(r, both),
		httpadapter.WithAssistance(phone, both),
		httpadapter.WithAssistanceInbox(f, both))
}

func TestTheInboxRoutesDoNotClashWithTheOtherRoutesOfTheClaims(t *testing.T) {
	f := &fakeInbox{view: dispatchedView()}
	phone := &fakeAssistance{request: recordedWithFix(), created: true}
	reads := &fakeReads{claim: webClaim()}
	tri := &fakeTriage{view: triage.View{ClaimID: claimID, State: domain.TriagePending}}
	a, r, rev := &fakeAssigner{view: proposed()}, &fakeRegistry{provider: hogarNorte()}, &fakeReviewer{}
	h := everyClaimsRoute(f, phone, reads, tri, a, r, rev)

	for _, route := range inboxRoutes {
		calls := f.calls
		got, _, _ := send(h, route.method, route.path, "operations", route.body)
		assert.Equal(t, http.StatusOK, got, "%s %s", route.method, route.path)
		assert.Equal(t, calls+1, f.calls, "%s %s reached the inbox", route.method, route.path)
	}
	assert.Empty(t, reads.gotID, "no route of the inbox was read as a claim")
	assert.Zero(t, phone.calls+tri.calls+a.calls+r.calls, "nor reached another story")

	// The request of the phone is still the insured's, by its own routes.
	calls := f.calls
	rec := do(h, http.MethodGet, "/v1/claims/assistance-requests/"+requestID, "insured", nil, nil)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, 1, phone.calls)
	rec = do(h, http.MethodGet, "/v1/claims/assistance-requests/"+requestID, "operations", nil, nil)
	assert.Equal(t, http.StatusForbidden, rec.Code, "operations reads the requests through the inbox")

	// And the routes of the claims still reach their own.
	rec = do(h, http.MethodGet, "/v1/claims/"+claimID, "insured", nil, nil)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, claimID, reads.gotID)
	rec = do(h, http.MethodGet, "/v1/claims/assignment-policy", "operations", nil, nil)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.NotContains(t, rec.Body.String(), "assistance-policy-v1", "the policy of the assignment is its own")
	assert.Equal(t, calls, f.calls, "the inbox was not reached by the routes of others")

	// A path that looks like both is never one of the inbox.
	for _, path := range []string{"/v1/claims/" + claimID + "/assistance-inbox", "/v1/claims/assistance-inbox/" +
		requestID + "/assignment", "/v1/claims/assistance-inbox/" + requestID + "/history"} {
		rec := do(h, http.MethodGet, path, "operations", nil, nil)
		assert.Contains(t, []int{http.StatusNotFound, http.StatusMethodNotAllowed}, rec.Code, path)
		assert.False(t, strings.Contains(rec.Body.String(), "AST-2026-0912"), path)
	}
	assert.Equal(t, calls, f.calls)
}
