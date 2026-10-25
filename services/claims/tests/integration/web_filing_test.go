// Package integration_test exercises every acceptance criterion of HU-W2.1-01
// (filing of the claim with supports from the web portal) over HTTP: the real
// router and use cases, PostgreSQL in a container and the real policy core
// client against a policy core of the test. Only what is outside the service
// is a double: the policy core and the token verification.
package integration_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	corepolicies "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/adapters/policies"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/resilience"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/security"
	httpadapter "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/adapters/http"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/adapters/pdf"
	policiesadapter "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/adapters/policies"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/adapters/postgres"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/filing"
)

// requireDocker fails the test when REQUIRE_DOCKER=1 and Docker is not
// healthy, and skips it otherwise.
func requireDocker(t *testing.T) {
	t.Helper()
	provider, err := testcontainers.NewDockerProvider()
	if err == nil {
		defer func() { _ = provider.Close() }()
		err = provider.Health(context.Background())
	}
	if err == nil {
		return
	}
	if os.Getenv("REQUIRE_DOCKER") == "1" {
		t.Fatalf("Docker is required (REQUIRE_DOCKER=1) but is not healthy: %v", err)
	}
	t.Skipf("Docker is not available: %v", err)
}

type tokenVerifier map[string]security.Claims

func (v tokenVerifier) Verify(_ context.Context, raw string) (security.Claims, error) {
	c, ok := v[raw]
	if !ok {
		return security.Claims{}, fmt.Errorf("unknown token")
	}
	return c, nil
}

var verifier = tokenVerifier{
	"carolina": {Subject: "sub-carolina", Roles: []security.Role{security.RoleInsured}},
	"andres":   {Subject: "sub-andres", Roles: []security.Role{security.RoleInsured}},
	"advisor":  {Subject: "sub-advisor", Roles: []security.Role{security.RoleAdvisor}},
}

var (
	now       = time.Now().UTC()
	validFrom = now.AddDate(0, -6, 0).Truncate(time.Second)
	validTo   = now.AddDate(0, 6, 0).Truncate(time.Second)
)

// policyCore is the policy core of the test: the policies of each holder, and
// a switch to make it fail.
type policyCore struct {
	srv   *httptest.Server
	down  atomic.Bool
	calls atomic.Int32
}

func newPolicyCore(t *testing.T) *policyCore {
	t.Helper()
	pc := &policyCore{}
	policy := func(id, line string, status string, from, to time.Time, covers ...string) map[string]any {
		return map[string]any{"policy_id": id, "line": line, "product": map[string]string{"auto": "Autos", "home": "Hogar",
			"travel": "Viaje"}[line], "insured_object": "KJT-482", "status": status, "valid_from": from, "valid_to": to,
			"covered_incidents": covers}
	}
	holders := map[string][]map[string]any{
		"sub-carolina": {
			policy("AU-2025-118204", "auto", "active", validFrom, validTo, "collision", "theft", "third_party_damage", "other"),
			policy("HG-2025-880231", "home", "active", validFrom, validTo, "water_damage"),
			policy("VJ-2025-000931", "travel", "active", validFrom, validTo, "flight_delay"),
			policy("AU-2024-000777", "auto", "expired", validFrom.AddDate(-1, 0, 0), validFrom, "collision"),
		},
		"sub-andres": {policy("AU-2025-999999", "auto", "active", validFrom, validTo, "collision")},
	}
	pc.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pc.calls.Add(1)
		if pc.down.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
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

var (
	containerOnce sync.Once
	container     *tcpostgres.PostgresContainer
	adminURL      string
	containerErr  error
)

func TestMain(m *testing.M) {
	code := m.Run()
	if container != nil {
		_ = testcontainers.TerminateContainer(container)
	}
	os.Exit(code)
}

// newDatabase returns the URL of a new, empty database in the shared container.
func newDatabase(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	containerOnce.Do(func() {
		container, containerErr = tcpostgres.Run(ctx, "postgres:16-alpine",
			tcpostgres.WithDatabase("claims"), tcpostgres.WithUsername("claims"), tcpostgres.WithPassword("claims"),
			testcontainers.WithWaitStrategy(wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).WithStartupTimeout(2*time.Minute)))
		if containerErr == nil {
			adminURL, containerErr = container.ConnectionString(ctx, "sslmode=disable")
		}
	})
	require.NoError(t, containerErr, "start PostgreSQL")
	admin, err := postgres.Open(ctx, adminURL)
	require.NoError(t, err)
	defer func() { _ = admin.Close() }()
	name := fmt.Sprintf("w21_%d", time.Now().UnixNano())
	_, err = admin.ExecContext(ctx, "CREATE DATABASE "+name)
	require.NoError(t, err)
	return strings.Replace(adminURL, "/claims?", "/"+name+"?", 1)
}

