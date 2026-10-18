package integration_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/resilience"
	auditadapter "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/audit"
	httpadapter "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/http"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/profiling"
)

// fakeAudit stands in for the audit service (HU-W1.2-01): a controllable
// answer to GET /v1/audit/consent-verifications.
type fakeAudit struct {
	srv   *httptest.Server
	hits  atomic.Int32
	down  atomic.Bool
	delay atomic.Int64 // nanoseconds

	mu       sync.Mutex
	consents map[string]bool // customer id -> has an active consent
}

func newFakeAudit(t *testing.T) *fakeAudit {
	t.Helper()
	a := &fakeAudit{consents: map[string]bool{}}
	a.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a.hits.Add(1)
		if d := time.Duration(a.delay.Load()); d > 0 {
			time.Sleep(d)
		}
		if a.down.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		a.mu.Lock()
		active := a.consents[r.URL.Query().Get("customer_id")]
		a.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if !active {
			_, _ = w.Write([]byte(`{"active":false,"consent_id":null,"granted_at":null,"expires_at":null,"sources":[]}`))
			return
		}
		_, _ = w.Write([]byte(`{"active":true,"consent_id":"cns-1","granted_at":"2026-10-17T12:00:00Z","expires_at":"2027-10-17T12:00:00Z","sources":["open_finance","open_data","kyc"]}`))
	}))
	t.Cleanup(a.srv.Close)
	return a
}

func (a *fakeAudit) grant(customerID string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.consents[customerID] = true
}

// consentEnv is the service as main wires it with AUDIT_URL: the profile
// routes sit behind the consent verified with audit.
type consentEnv struct {
	*parallelEnv
	audit *fakeAudit
	cache *memoryCache
	api   http.Handler
}

func newConsentEnv(t *testing.T) *consentEnv {
	t.Helper()
	cache := &memoryCache{items: map[string]domain.RiskProfile{}}
	e := buildParallelEnv(t, true, cache, productionBreaker(), func(c *profiling.ParallelConfig, o *[]profiling.Option) {
		c.Weights = profiling.DefaultWeights()
		*o = append(*o, profiling.WithCachedProfileReuse())
	})
	audit := newFakeAudit(t)
	verifier := auditadapter.NewVerifier(auditadapter.Config{
		BaseURL: audit.srv.URL, Timeout: 150 * time.Millisecond, Breaker: resilience.DefaultConfig(),
	})
	return &consentEnv{
		parallelEnv: e, audit: audit, cache: cache,
		api: httpadapter.NewRouter(profiling.NewConsentCheckedResolver(e.resolver, verifier)),
	}
}

func (e *consentEnv) post(t *testing.T, customerID string, assertedConsent bool) result {
	t.Helper()
	payload, _ := json.Marshal(map[string]any{"customer_id": customerID, "consent_active": assertedConsent})
	rec := httptest.NewRecorder()
	e.api.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/risk-profiles/resolve", bytes.NewReader(payload)))
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body), rec.Body.String())
	return result{status: rec.Code, body: body}
}

// CA-3: without a consent, no source is queried.
func TestConsentCA3WithoutAConsentNoSourceIsQueriedEvenIfTheCallerAffirmsIt(t *testing.T) {
	e := newConsentEnv(t)

	res := e.post(t, "c1", true)

	assert.Equal(t, http.StatusUnprocessableEntity, res.status)
	assert.Equal(t, "consent_required", res.body["error"])
	assert.Zero(t, e.hits(), "no external source may be queried")
	assert.EqualValues(t, 1, e.audit.hits.Load(), "the consent was asked to audit")
}

func TestConsentCA3WithoutAConsentTheCacheIsNotTouchedEither(t *testing.T) {
	e := newConsentEnv(t)
	require.NoError(t, e.cache.Set(context.Background(), domain.RiskProfile{
		CustomerID: "c1", Score: 0.1, Category: "preferred", CalculatedAt: time.Now(),
	}))

	res := e.post(t, "c1", true)

	assert.Equal(t, http.StatusUnprocessableEntity, res.status)
	assert.Nil(t, res.body["profile"], "the cached profile is not served without a consent")
	assert.Zero(t, e.hits())
}

