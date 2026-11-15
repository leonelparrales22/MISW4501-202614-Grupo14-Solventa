package integration_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	corepolicies "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/adapters/policies"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/resilience"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/security"
	httpadapter "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/adapters/http"
	policiesadapter "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/adapters/policies"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/adapters/postgres"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/assistance"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/filing"
)

// Every acceptance criterion of HU-M2.3-01 with a part in the backend
// (criteria 2, 4 and 5) over HTTP: the real router and use case, PostgreSQL in
// a container and the real policy core client (with its timeout and its
// circuit breaker) against a policy core of the test that can be made slow.
// Only the policy core and the token verification are doubles. Criteria 1
// and 3 (the permission and the dialer) belong to the app.

func init() {
	verifier["asistencia-carolina"] = security.Claims{Subject: "sub-carolina", Name: "Carolina Ruiz",
		Roles: []security.Role{security.RoleInsured}}
	verifier["asistencia-operaciones"] = security.Claims{Subject: "sub-ops-asistencia",
		Roles: []security.Role{security.RoleOperations}}
}

// The timeout of the client of the core in this test, and how long the slow
// core takes: more than the timeout.
const (
	assistanceCoreTimeout = 300 * time.Millisecond
	slowCoreDelay         = 900 * time.Millisecond
)

// assistanceCore is the policy core of the test: the policies of each holder,
// and a switch to make it slower than the timeout of the client.
type assistanceCore struct {
	srv   *httptest.Server
	slow  atomic.Bool
	calls atomic.Int32
}

func newAssistanceCore(t *testing.T) *assistanceCore {
	t.Helper()
	pc := &assistanceCore{}
	policy := func(id, line, product, object, status string, from, to time.Time) map[string]any {
		return map[string]any{"policy_id": id, "line": line, "product": product, "insured_object": object,
			"status": status, "valid_from": from, "valid_to": to, "covered_incidents": []string{}}
	}
	holders := map[string][]map[string]any{
		"sub-carolina": {
			policy("AU-2025-118204", "auto", "Autos", "KJT-482", "active", validFrom, validTo),
			policy("HG-2025-880231", "home", "Hogar", "Calle 80 # 15-20, apto 402", "active", validFrom, validTo),
			policy("AU-2024-000777", "auto", "Autos", "KJT-482", "expired", validFrom.AddDate(-1, 0, 0), validFrom),
		},
		"sub-andres": {policy("AU-2025-999999", "auto", "Autos", "HZX-118", "active", validFrom, validTo)},
	}
	pc.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pc.calls.Add(1)
		if pc.slow.Load() {
			select {
			case <-time.After(slowCoreDelay):
			case <-r.Context().Done():
				return
			}
		}
		holder := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/holders/"), "/policies")
		policies := holders[holder]
		if policies == nil {
			policies = []map[string]any{}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"policies": policies})
	}))
	t.Cleanup(pc.srv.Close)
	return pc
}

// startWithAssistance is the claims service as cmd/claims composes it with
// DATABASE_URL, AUTH_* and POLICY_CORE_URL: the filing and the reads of the
// claims, and the request for assistance over the same database. The breaker
// opens after three failures in a row, so the test can see it.
func startWithAssistance(t *testing.T, url string, core *assistanceCore) service {
	t.Helper()
	ctx := context.Background()
	db, err := postgres.Open(ctx, url)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, postgres.Migrate(ctx, db))

	records, objects := postgres.NewClaimRecordStore(db), newBucket()
	directory := policiesadapter.NewDirectory(corepolicies.NewClient(corepolicies.Config{
		BaseURL: core.srv.URL, Timeout: assistanceCoreTimeout,
		Breaker: resilience.Config{ConsecutiveFailures: 3, OpenTimeout: 300 * time.Millisecond, HalfOpenSuccesses: 1},
	}))
	return service{db: db, bucket: objects, Handler: httpadapter.NewRouter(
		httpadapter.WithFilings(filing.NewService(records, objects, clock{}), verifier),
		httpadapter.WithClaimReads(filing.NewQueryService(records, nil).WithObjects(objects), verifier),
		httpadapter.WithAssistance(assistance.NewService(postgres.AssistanceStoreOf(records), directory, clock{}),
			verifier),
	)}
}

// requestAssistance is the POST of the app.
func requestAssistance(t *testing.T, svc service, token, key string, body any) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	raw, err := json.Marshal(body)
	require.NoError(t, err)
	headers := map[string]string{"Content-Type": "application/json"}
	if key != "" {
		headers["Idempotency-Key"] = key
	}
	return call(t, svc, http.MethodPost, "/v1/claims/assistance-requests", token, strings.NewReader(string(raw)), headers)
}

