package integration_test

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	corepolicies "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/adapters/policies"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/resilience"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/security"
	httpadapter "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/adapters/http"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/adapters/pdf"
	policiesadapter "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/adapters/policies"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/adapters/postgres"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/filing"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/triage"
)

// Every acceptance criterion of HU-W2.2-01 (automatic priority of the claim)
// over HTTP: the real router, use cases and sweep, PostgreSQL in a container
// and the claims filed through the real web filing. Only the policy core, the
// token verification and the bucket are doubles, as in the test of HU-W2.1-01.

func init() {
	verifier["analyst"] = security.Claims{Subject: "sub-analyst", Roles: []security.Role{security.RoleOperations}}
	verifier["partner"] = security.Claims{Subject: "sub-partner", Roles: []security.Role{security.RolePartner}}
}

// sweepInterval is the interval of the sweep in these tests; the service uses
// 10 seconds.
const sweepInterval = 100 * time.Millisecond

// startWithTriage is the claims service as cmd/claims composes it with
// DATABASE_URL, AUTH_* and POLICY_CORE_URL, with the sweep of the triage
// running.
func startWithTriage(t *testing.T, url string, core *policyCore) service {
	t.Helper()
	ctx := context.Background()
	db, err := postgres.Open(ctx, url)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, postgres.Migrate(ctx, db))

	records, supports, objects := postgres.NewClaimRecordStore(db), postgres.NewSupportStore(db), newBucket()
	directory := policiesadapter.NewDirectory(corepolicies.NewClient(corepolicies.Config{
		BaseURL: core.srv.URL, Timeout: 500 * time.Millisecond,
		Breaker: resilience.Config{ConsecutiveFailures: 100, OpenTimeout: time.Second, HalfOpenSuccesses: 1},
	}))
	priorities := startSweep(t, db)
	return service{db: db, bucket: objects, Handler: httpadapter.NewRouter(
		httpadapter.WithFilings(filing.NewService(records, objects, clock{}), verifier),
		httpadapter.WithClaimReads(filing.NewQueryService(records, pdf.NewReceiptRenderer()).WithObjects(objects), verifier),
		httpadapter.WithWebFilings(filing.NewWebService(records, supports, directory, clock{}),
			filing.NewSupportService(supports, objects, clock{}), verifier),
		httpadapter.WithTriage(priorities, verifier),
	)}
}

// startSweep starts one task of the triage over the database, stopped when
// the test ends.
func startSweep(t *testing.T, db *sql.DB) *triage.Service {
	t.Helper()
	svc := triage.NewService(postgres.TriageStoreOf(postgres.NewClaimRecordStore(db)), clock{})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		triage.NewWorker(svc, sweepInterval, 0).Run(ctx)
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	return svc
}

// cents turns pesos into the cents of the API.
func cents(cop int64) int64 { return cop * 100 }

func waterDamageBody(t *testing.T, svc service, token string, amount any) map[string]any {
	t.Helper()
	body := map[string]any{
		"policy_id":     "HG-2025-880231",
		"incident_type": "water_damage",
		"occurred_at":   now.Add(-48 * time.Hour).Format(time.RFC3339),
		"place":         "Carrera 15 # 93-40, apto 402, Bogotá",
		"description":   "Filtración desde el apartamento superior que afectó techo y muro de la cocina.",
		"supports": []map[string]any{
			{"requirement_code": "damage_photos", "support_ids": uploadIDs(t, svc, token, jpegFile, 2)},
			{"requirement_code": "repair_quote", "support_ids": uploadIDs(t, svc, token, pdfFile, 1)},
		},
	}
	if amount != nil {
		body["estimated_amount_minor"] = amount
	}
	return body
}

func theftBody(t *testing.T, svc service, token string) map[string]any {
	t.Helper()
	return map[string]any{
		"policy_id":     "AU-2025-118204",
		"incident_type": "theft",
		"occurred_at":   now.Add(-24 * time.Hour).Format(time.RFC3339),
		"place":         "Parqueadero del centro comercial Andino, Bogotá",
		"description":   "Al regresar al parqueadero el vehículo ya no estaba en su lugar.",
		"supports": []map[string]any{
			{"requirement_code": "police_report", "support_ids": uploadIDs(t, svc, token, pdfFile, 1)},
			{"requirement_code": "driver_document", "support_ids": uploadIDs(t, svc, token, pdfFile, 1)},
		},
	}
}