type clock struct{}

func (clock) Now() time.Time { return time.Now() }

// service is the claims service as cmd/claims composes it with DATABASE_URL,
// AUTH_* and POLICY_CORE_URL.
type service struct {
	http.Handler
	db *sql.DB
}

func start(t *testing.T, url string, core *policyCore) service {
	t.Helper()
	ctx := context.Background()
	db, err := postgres.Open(ctx, url)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, postgres.Migrate(ctx, db))

	claims, records, supports := postgres.NewClaimStore(db), postgres.NewClaimRecordStore(db), postgres.NewSupportStore(db)
	directory := policiesadapter.NewDirectory(corepolicies.NewClient(corepolicies.Config{
		BaseURL: core.srv.URL, Timeout: 500 * time.Millisecond,
		Breaker: resilience.Config{ConsecutiveFailures: 100, OpenTimeout: time.Second, HalfOpenSuccesses: 1},
	}))
	return service{db: db, Handler: httpadapter.NewRouter(
		httpadapter.WithFilings(filing.NewService(claims, clock{}), verifier),
		httpadapter.WithClaimReads(filing.NewQueryService(records, pdf.NewReceiptRenderer()), verifier),
		httpadapter.WithWebFilings(filing.NewWebService(records, supports, directory, clock{}),
			filing.NewSupportService(supports, clock{}), verifier),
	)}
}

func call(t *testing.T, h http.Handler, method, path, token string, body io.Reader, headers map[string]string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(method, path, body)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var m map[string]any
	if strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") {
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &m), rec.Body.String())
	}
	return rec, m
}

var (
	jpegFile = append([]byte{0xFF, 0xD8, 0xFF, 0xE0}, bytes.Repeat([]byte{7}, 4096)...)
	pdfFile  = append([]byte("%PDF-1.7\n"), bytes.Repeat([]byte{8}, 4096)...)
)

func upload(t *testing.T, h http.Handler, token, name string, content []byte) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	part, err := w.CreateFormFile("file", name)
	require.NoError(t, err)
	_, err = part.Write(content)
	require.NoError(t, err)
	require.NoError(t, w.Close())
	return call(t, h, http.MethodPost, "/v1/claims/supports", token, &buf, map[string]string{"Content-Type": w.FormDataContentType()})
}

func uploadIDs(t *testing.T, h http.Handler, token string, content []byte, n int) []string {
	t.Helper()
	ids := make([]string, 0, n)
	for i := 0; i < n; i++ {
		rec, m := upload(t, h, token, fmt.Sprintf("file-%d", i), content)
		require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
		ids = append(ids, m["support_id"].(string))
	}
	return ids
}