func readAssistance(t *testing.T, svc service, token, id string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	return call(t, svc, http.MethodGet, "/v1/claims/assistance-requests/"+id, token, nil, nil)
}

// fixAt is the location of the mockup M14: Autopista Norte km 12, 8 m.
func fixAt(capturedAt time.Time) map[string]any {
	return map[string]any{"status": "sent", "latitude": 4.7712, "longitude": -74.0452, "accuracy_m": 8,
		"captured_at": capturedAt.UTC().Format(time.RFC3339)}
}

func noFix(reason string) map[string]any {
	return map[string]any{"status": "unavailable", "unavailable_reason": reason}
}

func TestAssistanceRequestAcceptanceCriteria(t *testing.T) {
	requireDocker(t)
	core := newAssistanceCore(t)
	svc := startWithAssistance(t, newDatabase(t), core)
	rows := func() int { return countRows(t, svc.db, `SELECT count(*) FROM assistance_request`) }

	t.Run("CA-2 a request with a location is recorded with its policy, UTC time and accuracy", func(t *testing.T) {
		before := time.Now().UTC().Add(-time.Second)
		capturedAt := time.Now().Add(-6 * time.Second)
		rec, body := requestAssistance(t, svc, "asistencia-carolina", "ca2-7c1f", map[string]any{
			"policy_id": "AU-2025-118204", "location": fixAt(capturedAt), "place": "Autopista Norte km 12, Bogotá"})
		require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
		assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))

		assert.Regexp(t, `^AST-\d{4}-\d{4,}$`, body["request_number"])
		assert.Equal(t, "new", body["status"])
		assert.Equal(t, map[string]any{"policy_id": "AU-2025-118204", "line": "auto", "product": "Autos",
			"insured_object": "KJT-482"}, body["policy"])
		assert.Equal(t, map[string]any{"status": "sent", "latitude": 4.7712, "longitude": -74.0452, "accuracy_m": 8.0,
			"captured_at": capturedAt.UTC().Format(time.RFC3339), "unavailable_reason": nil}, body["location"])
		assert.Equal(t, "Autopista Norte km 12, Bogotá", body["place"])
		requestedAt := instant(t, body["requested_at"])
		assert.True(t, strings.HasSuffix(body["requested_at"].(string), "Z"), "UTC")
		assert.WithinRange(t, requestedAt, before, time.Now().UTC().Add(time.Second), "the clock of the server")

		read, again := readAssistance(t, svc, "asistencia-carolina", body["request_id"].(string))
		require.Equal(t, http.StatusOK, read.Code)
		assert.Equal(t, body, again, "the read says the same as the answer")

		var (
			policyID, status, locationStatus, name, sub string
			latitude, longitude, accuracy               float64
			atUTC                                       time.Time
		)
		require.NoError(t, svc.db.QueryRow(`SELECT policy_id, status, location_status, requester_name, cognito_sub,
			latitude, longitude, accuracy_m, requested_at FROM assistance_request WHERE id = $1`, body["request_id"]).
			Scan(&policyID, &status, &locationStatus, &name, &sub, &latitude, &longitude, &accuracy, &atUTC))
		assert.Equal(t, []any{"AU-2025-118204", "new", "sent", "Carolina Ruiz", "sub-carolina", 4.7712, -74.0452, 8.0},
			[]any{policyID, status, locationStatus, name, sub, latitude, longitude, accuracy})
		assert.True(t, requestedAt.Equal(atUTC))
	})

	t.Run("CA-4 a request without a location is recorded with its reason", func(t *testing.T) {
		for _, reason := range []string{"permission_denied", "location_disabled", "no_fix"} {
			rec, body := requestAssistance(t, svc, "asistencia-carolina", "ca4-"+reason,
				map[string]any{"policy_id": "AU-2025-118204", "location": noFix(reason)})
			require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
			assert.Equal(t, map[string]any{"status": "unavailable", "latitude": nil, "longitude": nil,
				"accuracy_m": nil, "captured_at": nil, "unavailable_reason": reason}, body["location"], reason)
			assert.Nil(t, body["place"])
			assert.Equal(t, 1, countRows(t, svc.db, `SELECT count(*) FROM assistance_request
				WHERE id = $1 AND location_status = 'unavailable' AND unavailable_reason = $2 AND latitude IS NULL`,
				body["request_id"], reason))
		}

		// The two shapes are never mixed: nothing is recorded.
		n := rows()
		bad := noFix("no_fix")
		bad["latitude"], bad["longitude"] = 4.7712, -74.0452
		rec, body := requestAssistance(t, svc, "asistencia-carolina", "ca4-mixed",
			map[string]any{"policy_id": "AU-2025-118204", "location": bad})
		require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
		assert.Equal(t, "validation_failed", body["error"])
		assert.Equal(t, map[string]string{"location.latitude": "not_applicable", "location.longitude": "not_applicable"},
			fieldCodes(body))
		assert.Equal(t, n, rows())
	})

	t.Run("CA-5 only an active policy of the insured takes a request", func(t *testing.T) {
		n, calls := rows(), core.calls.Load()
		for policyID, reason := range map[string]string{
			"AU-2025-999999": "policy_not_found", "AU-2025-000404": "policy_not_found",
			"AU-2024-000777": "policy_not_active",
		} {
			rec, body := requestAssistance(t, svc, "asistencia-carolina", "ca5-"+policyID,
				map[string]any{"policy_id": policyID, "location": fixAt(time.Now())})
			require.Equal(t, http.StatusUnprocessableEntity, rec.Code, policyID)
			assert.Equal(t, "policy_not_eligible", body["error"], policyID)
			assert.Equal(t, reason, body["reason"], policyID)
			if reason == "policy_not_found" {
				assert.Nil(t, body["valid_from"], "a policy of someone else says nothing of it")
			} else {
				assert.NotNil(t, body["valid_to"])
			}
		}
		assert.Equal(t, n, rows(), "nothing is recorded")
		assert.Equal(t, calls+3, core.calls.Load(), "the policy is checked against the core every time")

		// Any line of the insured takes a request: the home policy too.
		rec, body := requestAssistance(t, svc, "asistencia-carolina", "ca5-home",
			map[string]any{"policy_id": "HG-2025-880231", "location": noFix("no_fix")})
		require.Equal(t, http.StatusCreated, rec.Code)
		assert.Equal(t, "home", body["policy"].(map[string]any)["line"])

		// An invalid body costs no call to the core.
		calls = core.calls.Load()
		rec, _ = requestAssistance(t, svc, "asistencia-carolina", "ca5-invalid",
			map[string]any{"policy_id": "AU-2025-118204", "location": map[string]any{"status": "sent"}})
		assert.Equal(t, http.StatusUnprocessableEntity, rec.Code)
		assert.Equal(t, calls, core.calls.Load())
	})

	t.Run("CA-5 with the policy core down nothing is recorded", func(t *testing.T) {
		n := rows()
		body := map[string]any{"policy_id": "AU-2025-118204", "location": fixAt(time.Now())}

		core.slow.Store(true)
		for i := 0; i < 3; i++ {
			started := time.Now()
			rec, out := requestAssistance(t, svc, "asistencia-carolina", "ca5-core-down", body)
			require.Equal(t, http.StatusServiceUnavailable, rec.Code)
			assert.Equal(t, "policy_directory_unavailable", out["error"])
			assert.Less(t, time.Since(started), assistanceCoreTimeout+500*time.Millisecond, "the timeout cuts it")
		}
		// Three failures in a row open the breaker: the answer is immediate
		// and the core is not even called.
		calls := core.calls.Load()
		started := time.Now()
		rec, _ := requestAssistance(t, svc, "asistencia-carolina", "ca5-core-down", body)
		require.Equal(t, http.StatusServiceUnavailable, rec.Code)
		assert.Less(t, time.Since(started), 100*time.Millisecond)
		assert.Equal(t, calls, core.calls.Load())
		assert.Equal(t, n, rows(), "nothing is recorded while the core is down")

		// When it is back, the same key records the request.
		core.slow.Store(false)
		require.Eventually(t, func() bool {
			rec, _ := requestAssistance(t, svc, "asistencia-carolina", "ca5-core-down", body)
			return rec.Code == http.StatusCreated
		}, 3*time.Second, 100*time.Millisecond)
		assert.Equal(t, n+1, rows())
	})

	t.Run("CA-5 what was sent is never rewritten", func(t *testing.T) {
		rec, body := requestAssistance(t, svc, "asistencia-carolina", "ca5-immutable",
			map[string]any{"policy_id": "AU-2025-118204", "location": fixAt(time.Now())})
		require.Equal(t, http.StatusCreated, rec.Code)
		id := body["request_id"]

		for _, statement := range []string{
			`UPDATE assistance_request SET latitude = 4.6, longitude = -74.1 WHERE id = $1`,
			`UPDATE assistance_request SET location_status = 'unavailable', latitude = NULL, longitude = NULL,
				accuracy_m = NULL, location_captured_at = NULL, unavailable_reason = 'no_fix' WHERE id = $1`,
			`UPDATE assistance_request SET policy_id = 'AU-2025-999999' WHERE id = $1`,
			`UPDATE assistance_request SET requested_at = now() WHERE id = $1`,
			`DELETE FROM assistance_request WHERE id = $1`,
		} {
			_, err := svc.db.Exec(statement, id)
			assert.ErrorContains(t, err, "keeps what was sent", statement)
		}
		_, err := svc.db.Exec(`TRUNCATE assistance_request`)
		assert.ErrorContains(t, err, "keeps what was sent")

		_, read := readAssistance(t, svc, "asistencia-carolina", id.(string))
		assert.Equal(t, body, read, "the request reads as it was sent")
	})

	t.Run("a retry with the same key after a lost answer finds the same request", func(t *testing.T) {
		payload := map[string]any{"policy_id": "AU-2025-118204", "location": noFix("no_fix")}
		first, created := requestAssistance(t, svc, "asistencia-carolina", "retry-1", payload)
		require.Equal(t, http.StatusCreated, first.Code)
		n := rows()

		again, retried := requestAssistance(t, svc, "asistencia-carolina", "retry-1", payload)
		require.Equal(t, http.StatusOK, again.Code)
		assert.Equal(t, created, retried, "same id, number and instant")
		assert.Equal(t, n, rows(), "one row")

		rec, body := requestAssistance(t, svc, "asistencia-carolina", "retry-1",
			map[string]any{"policy_id": "AU-2025-118204", "location": noFix("permission_denied")})
		require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
		assert.Equal(t, "idempotency_key_reused", body["error"])

		// The same key of another insured is another request.
		rec, theirs := requestAssistance(t, svc, "andres", "retry-1",
			map[string]any{"policy_id": "AU-2025-999999", "location": noFix("no_fix")})
		require.Equal(t, http.StatusCreated, rec.Code)
		assert.NotEqual(t, created["request_id"], theirs["request_id"])
	})

	t.Run("an insured does not read the request of another", func(t *testing.T) {
		_, mine := requestAssistance(t, svc, "asistencia-carolina", "owner-1",
			map[string]any{"policy_id": "AU-2025-118204", "location": noFix("no_fix")})
		id := mine["request_id"].(string)

		rec, body := readAssistance(t, svc, "andres", id)
		require.Equal(t, http.StatusNotFound, rec.Code)
		_, missing := readAssistance(t, svc, "andres", "00000000-0000-4000-8000-000000000000")
		assert.Equal(t, missing, body, "the same answer as one that does not exist")

		rec, _ = readAssistance(t, svc, "carolina", id)
		assert.Equal(t, http.StatusOK, rec.Code, "the owner, with another token of hers")
	})

	t.Run("only the insured requests and reads", func(t *testing.T) {
		n := rows()
		payload := map[string]any{"policy_id": "AU-2025-118204", "location": noFix("no_fix")}
		rec, _ := requestAssistance(t, svc, "", "access-1", payload)
		assert.Equal(t, http.StatusUnauthorized, rec.Code)
		for _, token := range []string{"advisor", "asistencia-operaciones"} {
			rec, _ := requestAssistance(t, svc, token, "access-1", payload)
			assert.Equal(t, http.StatusForbidden, rec.Code, token)
			rec, _ = readAssistance(t, svc, token, "00000000-0000-4000-8000-000000000000")
			assert.Equal(t, http.StatusForbidden, rec.Code, token)
		}
		rec, _ = requestAssistance(t, svc, "asistencia-carolina", "access-1",
			map[string]any{"policy_id": "AU-2025-118204", "sub": "sub-andres", "location": noFix("no_fix")})
		assert.Equal(t, http.StatusBadRequest, rec.Code, "the owner never comes in the body")
		assert.Equal(t, n, rows())
	})

	t.Run("a request is not a claim", func(t *testing.T) {
		claims := countRows(t, svc.db, `SELECT count(*) FROM claim`)
		_, body := requestAssistance(t, svc, "asistencia-carolina", "not-a-claim",
			map[string]any{"policy_id": "AU-2025-118204", "location": fixAt(time.Now())})
		assert.Equal(t, claims, countRows(t, svc.db, `SELECT count(*) FROM claim`), "no row in claim")

		rec, list := call(t, svc, http.MethodGet, "/v1/claims", "carolina", nil, nil)
		require.Equal(t, http.StatusOK, rec.Code)
		assert.NotContains(t, rec.Body.String(), body["request_id"])
		assert.NotContains(t, rec.Body.String(), "AST-")
		assert.Empty(t, idsOf(list))

		rec, _ = call(t, svc, http.MethodGet, "/v1/claims/"+body["request_id"].(string), "carolina", nil, nil)
		assert.Equal(t, http.StatusNotFound, rec.Code, "nor read as a claim")
	})
}