// fileClaim files the claim from the portal and returns its id.
func fileClaim(t *testing.T, svc service, key string, body map[string]any) string {
	t.Helper()
	rec, m := file(t, svc, "carolina", key, body)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	return m["claim_id"].(string)
}

// triageOf reads the triage of the claim as the token.
func triageOf(t *testing.T, svc service, token, id string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	return call(t, svc, http.MethodGet, "/v1/claims/"+id+"/triage", token, nil, nil)
}

// waitForCalculations waits, at most the 60 seconds of criterion 1, until the
// claim has n calculations, and returns its triage as operations.
func waitForCalculations(t *testing.T, svc service, id string, n int) map[string]any {
	t.Helper()
	var body map[string]any
	require.Eventually(t, func() bool {
		rec, m := triageOf(t, svc, "analyst", id)
		body = m
		return rec.Code == http.StatusOK && len(m["history"].([]any)) == n
	}, 60*time.Second, 50*time.Millisecond, "the claim %s did not reach %d calculations", id, n)
	return body
}

func countRows(t *testing.T, db *sql.DB, query string, args ...any) int {
	t.Helper()
	var n int
	require.NoError(t, db.QueryRowContext(context.Background(), query, args...).Scan(&n))
	return n
}

func current(body map[string]any) map[string]any { return body["current"].(map[string]any) }

func latest(body map[string]any) map[string]any { return body["history"].([]any)[0].(map[string]any) }