func collisionBody(t *testing.T, h http.Handler, token string) map[string]any {
	t.Helper()
	return map[string]any{
		"policy_id":     "AU-2025-118204",
		"incident_type": "collision",
		"occurred_at":   now.Add(-48 * time.Hour).Format(time.RFC3339),
		"place":         "Calle 80 con carrera 30, Bogotá",
		"description":   "Al detenerme en el semáforo, un vehículo particular golpeó la parte trasera.",
		"supports": []map[string]any{
			{"requirement_code": "vehicle_photos", "support_ids": uploadIDs(t, h, token, jpegFile, 4)},
			{"requirement_code": "driver_document", "support_ids": uploadIDs(t, h, token, pdfFile, 1)},
			{"requirement_code": "traffic_report", "support_ids": uploadIDs(t, h, token, pdfFile, 1)},
		},
	}
}

func file(t *testing.T, h http.Handler, token, key string, body map[string]any) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	raw, err := json.Marshal(body)
	require.NoError(t, err)
	return call(t, h, http.MethodPost, "/v1/claims/web-filings", token, bytes.NewReader(raw),
		map[string]string{"Content-Type": "application/json", "Idempotency-Key": key})
}

func countClaims(t *testing.T, db *sql.DB) int {
	t.Helper()
	var n int
	require.NoError(t, db.QueryRowContext(context.Background(), `SELECT count(*) FROM claim`).Scan(&n))
	return n
}

