package httpadapter_test

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	httpadapter "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/adapters/http"
)

// HU-W1.4-02: origination asks, inside the VPC, how many claims of a policy
// are in progress before it applies an endorsement.

type openClaims struct {
	n     int
	err   error
	asked string
}

func (o *openClaims) OpenClaims(_ context.Context, policyID string) (int, error) {
	o.asked = policyID
	return o.n, o.err
}

func TestTheOpenClaimsOfAPolicyAreCountedWithoutAToken(t *testing.T) {
	reader := &openClaims{n: 2}

	rec := do(httpadapter.NewRouter(httpadapter.WithOpenClaims(reader)), http.MethodGet,
		"/internal/policies/pol-acc-1/open-claims", "", nil, nil)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
	assert.Equal(t, map[string]any{"policy_id": "pol-acc-1", "open_claims": 2.0}, decode(t, rec))
	assert.Equal(t, "pol-acc-1", reader.asked)
}

func TestTheOpenClaimsNeedAValidPolicyAndAnAnswerOfTheStore(t *testing.T) {
	reader := &openClaims{}
	h := httpadapter.NewRouter(httpadapter.WithOpenClaims(reader))

	rec := do(h, http.MethodGet, "/internal/policies/pol%20acc/open-claims", "", nil, nil)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Empty(t, reader.asked, "an invalid id never reaches the store")

	reader.err = errors.New("database down")
	rec = do(h, http.MethodGet, "/internal/policies/pol-acc-1/open-claims", "", nil, nil)
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	assert.Equal(t, "unavailable", decode(t, rec)["error"])
}