func TestClaimTriageAcceptanceCriteria(t *testing.T) {
	requireDocker(t)
	svc := startWithTriage(t, newDatabase(t), newPolicyCore(t))

	var waterID string

	t.Run("CA-1 a claim that enters evaluation gets its level within 60 seconds, with the date and time", func(t *testing.T) {
		filedAt := time.Now().UTC()
		waterID = fileClaim(t, svc, "water-1", waterDamageBody(t, svc, "carolina", cents(4_800_000)))

		rec, pending := triageOf(t, svc, "analyst", waterID)
		require.Equal(t, http.StatusOK, rec.Code)
		assert.Contains(t, []any{"pending", "assessed"}, pending["state"])

		body := waitForCalculations(t, svc, waterID, 1)
		assert.Equal(t, "assessed", body["state"])
		assert.Equal(t, []any{"entered_evaluation"}, latest(body)["causes"])
		assert.Equal(t, "system", latest(body)["actor"])
		assessedAt, err := time.Parse(time.RFC3339Nano, current(body)["assessed_at"].(string))
		require.NoError(t, err)
		assert.WithinDuration(t, filedAt, assessedAt, 60*time.Second)
		assert.Equal(t, 1, countRows(t, svc.db, `SELECT count(*) FROM claim_triage WHERE claim_id = $1`, waterID))
	})

	t.Run("CA-2 the level combines the severity of the claim type and the band of the amount", func(t *testing.T) {
		body := waitForCalculations(t, svc, waterID, 1)
		assert.Equal(t, 2.0, current(body)["level"], "water damage for 4.8 million")
		assert.Equal(t, "medium", current(body)["severity"])
		assert.Equal(t, "low", current(body)["amount_band"])
		assert.InDelta(t, cents(4_800_000), current(body)["estimated_amount_minor"], 0)
		assert.Equal(t, "water_damage", current(body)["coverage"])

		theft := fileClaim(t, svc, "theft-1", theftBody(t, svc, "carolina"))
		body = waitForCalculations(t, svc, theft, 1)
		assert.Equal(t, 1.0, current(body)["level"], "a theft is Triage 1 without amount")
		assert.Equal(t, "unknown", current(body)["amount_band"])
		assert.Nil(t, current(body)["estimated_amount_minor"])

		collision := collisionBody(t, svc, "carolina")
		collision["estimated_amount_minor"] = cents(40_000_000)
		id := fileClaim(t, svc, "collision-1", collision)
		body = waitForCalculations(t, svc, id, 1)
		assert.Equal(t, 1.0, current(body)["level"], "a collision above 30 million")
		assert.Equal(t, "high", current(body)["amount_band"])
	})

	t.Run("CA-3 a change of the amount or the claim type recalculates the level with its cause", func(t *testing.T) {
		// The stories of sprint 3 will change the claim; the sweep sees it.
		_, err := svc.db.Exec(`UPDATE claim SET estimated_amount_minor = $1 WHERE id = $2`, cents(40_000_000), waterID)
		require.NoError(t, err)
		body := waitForCalculations(t, svc, waterID, 2)
		assert.Equal(t, 1.0, current(body)["level"])
		assert.Equal(t, 2.0, latest(body)["previous_level"])
		assert.Equal(t, []any{"estimated_amount_changed"}, latest(body)["causes"])

		_, err = svc.db.Exec(`UPDATE claim SET incident_type = 'glass', estimated_amount_minor = $1 WHERE id = $2`,
			cents(900_000), waterID)
		require.NoError(t, err)
		body = waitForCalculations(t, svc, waterID, 3)
		assert.Equal(t, 3.0, current(body)["level"], "glass for little money")
		assert.Equal(t, 1.0, latest(body)["previous_level"])
		assert.Equal(t, []any{"estimated_amount_changed", "incident_type_changed", "coverage_changed"}, latest(body)["causes"])

		// The evaluation of HU-W2.2-03 moves the claim to in_evaluation: the
		// same information, no new calculation.
		_, err = svc.db.Exec(`UPDATE claim SET status = 'in_evaluation' WHERE id = $1`, waterID)
		require.NoError(t, err)
		time.Sleep(5 * sweepInterval)
		assert.Equal(t, 3, countRows(t, svc.db, `SELECT count(*) FROM claim_triage WHERE claim_id = $1`, waterID))
	})

	t.Run("CA-3 a claim out of evaluation keeps its last level and is not recalculated", func(t *testing.T) {
		id := fileClaim(t, svc, "water-paid", waterDamageBody(t, svc, "carolina", cents(4_800_000)))
		waitForCalculations(t, svc, id, 1)

		_, err := svc.db.Exec(`UPDATE claim SET status = 'paid', estimated_amount_minor = $1 WHERE id = $2`,
			cents(40_000_000), id)
		require.NoError(t, err)
		time.Sleep(5 * sweepInterval)

		_, body := triageOf(t, svc, "analyst", id)
		assert.Equal(t, "assessed", body["state"])
		assert.Equal(t, 2.0, current(body)["level"], "the last level stays visible")
		assert.Len(t, body["history"], 1)
	})

	t.Run("CA-4 the analyst sees level, severity and band in the list and in the detail; the insured, the level and the time", func(t *testing.T) {
		unknown := "00000000-0000-4000-8000-000000000000"
		rec, body := call(t, svc, http.MethodGet, "/v1/claims/triage?claim_id="+waterID+"&claim_id="+unknown,
			"analyst", nil, nil)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		items := body["items"].([]any)
		require.Len(t, items, 2)
		first := items[0].(map[string]any)
		assert.Equal(t, waterID, first["claim_id"])
		assert.Equal(t, 3.0, first["level"])
		assert.Equal(t, "low", first["severity"])
		assert.Equal(t, "low", first["amount_band"])
		assert.Equal(t, "not_found", items[1].(map[string]any)["state"])

		rec, body = triageOf(t, svc, "carolina", waterID)
		require.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, "insured", body["view"])
		assert.Equal(t, 3.0, body["level"])
		assert.Equal(t, 10.0, body["estimated_resolution_business_days"])
		for _, hidden := range []string{"severity", "amount", "history", "causes"} {
			assert.NotContains(t, rec.Body.String(), hidden)
		}

		rec, _ = triageOf(t, svc, "andres", waterID)
		assert.Equal(t, http.StatusForbidden, rec.Code, "another insured")
		for _, token := range []string{"advisor", "partner"} {
			rec, _ = triageOf(t, svc, token, waterID)
			assert.Equal(t, http.StatusForbidden, rec.Code, token)
		}
		rec, _ = call(t, svc, http.MethodGet, "/v1/claims/triage?claim_id="+waterID, "carolina", nil, nil)
		assert.Equal(t, http.StatusForbidden, rec.Code, "the batch is for operations")
		rec, _ = triageOf(t, svc, "", waterID)
		assert.Equal(t, http.StatusUnauthorized, rec.Code)
		rec, _ = triageOf(t, svc, "analyst", unknown)
		assert.Equal(t, http.StatusNotFound, rec.Code)
	})

	t.Run("CA-5 every level has its target next to it", func(t *testing.T) {
		rec, body := call(t, svc, http.MethodGet, "/v1/claims/triage-policy", "analyst", nil, nil)
		require.Equal(t, http.StatusOK, rec.Code)
		targets := body["targets"].([]any)
		require.Len(t, targets, 3)
		assert.Equal(t, map[string]any{"kind": "immediate", "business_days": nil, "resolution_business_days": 2.0},
			targets[0].(map[string]any)["target"])
		assert.Equal(t, 5.0, targets[1].(map[string]any)["target"].(map[string]any)["business_days"])
		assert.Equal(t, 10.0, targets[2].(map[string]any)["target"].(map[string]any)["business_days"])

		_, body = call(t, svc, http.MethodGet, "/v1/claims/triage?claim_id="+waterID, "analyst", nil, nil)
		target := body["items"].([]any)[0].(map[string]any)["target"].(map[string]any)
		assert.Equal(t, 10.0, target["business_days"], "the target of Triage 3 in the row of the list")
	})

	t.Run("the calculations cannot be changed nor deleted", func(t *testing.T) {
		for _, stmt := range []string{`UPDATE claim_triage SET level = 1`, `DELETE FROM claim_triage`, `TRUNCATE claim_triage`} {
			_, err := svc.db.Exec(stmt)
			require.Error(t, err, stmt)
			assert.True(t, strings.Contains(err.Error(), "append-only"), stmt)
		}
	})

	t.Run("a wrong amount is a field of the filing and nothing is filed", func(t *testing.T) {
		before := countClaims(t, svc.db)
		rec, body := file(t, svc, "carolina", "water-bad", waterDamageBody(t, svc, "carolina", "4.8 millones"))
		require.Equal(t, http.StatusUnprocessableEntity, rec.Code, rec.Body.String())
		assert.Contains(t, body["fields"], map[string]any{"field": "estimated_amount_minor", "code": "invalid_format"})
		assert.Equal(t, before, countClaims(t, svc.db))
	})

	t.Run("the detail of the claim shows the amount the insured declared", func(t *testing.T) {
		rec, body := call(t, svc, http.MethodGet, "/v1/claims/"+waterID, "carolina", nil, nil)
		require.Equal(t, http.StatusOK, rec.Code)
		assert.InDelta(t, cents(900_000), body["estimated_amount_minor"], 0)
	})
}