func TestWebFilingAcceptanceCriteria(t *testing.T) {
	requireDocker(t)
	core := newPolicyCore(t)
	svc := start(t, newDatabase(t), core)

	t.Run("CA-1 only the insured's active policies, with the supports of each type", func(t *testing.T) {
		rec, m := call(t, svc, http.MethodGet, "/v1/claims/filing-options", "carolina", nil, nil)

		require.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, "claim-catalog-v1", m["catalog_version"])
		policies := m["policies"].([]any)
		require.Len(t, policies, 2, "neither travel (parametric) nor the expired one")
		auto := policies[0].(map[string]any)
		assert.Equal(t, "AU-2025-118204", auto["policy_id"])
		types := auto["incident_types"].([]any)
		require.Len(t, types, 4)
		collision := types[0].(map[string]any)
		assert.Equal(t, "collision", collision["code"])
		required := map[string]bool{}
		for _, r := range collision["requirements"].([]any) {
			req := r.(map[string]any)
			required[req["code"].(string)] = req["required"].(bool)
		}
		assert.Equal(t, map[string]bool{"vehicle_photos": true, "driver_document": true, "traffic_report": true}, required)
		assert.Equal(t, "HG-2025-880231", policies[1].(map[string]any)["policy_id"])
	})

	t.Run("CA-2 a missing support or field is named and nothing is filed", func(t *testing.T) {
		before := countClaims(t, svc.db)
		body := collisionBody(t, svc, "carolina")
		body["supports"] = body["supports"].([]map[string]any)[:2]
		body["place"] = ""

		rec, m := file(t, svc, "carolina", "ca2", body)

		require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
		assert.Equal(t, "validation_failed", m["error"])
		assert.ElementsMatch(t, []any{
			map[string]any{"field": "place", "code": "required"},
			map[string]any{"field": "supports.traffic_report", "code": "missing"},
		}, m["fields"])
		assert.Equal(t, before, countClaims(t, svc.db))
	})

	t.Run("CA-3 outside the validity or not covered is refused with the reason", func(t *testing.T) {
		before := countClaims(t, svc.db)

		body := collisionBody(t, svc, "carolina")
		body["occurred_at"] = validFrom.Add(-time.Second).Format(time.RFC3339)
		rec, m := file(t, svc, "carolina", "ca3-a", body)
		require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
		assert.Equal(t, "policy_not_eligible", m["error"])
		assert.Equal(t, "occurred_outside_validity", m["reason"])
		assert.Equal(t, validFrom.Format(time.RFC3339), m["valid_from"])

		body = collisionBody(t, svc, "carolina")
		body["policy_id"] = "HG-2025-880231"
		rec, m = file(t, svc, "carolina", "ca3-b", body)
		require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
		assert.Equal(t, "incident_not_covered", m["reason"])

		body = collisionBody(t, svc, "carolina")
		body["policy_id"] = "AU-2024-000777"
		rec, m = file(t, svc, "carolina", "ca3-c", body)
		require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
		assert.Equal(t, "policy_not_active", m["reason"])

		assert.Equal(t, before, countClaims(t, svc.db))
	})

	var filed map[string]any
	t.Run("CA-4 the claim is active, numbered, with its supports and a receipt", func(t *testing.T) {
		rec, m := file(t, svc, "carolina", "ca4", collisionBody(t, svc, "carolina"))

		require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
		filed = m
		assert.Equal(t, "active", m["status"])
		assert.Equal(t, "web", m["channel"])
		assert.Regexp(t, `^SIN-\d{4}-\d{5}$`, m["filing_number"])
		assert.NotEmpty(t, m["filed_at"])
		supports := m["supports"].(map[string]any)
		assert.EqualValues(t, 3, supports["required_total"])
		assert.EqualValues(t, 3, supports["required_attached"])
		assert.EqualValues(t, 6, supports["files"])

		id := m["claim_id"].(string)
		rec, detail := call(t, svc, http.MethodGet, "/v1/claims/"+id, "carolina", nil, nil)
		require.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, m["filing_number"], detail["filing_number"])

		rec, _ = call(t, svc, http.MethodGet, "/v1/claims/"+id+"/filing-receipt", "carolina", nil, nil)
		require.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, "application/pdf", rec.Header().Get("Content-Type"))
		assert.True(t, bytes.HasPrefix(rec.Body.Bytes(), []byte("%PDF-")))
		assert.Contains(t, rec.Body.String(), m["filing_number"].(string))

		// The same key and body: the same claim, without a second number.
		body := collisionBody(t, svc, "carolina")
		rec, first := file(t, svc, "carolina", "ca4-retry", body)
		require.Equal(t, http.StatusCreated, rec.Code)
		rec, again := file(t, svc, "carolina", "ca4-retry", body)
		require.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, first["filing_number"], again["filing_number"])
	})

	t.Run("CA-5 a file of a type or size not allowed is refused without losing the rest", func(t *testing.T) {
		body := collisionBody(t, svc, "carolina")

		rec, m := upload(t, svc, "carolina", "virus.pdf", []byte("MZ\x90\x00 an executable"))
		assert.Equal(t, http.StatusUnsupportedMediaType, rec.Code)
		assert.Equal(t, "unsupported_file_type", m["error"])

		rec, m = upload(t, svc, "carolina", "huge.pdf", append(pdfFile[:9:9], bytes.Repeat([]byte{1}, 10<<20)...))
		assert.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)
		assert.Equal(t, "file_too_large", m["error"])

		rec, m = upload(t, svc, "carolina", "limit.pdf", append(pdfFile[:9:9], bytes.Repeat([]byte{1}, 10<<20-9)...))
		assert.Equal(t, http.StatusCreated, rec.Code, "exactly 10 MiB is accepted")
		assert.EqualValues(t, 10<<20, m["byte_size"])

		// The supports uploaded before the refusals still file the claim.
		rec, _ = file(t, svc, "carolina", "ca5", body)
		assert.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	})

	t.Run("CA-6 only on the insured's own policies, and the claim is in their list", func(t *testing.T) {
		body := collisionBody(t, svc, "carolina")
		body["policy_id"] = "AU-2025-999999" // Andrés's
		rec, m := file(t, svc, "carolina", "ca6-a", body)
		require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
		assert.Equal(t, "policy_not_found", m["reason"])
		assert.Nil(t, m["valid_from"], "nothing of another insured's policy is told")

		body = collisionBody(t, svc, "carolina")
		body["supports"].([]map[string]any)[1]["support_ids"] = uploadIDs(t, svc, "andres", pdfFile, 1)
		rec, m = file(t, svc, "carolina", "ca6-b", body)
		require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
		assert.Contains(t, m["fields"], map[string]any{"field": "supports.driver_document", "code": "not_found"})

		id := filed["claim_id"].(string)
		rec, _ = call(t, svc, http.MethodGet, "/v1/claims/"+id, "andres", nil, nil)
		assert.Equal(t, http.StatusForbidden, rec.Code)
		rec, _ = call(t, svc, http.MethodGet, "/v1/claims/"+id+"/filing-receipt", "andres", nil, nil)
		assert.Equal(t, http.StatusForbidden, rec.Code)
		rec, _ = call(t, svc, http.MethodGet, "/v1/claims/"+id, "advisor", nil, nil)
		assert.Equal(t, http.StatusForbidden, rec.Code)

		rec, list := call(t, svc, http.MethodGet, "/v1/claims", "carolina", nil, nil)
		require.Equal(t, http.StatusOK, rec.Code)
		var numbers []any
		for _, item := range list["items"].([]any) {
			numbers = append(numbers, item.(map[string]any)["filing_number"])
			assert.Equal(t, "active", item.(map[string]any)["status"])
		}
		assert.Contains(t, numbers, filed["filing_number"])

		rec, list = call(t, svc, http.MethodGet, "/v1/claims", "andres", nil, nil)
		require.Equal(t, http.StatusOK, rec.Code)
		assert.Empty(t, list["items"], "nobody sees someone else's claims")
	})

	t.Run("the policy core down files nothing", func(t *testing.T) {
		body := collisionBody(t, svc, "carolina")
		before := countClaims(t, svc.db)
		core.down.Store(true)
		defer core.down.Store(false)

		rec, m := file(t, svc, "carolina", "core-down", body)
		assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
		assert.Equal(t, "policy_directory_unavailable", m["error"])
		rec, _ = call(t, svc, http.MethodGet, "/v1/claims/filing-options", "carolina", nil, nil)
		assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
		assert.Equal(t, before, countClaims(t, svc.db))
	})

	t.Run("a claim filed from the mobile app is in the same list", func(t *testing.T) {
		mobile := `{"policy_id":"AU-2025-118204","incident_type":"theft","occurred_at":"` +
			now.Add(-time.Hour).Format(time.RFC3339) + `","place":"Parqueadero","description":"Se llevaron el carro del parqueadero.",` +
			`"declaration_accepted":true}`
		rec, m := call(t, svc, http.MethodPost, "/v1/claims", "carolina", strings.NewReader(mobile),
			map[string]string{"Idempotency-Key": "draft-mobile"})
		require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())

		rec, list := call(t, svc, http.MethodGet, "/v1/claims?page_size=50", "carolina", nil, nil)
		require.Equal(t, http.StatusOK, rec.Code)
		var channels []any
		for _, item := range list["items"].([]any) {
			if item.(map[string]any)["filing_number"] == m["filing_number"] {
				channels = append(channels, item.(map[string]any)["channel"])
			}
		}
		assert.Equal(t, []any{"mobile"}, channels)
	})
}

func TestTheClaimAndItsSupportsSurviveARestart(t *testing.T) {
	requireDocker(t)
	core := newPolicyCore(t)
	url := newDatabase(t)

	first := start(t, url, core)
	rec, m := file(t, first, "carolina", "restart", collisionBody(t, first, "carolina"))
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())

	// Another instance over the same database: a redeploy.
	second := start(t, url, core)
	id := m["claim_id"].(string)
	rec, detail := call(t, second, http.MethodGet, "/v1/claims/"+id, "carolina", nil, nil)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, m["filing_number"], detail["filing_number"])
	assert.EqualValues(t, 6, detail["supports"].(map[string]any)["files"])
	rec, _ = call(t, second, http.MethodGet, "/v1/claims/"+id+"/filing-receipt", "carolina", nil, nil)
	assert.Equal(t, http.StatusOK, rec.Code)

	_, err := second.db.ExecContext(context.Background(), `UPDATE claim_support SET requirement_code = 'x'`)
	assert.Error(t, err, "the supports of a filed claim cannot be changed")
}
