package httpadapter_test

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"

	httpadapter "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/adapters/http"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/triage"
)

// Who reaches each route of the triage (HU-W2.2-01, criterion 4): operations
// every route, the insured only the triage of a claim (and the service checks
// it is theirs), nobody else anything.

func TestEveryTriageRouteNeedsItsRole(t *testing.T) {
	routes := map[string]map[string]int{
		"/v1/claims/" + claimID + "/triage": {
			"": http.StatusUnauthorized, "operations": http.StatusOK, "insured": http.StatusOK,
			"advisor": http.StatusForbidden, "partner": http.StatusForbidden,
		},
		batchPath(claimID): {
			"": http.StatusUnauthorized, "operations": http.StatusOK, "insured": http.StatusForbidden,
			"advisor": http.StatusForbidden, "partner": http.StatusForbidden,
		},
		"/v1/claims/triage-policy": {
			"": http.StatusUnauthorized, "operations": http.StatusOK, "insured": http.StatusForbidden,
			"advisor": http.StatusForbidden, "partner": http.StatusForbidden,
		},
	}
	for path, want := range routes {
		for token, status := range want {
			f := &fakeTriage{view: triage.View{ClaimID: claimID, State: domain.TriagePending},
				summaries: []triage.Summary{{ClaimID: claimID, State: domain.TriagePending}}}
			rec := do(triageRouter(f), http.MethodGet, path, token, nil, nil)
			assert.Equal(t, status, rec.Code, "%s as %q", path, token)
			if status != http.StatusOK {
				assert.Zero(t, f.calls, "%s as %q reached the service", path, token)
			}
		}
	}
	rec := do(triageRouter(&fakeTriage{}), http.MethodGet, "/v1/claims/triage", "unknown-token", nil, nil)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

// The routes of the triage hang from /v1/claims next to the routes of
// HU-W2.1-01, and none of them takes the place of another.
func TestTheTriageRoutesDoNotClashWithTheReadsOfTheClaims(t *testing.T) {
	reads := &fakeReads{claim: webClaim()}
	web := &fakeWeb{}
	f := &fakeTriage{view: triage.View{ClaimID: claimID, State: domain.TriagePending}}
	h := httpadapter.NewRouter(
		httpadapter.WithClaimReads(reads, callers),
		httpadapter.WithWebFilings(web, &fakeUploads{}, callers),
		httpadapter.WithTriage(f, callers))

	rec := do(h, http.MethodGet, "/v1/claims/triage-policy", "operations", nil, nil)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "triage-policy-v1", decode(t, rec)["version"])
	assert.Empty(t, reads.gotID, "not read as the claim triage-policy")

	rec = do(h, http.MethodGet, batchPath(claimID), "operations", nil, nil)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Empty(t, reads.gotID, "not read as the claim triage")

	calls := f.calls
	rec = do(h, http.MethodGet, "/v1/claims/"+claimID, "insured", nil, nil)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, claimID, reads.gotID)
	assert.Equal(t, calls, f.calls, "the detail of a claim is not its triage")

	rec = do(h, http.MethodGet, "/v1/claims/"+claimID+"/triage", "insured", nil, nil)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "insured", decode(t, rec)["view"])

	rec = do(h, http.MethodGet, "/v1/claims/filing-options", "insured", nil, nil)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, 1, web.calls, "the filing options keep their route")

	rec = do(h, http.MethodGet, "/v1/claims/triage-policy", "insured", nil, nil)
	assert.Equal(t, http.StatusForbidden, rec.Code, "an insured does not fall into the detail of a claim")
}
