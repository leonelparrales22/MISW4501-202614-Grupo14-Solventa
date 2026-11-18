package integration_test

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	httpadapter "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/adapters/http"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/adapters/postgres"
)

// HU-W1.4-02: origination asks, inside the VPC, how many claims of a policy
// are in progress before it applies an endorsement. In progress is every
// status the service does not close: only paid and rejected are closed.
func TestTheOpenClaimsOfAPolicyAreTheOnesNotClosed(t *testing.T) {
	requireDocker(t)
	ctx := context.Background()
	db, err := postgres.Open(ctx, newDatabase(t))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, postgres.Migrate(ctx, db))

	insert := func(policyID, status string, n int) {
		_, err := db.ExecContext(ctx, `INSERT INTO claim (id, filing_number, cognito_sub, filing_key, fingerprint, policy_id,
			incident_type, occurred_at, place, description, status, filed_at)
			VALUES ($1, $1, 'carolina', $1, 'x', $2, 'water_damage', $3, 'Bogotá', 'Fuga en la cocina', $4, $3)`,
			fmt.Sprintf("%s-%s-%d", policyID, status, n), policyID, time.Now().UTC(), status)
		require.NoError(t, err)
	}
	for _, status := range []string{"active", "returned", "in_evaluation", "approved", "payment_exception"} {
		insert("pol-acc-endosada", status, 1)
	}
	insert("pol-acc-endosada", "paid", 1)
	insert("pol-acc-endosada", "rejected", 1)
	insert("pol-acc-endosada", "rejected", 2)
	insert("pol-acc-cerrada", "paid", 1)
	insert("pol-acc-otra", "active", 1)
	h := httpadapter.NewRouter(httpadapter.WithOpenClaims(postgres.NewClaimRecordStore(db)))

	for policyID, want := range map[string]float64{"pol-acc-endosada": 5, "pol-acc-cerrada": 0, "pol-acc-sin-siniestros": 0} {
		rec, body := call(t, h, http.MethodGet, "/internal/policies/"+policyID+"/open-claims", "", nil, nil)

		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		assert.Equal(t, map[string]any{"policy_id": policyID, "open_claims": want}, body, policyID)
	}
}