func TestConsentCA3AnotherCustomersConsentDoesNotEnableThisOne(t *testing.T) {
	e := newConsentEnv(t)
	e.audit.grant("someone-else")

	res := e.post(t, "c1", true)

	assert.Equal(t, http.StatusUnprocessableEntity, res.status)
	assert.Zero(t, e.hits())
}

func TestConsentCA3WhenAuditIsDownNoSourceIsQueried(t *testing.T) {
	e := newConsentEnv(t)
	e.audit.grant("c1")
	e.audit.down.Store(true)

	res := e.post(t, "c1", true)

	assert.Equal(t, http.StatusServiceUnavailable, res.status)
	assert.Equal(t, "request_aborted", res.body["error"])
	assert.Zero(t, e.hits(), "fail-closed: a consent that cannot be verified is not assumed")
}

func TestConsentCA3WhenAuditIsUnreachableNoSourceIsQueried(t *testing.T) {
	e := newConsentEnv(t)
	e.audit.srv.Close()

	res := e.post(t, "c1", true)

	assert.Equal(t, http.StatusServiceUnavailable, res.status)
	assert.Zero(t, e.hits())
}

func TestConsentCA3WhenAuditIsTooSlowNoSourceIsQueried(t *testing.T) {
	e := newConsentEnv(t)
	e.audit.grant("c1")
	e.audit.delay.Store(int64(400 * time.Millisecond))

	start := time.Now()
	res := e.post(t, "c1", true)

	assert.Equal(t, http.StatusServiceUnavailable, res.status)
	assert.Less(t, time.Since(start), 350*time.Millisecond, "the verification is cut at its timeout")
	assert.Zero(t, e.hits())
}

func TestConsentCA3AnOpenCircuitFailsFastWithoutCallingAudit(t *testing.T) {
	e := newConsentEnv(t)
	e.audit.grant("c1")
	e.audit.down.Store(true)
	for i := 0; i < 3; i++ {
		require.Equal(t, http.StatusServiceUnavailable, e.post(t, "c1", true).status)
	}
	calls := e.audit.hits.Load()

	res := e.post(t, "c1", true)

	assert.Equal(t, http.StatusServiceUnavailable, res.status)
	assert.Equal(t, calls, e.audit.hits.Load(), "the open circuit does not call audit")
	assert.Zero(t, e.hits())
}

func TestConsentWithAnActiveConsentTheProfileIsResolved(t *testing.T) {
	e := newConsentEnv(t)
	e.audit.grant("c1")

	res := e.post(t, "c1", true)

	require.Equal(t, http.StatusOK, res.status)
	assert.Equal(t, "normal", res.body["mode"])
	assert.Equal(t, "external_sources", res.body["source"])
	assert.Positive(t, e.hits())
}

func TestConsentTheAffirmedFlagIsIgnoredWhenAuditHasTheConsent(t *testing.T) {
	e := newConsentEnv(t)
	e.audit.grant("c1")

	post := e.post(t, "c1", false)
	require.Equal(t, http.StatusOK, post.status, "consent_active=false does not revoke a consent audit holds")

	rec := httptest.NewRecorder()
	e.api.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/risk-profiles/c1?consent_active=false", nil))
	assert.Equal(t, http.StatusOK, rec.Code, "same for the cacheable GET")
}

func TestConsentTheGetRouteVerifiesTheConsentToo(t *testing.T) {
	e := newConsentEnv(t)
	rec := httptest.NewRecorder()

	e.api.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/risk-profiles/c1?consent_active=true", nil))

	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	assert.Zero(t, e.hits())
}

func TestConsentAfterAuditRecoversTheProfileIsResolved(t *testing.T) {
	e := newConsentEnv(t)
	e.audit.grant("c1")
	e.audit.down.Store(true)
	require.Equal(t, http.StatusServiceUnavailable, e.post(t, "c1", true).status)
	e.audit.down.Store(false)

	// The circuit is still closed after a single failure.
	res := e.post(t, "c1", true)

	assert.Equal(t, http.StatusOK, res.status)
}
