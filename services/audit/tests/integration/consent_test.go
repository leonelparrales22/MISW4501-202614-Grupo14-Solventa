// Package integration_test exercises each acceptance criterion of HU-W1.2-01
// over HTTP against the real router and a real PostgreSQL (Testcontainers),
// with no double of anything inside the service.
package integration_test

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/security"
	httpadapter "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/audit/internal/adapters/http"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/audit/internal/adapters/postgres"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/audit/internal/consents"
)

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
	if c, ok := v[raw]; ok {
		return c, nil
	}
	return security.Claims{}, security.ErrInvalidToken
}

type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *clock) set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = t
}

type env struct {
	handler http.Handler
	db      *sql.DB
	clock   *clock
}

func newEnv(t *testing.T) *env {
	t.Helper()
	requireDocker(t)
	ctx := context.Background()
	containerOnce.Do(func() {
		container, containerErr = tcpostgres.Run(ctx, "postgres:16-alpine",
			tcpostgres.WithDatabase("audit"), tcpostgres.WithUsername("audit"), tcpostgres.WithPassword("audit"),
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
	b := make([]byte, 8)
	_, err = rand.Read(b)
	require.NoError(t, err)
	name := "t_" + hex.EncodeToString(b)
	_, err = admin.ExecContext(ctx, "CREATE DATABASE "+name)
	require.NoError(t, err)
	db, err := postgres.Open(ctx, strings.Replace(adminURL, "/audit?", "/"+name+"?", 1))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, postgres.Migrate(ctx, db))

	clk := &clock{now: time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)}
	svc := consents.NewService(postgres.NewConsentStore(db), clk)
	verifier := tokenVerifier{
		"operations": {Subject: "u-ops", Roles: []security.Role{security.RoleOperations}},
		"advisor":    {Subject: "u-adv", Roles: []security.Role{security.RoleAdvisor}},
		"insured":    {Subject: "u-ins", Roles: []security.Role{security.RoleInsured}},
	}
	return &env{
		handler: httpadapter.NewRouter(httpadapter.WithConsents(svc), httpadapter.WithConsentReads(svc, verifier)),
		db:      db,
		clock:   clk,
	}
}

func (e *env) do(method, target, body, token string) (int, map[string]any) {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.Header.Set("User-Agent", "integration-test")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	e.handler.ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func grant(customer, application, termsVersion string) string {
	return `{"customer_id":"` + customer + `","document_id":"1020304050","full_name":"Ana María Pérez","terms_version":"` + termsVersion + `",
"accepted_sources":["open_finance","open_data","kyc"],"accepted_data_policy":true,"channel":"embedded",
"partner_id":"partner-1","application_id":"` + application + `"}`
}

// CA-1: what the customer sees is what is recorded.
func TestCA1TheTermsShownAreTheTermsRecorded(t *testing.T) {
	e := newEnv(t)

	status, terms := e.do(http.MethodGet, "/v1/audit/consent-terms/current", "", "")
	require.Equal(t, http.StatusOK, status)
	shown := terms["version"].(string)

	status, receipt := e.do(http.MethodPost, "/v1/audit/consents", grant("cust-1", "app-1", shown), "")
	require.Equal(t, http.StatusCreated, status)
	assert.Equal(t, shown, receipt["terms_version"])
	assert.Equal(t, terms["sources"], receipt["scope"], "the scope recorded is the one shown: sources, data and validity")
	assert.Equal(t, terms["purpose"], receipt["purpose"])
}

func TestCA1AnOutdatedVersionIsRefusedAndNothingIsRecorded(t *testing.T) {
	e := newEnv(t)

	status, body := e.do(http.MethodPost, "/v1/audit/consents", grant("cust-1", "app-1", "v0"), "")

	assert.Equal(t, http.StatusConflict, status)
	assert.Equal(t, "terms_outdated", body["error"])
	var rows int
	require.NoError(t, e.db.QueryRow(`SELECT count(*) FROM consent_record`).Scan(&rows))
	assert.Zero(t, rows)
}

func TestCA1NothingIsRecordedUnlessTheCustomerAcceptsEverySource(t *testing.T) {
	e := newEnv(t)
	partial := strings.Replace(grant("cust-1", "app-1", "v1"), `"open_finance","open_data","kyc"`, `"open_finance","open_data"`, 1)

	status, body := e.do(http.MethodPost, "/v1/audit/consents", partial, "")

	assert.Equal(t, http.StatusBadRequest, status)
	assert.Equal(t, "validation_failed", body["error"])
	var rows int
	require.NoError(t, e.db.QueryRow(`SELECT count(*) FROM consent_record`).Scan(&rows))
	assert.Zero(t, rows)
}

// CA-2: the record names the holder, the scope, the date and time and the
// channel, and can be queried later.
func TestCA2TheConsentIsQueryableAfterwards(t *testing.T) {
	e := newEnv(t)
	status, receipt := e.do(http.MethodPost, "/v1/audit/consents", grant("cust-1", "app-1", "v1"), "")
	require.Equal(t, http.StatusCreated, status)
	id := receipt["consent_id"].(string)

	for _, token := range []string{"operations", "advisor"} {
		status, evidence := e.do(http.MethodGet, "/v1/audit/consents/"+id, "", token)
		require.Equal(t, http.StatusOK, status, token)
		assert.Equal(t, "cust-1", evidence["customer_id"])
		assert.Equal(t, map[string]any{"document_id": "1020304050", "full_name": "Ana María Pérez"}, evidence["holder"])
		assert.Equal(t, "2026-10-17T12:00:00Z", evidence["granted_at"])
		assert.Equal(t, "embedded", evidence["channel"])
		assert.Equal(t, "partner-1", evidence["partner_id"])
		assert.Equal(t, "app-1", evidence["application_id"])
		assert.Equal(t, "integration-test", evidence["user_agent"])
		assert.Len(t, evidence["scope"], 3)
	}

	status, _ = e.do(http.MethodGet, "/v1/audit/consents/"+id, "", "")
	assert.Equal(t, http.StatusUnauthorized, status)
	status, _ = e.do(http.MethodGet, "/v1/audit/consents/"+id, "", "insured")
	assert.Equal(t, http.StatusForbidden, status)
	status, body := e.do(http.MethodGet, "/v1/audit/consents/cns-00000000000000000000000000000000", "", "operations")
	assert.Equal(t, http.StatusNotFound, status)
	assert.Equal(t, "consent_not_found", body["error"])
}

func TestCA2GrantingTwiceRecordsOnce(t *testing.T) {
	e := newEnv(t)
	_, first := e.do(http.MethodPost, "/v1/audit/consents", grant("cust-1", "app-1", "v1"), "")
	e.clock.set(e.clock.Now().Add(time.Hour))

	status, again := e.do(http.MethodPost, "/v1/audit/consents", grant("cust-1", "app-1", "v1"), "")

	assert.Equal(t, http.StatusOK, status)
	assert.Equal(t, first, again)
	var rows int
	require.NoError(t, e.db.QueryRow(`SELECT count(*) FROM consent_record`).Scan(&rows))
	assert.Equal(t, 1, rows)
}

func TestCA2TheVerificationFollowsTheConsentAndItsValidity(t *testing.T) {
	e := newEnv(t)
	status, none := e.do(http.MethodGet, "/v1/audit/consent-verifications?customer_id=cust-1", "", "")
	require.Equal(t, http.StatusOK, status)
	assert.Equal(t, false, none["active"], "no consent yet")

	_, receipt := e.do(http.MethodPost, "/v1/audit/consents", grant("cust-1", "app-1", "v1"), "")
	_, active := e.do(http.MethodGet, "/v1/audit/consent-verifications?customer_id=cust-1", "", "")
	assert.Equal(t, true, active["active"])
	assert.Equal(t, receipt["consent_id"], active["consent_id"])
	assert.Equal(t, []any{"open_finance", "open_data", "kyc"}, active["sources"])

	_, other := e.do(http.MethodGet, "/v1/audit/consent-verifications?customer_id=cust-2", "", "")
	assert.Equal(t, false, other["active"], "another customer is not covered")

	e.clock.set(time.Date(2027, 10, 17, 11, 59, 59, 0, time.UTC))
	_, before := e.do(http.MethodGet, "/v1/audit/consent-verifications?customer_id=cust-1", "", "")
	assert.Equal(t, true, before["active"], "one second before the 12 months")
	e.clock.set(time.Date(2027, 10, 17, 12, 0, 0, 0, time.UTC))
	_, after := e.do(http.MethodGet, "/v1/audit/consent-verifications?customer_id=cust-1", "", "")
	assert.Equal(t, false, after["active"], "at the 12 months")
}

// CA-4: the record cannot be altered, not even through the database.
func TestCA4TheRecordCannotBeAltered(t *testing.T) {
	e := newEnv(t)
	_, receipt := e.do(http.MethodPost, "/v1/audit/consents", grant("cust-1", "app-1", "v1"), "")
	id := receipt["consent_id"].(string)

	for _, stmt := range []string{
		`UPDATE consent_record SET holder_full_name = 'Someone Else'`,
		`DELETE FROM consent_record`,
		`TRUNCATE consent_record`,
	} {
		_, err := e.db.Exec(stmt)
		require.Error(t, err, stmt)
		assert.Contains(t, err.Error(), "append-only", stmt)
	}

	status, evidence := e.do(http.MethodGet, "/v1/audit/consents/"+id, "", "operations")
	require.Equal(t, http.StatusOK, status)
	assert.Equal(t, "Ana María Pérez", evidence["holder"].(map[string]any)["full_name"])
}

func TestCA4TheServiceExposesNoWayToChangeARecord(t *testing.T) {
	e := newEnv(t)
	_, receipt := e.do(http.MethodPost, "/v1/audit/consents", grant("cust-1", "app-1", "v1"), "")
	id := receipt["consent_id"].(string)

	for _, method := range []string{http.MethodPut, http.MethodPatch, http.MethodDelete} {
		for _, path := range []string{"/v1/audit/consents/" + id, "/v1/audit/consents"} {
			status, _ := e.do(method, path, `{}`, "operations")
			assert.Contains(t, []int{http.StatusNotFound, http.StatusMethodNotAllowed}, status, method+" "+path)
		}
	}
}