// Each task of the service runs its own sweep: two of them over one database
// record each calculation once.
func TestTwoTasksSweepingTheSameDatabaseRecordEachCalculationOnce(t *testing.T) {
	requireDocker(t)
	svc := startWithTriage(t, newDatabase(t), newPolicyCore(t))
	startSweep(t, svc.db)

	id := fileClaim(t, svc, "water-1", waterDamageBody(t, svc, "carolina", cents(4_800_000)))
	waitForCalculations(t, svc, id, 1)
	_, err := svc.db.Exec(`UPDATE claim SET estimated_amount_minor = $1 WHERE id = $2`, cents(40_000_000), id)
	require.NoError(t, err)
	waitForCalculations(t, svc, id, 2)
	time.Sleep(5 * sweepInterval)

	assert.Equal(t, 2, countRows(t, svc.db, `SELECT count(*) FROM claim_triage WHERE claim_id = $1`, id))
}

// A claim filed from the mobile app has no amount: its level comes from the
// severity alone.
func TestAClaimOfTheMobileAppIsAssessedWithoutAmount(t *testing.T) {
	requireDocker(t)
	svc := startWithTriage(t, newDatabase(t), newPolicyCore(t))
	_, _, err := postgres.NewClaimRecordStore(svc.db).Create(context.Background(), domain.Claim{
		ID: "11111111-1111-4111-8111-111111111111", Subject: "sub-carolina", FilingKey: "draft-1",
		PolicyID: "AU-2025-118204", IncidentType: domain.IncidentCollision, OccurredAt: now.Add(-time.Hour),
		Place: "Calle 80", Description: "Choque por alcance en el semáforo.", Details: map[string]string{},
		Status: domain.StatusActive, FiledAt: now,
	})
	require.NoError(t, err)

	body := waitForCalculations(t, svc, "11111111-1111-4111-8111-111111111111", 1)

	assert.Equal(t, 2.0, current(body)["level"])
	assert.Equal(t, "unknown", current(body)["amount_band"])
	assert.Equal(t, "medium", current(body)["severity"])
}
