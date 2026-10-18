// This file wires the personalized offer of HU-W1.2-03 end to end over HTTP:
// the router, pkg/security middleware, the offer service, the requote worker,
// the real PostgreSQL store (Testcontainers), the real profiling resolver over
// a controllable external source, a fake audit service (the consent) and a
// fake e-mail provider that deduplicates by idempotency key. It checks each
// acceptance criterion of the story over HTTP.
package integration_test

import (
	"context"
	"database/sql"
	"encoding/json"
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

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/adapters/email"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/resilience"
	auditadapter "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/audit"
	emailadapter "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/email"
	httpadapter "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/http"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/memory"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/postgres"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/issuance"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/orchestrator"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/profiling"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/rating"
)

// requireOfferDocker fails the test when REQUIRE_DOCKER=1 and Docker is not
// healthy, and skips it otherwise, so the certification run never passes
// without the tests that need Docker.
func requireOfferDocker(t *testing.T) {
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

type offerITClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *offerITClock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *offerITClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// offerSource is the external profile source: up answers a fresh profile,
// down fails like an unavailable Open Finance.
type offerSource struct {
	clock *offerITClock
	down  atomic.Bool
	calls atomic.Int32
}

func (s *offerSource) FetchProfile(_ context.Context, customerID string) (domain.RiskProfile, error) {
	s.calls.Add(1)
	if s.down.Load() {
		return domain.RiskProfile{}, ports.ErrSourceUnavailable
	}
	return domain.RiskProfile{CustomerID: customerID, Score: 0.4, Category: "standard", CalculatedAt: s.clock.Now()}, nil
}

// offerMail is the e-mail provider: it accepts a key once, like the real one.
type offerMail struct {
	srv  *httptest.Server
	mu   sync.Mutex
	keys map[string]map[string]string
	hits int
}

func newOfferMail(t *testing.T) *offerMail {
	t.Helper()
	m := &offerMail{keys: map[string]map[string]string{}}
	m.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var msg map[string]string
		_ = json.NewDecoder(r.Body).Decode(&msg)
		m.mu.Lock()
		defer m.mu.Unlock()
		m.hits++
		key := r.Header.Get("Idempotency-Key")
		if _, dup := m.keys[key]; dup {
			w.WriteHeader(http.StatusOK)
			return
		}
		m.keys[key] = msg
		w.WriteHeader(http.StatusCreated)
	}))
	t.Cleanup(m.srv.Close)
	return m
}

func (m *offerMail) messages() map[string]map[string]string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[string]map[string]string{}
	for k, v := range m.keys {
		out[k] = v
	}
	return out
}

type offerIT struct {
	srv     *httptest.Server
	db      *sql.DB
	clock   *offerITClock
	source  *offerSource
	cache   *memoryCache
	audit   *fakeAudit
	mail    *offerMail
	worker  *orchestrator.RequoteWorker
	router  *memory.OfferRouter
	service *orchestrator.OfferService
	store   *postgres.OfferStore
}

// newOfferIT builds the service over db, as main wires it with DATABASE_URL
// and AUDIT_URL. Calling it again over the same db simulates a restart.
func newOfferIT(t *testing.T, db *sql.DB, clock *offerITClock, audit *fakeAudit, mail *offerMail) *offerIT {
	t.Helper()
	e := &offerIT{db: db, clock: clock, audit: audit, mail: mail, cache: &memoryCache{items: map[string]domain.RiskProfile{}}}
	e.source = &offerSource{clock: clock}
	resolver := profiling.NewResolver(e.source, e.cache, clock)
	e.store = postgres.NewOfferStore(db)
	e.router = memory.NewOfferRouter(memory.NewOfferStub(clock), e.store)
	acceptances := memory.NewAcceptanceStore()
	verifier := auditadapter.NewVerifier(auditadapter.Config{
		BaseURL: audit.srv.URL, Timeout: time.Second, Breaker: resilience.DefaultConfig(),
	})
	e.service = orchestrator.NewOfferService(orchestrator.OfferDeps{
		Store: e.store, Offers: e.router, Consents: verifier, Profiles: resolver, Rater: rating.New(), Clock: clock,
	})
	notifier := emailadapter.NewRequoteNotifier(
		email.NewClient(email.Config{BaseURL: mail.srv.URL, Breaker: resilience.DefaultConfig()}), "https://app.solventa.test",
		emailadapter.WithRequoteRetries(5*time.Millisecond, 20*time.Millisecond, 3))
	e.worker = orchestrator.NewRequoteWorker(orchestrator.RequoteDeps{
		Store: e.store, Acceptances: acceptances, Consents: verifier, Profiles: resolver, Rater: rating.New(),
		Notifier: notifier, Clock: clock,
	}, orchestrator.RequoteConfig{})

	acceptance := issuance.NewAcceptanceService(e.router, acceptances, clock, nil)
	e.srv = httptest.NewServer(httpadapter.NewRouter(resolver,
		httpadapter.WithEmbeddedOffers(e.service),
		httpadapter.WithOffers(e.service, subjectVerifier{}),
		httpadapter.WithAcceptances(acceptance, subjectVerifier{})))
	t.Cleanup(e.srv.Close)
	return e
}

func newOfferITWithDB(t *testing.T) *offerIT {
	t.Helper()
	requireOfferDocker(t)
	clock := &offerITClock{t: time.Now().UTC().Truncate(time.Microsecond)}
	audit := newFakeAudit(t)
	audit.grant("cust-1")
	return newOfferIT(t, newChargeDB(t), clock, audit, newOfferMail(t))
}

func (e *offerIT) do(t *testing.T, method, path, token, body string) (int, map[string]any) {
	t.Helper()
	req, err := http.NewRequest(method, e.srv.URL+path, strings.NewReader(body))
	require.NoError(t, err)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = res.Body.Close() }()
	var m map[string]any
	_ = json.NewDecoder(res.Body).Decode(&m)
	return res.StatusCode, m
}

const offerBody = `{"customer_id":"cust-1","consent_id":"cns-1","partner_id":"partner-1","application_id":"app-1",
 "document_id":"1020304050","full_name":"Ana María Pérez","email":"ana@example.com","birth_date":"1985-03-14",
 "insured_amount":250000000,"term_months":120}`

func (e *offerIT) generate(t *testing.T) map[string]any {
	t.Helper()
	status, body := e.do(t, http.MethodPost, "/v1/offers", "", offerBody)
	require.Equal(t, http.StatusCreated, status, "%v", body)
	return body
}

func (e *offerIT) accept(t *testing.T, id, token string) (int, map[string]any) {
	t.Helper()
	return e.do(t, http.MethodPost, "/v1/offers/"+id+"/acceptance", token, `{"channel":"web"}`)
}

const (
	ownerCaller    = "insured:sub-a"
	strangerCaller = "insured:sub-b"
)

// CA-1: complete offer with a validity of 24 hours, claimed by the first insured.
func TestOfferCA1TheOfferIsCompleteValidFor24HoursAndClaimedOnFirstAccess(t *testing.T) {
	e := newOfferITWithDB(t)

	created := e.generate(t)
	id := created["offer_id"].(string)

	assert.Regexp(t, `^off-[0-9a-f]{32}$`, id)
	assert.Equal(t, "normal", created["mode"])
	assert.Greater(t, created["premium"], 0.0)
	assert.Equal(t, []any{"validity_24h", "onerous_beneficiary_lender", "monthly_premium", "max_insured_age_70"}, created["conditions"])
	terms := created["terms"].(map[string]any)
	assert.EqualValues(t, 250_000_000, terms["insured_amount"])
	assert.Len(t, terms["coverages"], 2)
	createdAt, _ := time.Parse(time.RFC3339Nano, created["created_at"].(string))
	expiresAt, _ := time.Parse(time.RFC3339Nano, created["expires_at"].(string))
	assert.Equal(t, 24*time.Hour, expiresAt.Sub(createdAt))

	status, body := e.do(t, http.MethodGet, "/v1/offers/"+id, ownerCaller, "")
	require.Equal(t, http.StatusOK, status)
	assert.Equal(t, "active", body["status"])
	assert.Equal(t, true, body["can_accept"])
	for _, secret := range []string{"score", "rules", "1020304050"} {
		raw, _ := json.Marshal(body)
		assert.NotContains(t, string(raw), secret)
	}

	status, _ = e.do(t, http.MethodGet, "/v1/offers/"+id, strangerCaller, "")
	assert.Equal(t, http.StatusForbidden, status, "claimed by A, read by B")
	status, _ = e.do(t, http.MethodGet, "/v1/offers/"+id, ownerCaller, "")
	assert.Equal(t, http.StatusOK, status, "the owner keeps reading it")
	status, _ = e.do(t, http.MethodGet, "/v1/offers/"+id, "advisor:adv-1", "")
	assert.Equal(t, http.StatusOK, status)
	status, _ = e.do(t, http.MethodGet, "/v1/offers/"+id, "", "")
	assert.Equal(t, http.StatusUnauthorized, status)

	status, body = e.accept(t, id, ownerCaller)
	assert.Equal(t, http.StatusAccepted, status, "the owner accepts it: %v", body)
	status, _ = e.accept(t, id, strangerCaller)
	assert.Equal(t, http.StatusForbidden, status)
}

func TestOfferCA1AnUnclaimedOfferCannotBeAccepted(t *testing.T) {
	e := newOfferITWithDB(t)
	id := e.generate(t)["offer_id"].(string)

	status, _ := e.accept(t, id, ownerCaller)

	assert.Equal(t, http.StatusForbidden, status, "the front always reads the offer first")
}

func TestOfferCA1InvalidRequestsAreRefusedBeforeAnyWork(t *testing.T) {
	e := newOfferITWithDB(t)

	status, body := e.do(t, http.MethodPost, "/v1/offers", "", strings.Replace(offerBody, `"term_months":120`, `"term_months":1`, 1))

	assert.Equal(t, http.StatusBadRequest, status)
	assert.Equal(t, "validation_failed", body["error"])
	assert.Zero(t, e.source.calls.Load())
	assert.EqualValues(t, 0, e.audit.hits.Load())
}

// CA-2: persisted with the profile copy, the rules and the engine version, immutable.
func TestOfferCA2ThePersistedRowKeepsTheProfileTheRulesAndTheVersion(t *testing.T) {
	e := newOfferITWithDB(t)
	id := e.generate(t)["offer_id"].(string)

	var version, mode string
	var profile, rules []byte
	require.NoError(t, e.db.QueryRow(`SELECT rater_version, mode, profile, rules FROM offer WHERE offer_id = $1`, id).
		Scan(&version, &mode, &profile, &rules))

	assert.Equal(t, "v1", version)
	assert.Equal(t, "normal", mode)
	var p map[string]any
	require.NoError(t, json.Unmarshal(profile, &p))
	assert.Equal(t, 0.4, p["score"])
	assert.Equal(t, "standard", p["category"])
	var rs []map[string]any
	require.NoError(t, json.Unmarshal(rules, &rs))
	assert.Len(t, rs, 5)

	for _, stmt := range []string{"UPDATE offer SET premium_minor = 1", "DELETE FROM offer"} {
		_, err := e.db.Exec(stmt)
		require.Error(t, err, stmt)
	}
}

func TestOfferCA2TheOfferAndItsOwnerSurviveARestart(t *testing.T) {
	e := newOfferITWithDB(t)
	id := e.generate(t)["offer_id"].(string)
	_, _ = e.do(t, http.MethodGet, "/v1/offers/"+id, ownerCaller, "")

	restarted := newOfferIT(t, e.db, e.clock, e.audit, e.mail)

	status, body := restarted.do(t, http.MethodGet, "/v1/offers/"+id, ownerCaller, "")
	require.Equal(t, http.StatusOK, status)
	assert.Equal(t, id, body["offer_id"])
	status, _ = restarted.do(t, http.MethodGet, "/v1/offers/"+id, strangerCaller, "")
	assert.Equal(t, http.StatusForbidden, status, "the owner persisted")
}

// CA-3: provisional, telling the cached profile (with its date) from the base one.
func TestOfferCA3WithoutTheSourceAndWithoutCacheTheOfferUsesTheBaseProfile(t *testing.T) {
	e := newOfferITWithDB(t)
	e.source.down.Store(true)

	body := e.generate(t)

	assert.Equal(t, "provisional", body["mode"])
	assert.Equal(t, "base_actuarial_profile", body["provisional_reason"])
	assert.Nil(t, body["profile_date"])
	assert.Equal(t, "open_finance", body["unavailable_source"])
	assert.Contains(t, body["conditions"], "provisional_requote_when_source_restored")
}

func TestOfferCA3WithoutTheSourceButWithACachedProfileTheOfferSaysWhen(t *testing.T) {
	e := newOfferITWithDB(t)
	e.source.down.Store(true)
	cachedAt := e.clock.Now().Add(-72 * time.Hour)
	require.NoError(t, e.cache.Set(context.Background(), domain.RiskProfile{
		CustomerID: "cust-1", Score: 0.3, Category: "standard", CalculatedAt: cachedAt, Partial: true,
	}))

	body := e.generate(t)

	assert.Equal(t, "provisional", body["mode"])
	assert.Equal(t, "cached_profile", body["provisional_reason"])
	assert.Equal(t, cachedAt.UTC().Format(time.RFC3339Nano), body["profile_date"])
}

// CA-4: a provisional offer already accepted is respected.
func TestOfferCA4AnAcceptedProvisionalOfferIsNotReplaced(t *testing.T) {
	e := newOfferITWithDB(t)
	e.source.down.Store(true)
	id := e.generate(t)["offer_id"].(string)
	_, _ = e.do(t, http.MethodGet, "/v1/offers/"+id, ownerCaller, "")
	status, _ := e.accept(t, id, ownerCaller)
	require.Equal(t, http.StatusAccepted, status)
	e.source.down.Store(false)

	replaced := e.worker.ProcessOnce(context.Background())
	e.worker.Wait()

	assert.Zero(t, replaced)
	_, body := e.do(t, http.MethodGet, "/v1/offers/"+id, ownerCaller, "")
	assert.Nil(t, body["superseded_by"])
	assert.Empty(t, e.mail.messages())
}

// CA-4: a valid, not accepted provisional offer is requoted when the source returns, and notified.
func TestOfferCA4TheSourceReturnsTheOfferIsReplacedTheCustomerIsToldOnceAndTheOldOneCannotBeAccepted(t *testing.T) {
	e := newOfferITWithDB(t)
	e.source.down.Store(true)
	id := e.generate(t)["offer_id"].(string)
	_, _ = e.do(t, http.MethodGet, "/v1/offers/"+id, ownerCaller, "")

	assert.Zero(t, e.worker.ProcessOnce(context.Background()), "the source is still down")
	e.source.down.Store(false)
	e.clock.advance(time.Hour)
	assert.Equal(t, 1, e.worker.ProcessOnce(context.Background()))
	assert.Zero(t, e.worker.ProcessOnce(context.Background()), "nothing left to requote")
	e.worker.Wait()

	status, old := e.do(t, http.MethodGet, "/v1/offers/"+id, ownerCaller, "")
	require.Equal(t, http.StatusOK, status)
	assert.Equal(t, "superseded", old["status"])
	assert.Equal(t, false, old["can_accept"])
	newID := old["superseded_by"].(string)

	msgs := e.mail.messages()
	require.Len(t, msgs, 1, "one e-mail")
	msg := msgs["offer-requoted:"+id]
	assert.Equal(t, "ana@example.com", msg["to"])
	assert.Contains(t, msg["body"], "https://app.solventa.test/embebido/oferta/"+newID)

	status, body := e.accept(t, id, ownerCaller)
	assert.Equal(t, http.StatusUnprocessableEntity, status)
	assert.Equal(t, "offer_expired", body["error"])

	status, fresh := e.do(t, http.MethodGet, "/v1/offers/"+newID, ownerCaller, "")
	require.Equal(t, http.StatusOK, status, "the owner was inherited")
	assert.Equal(t, "normal", fresh["mode"])
	assert.Equal(t, "active", fresh["status"])
	status, body = e.accept(t, newID, ownerCaller)
	assert.Equal(t, http.StatusAccepted, status, "%v", body)

	status, _ = e.do(t, http.MethodPost, "/v1/offers/"+id+"/renewal", ownerCaller, "")
	assert.Equal(t, http.StatusConflict, status)
}

func TestOfferCA4TwoServiceInstancesRequoteOnceAndNotifyOnce(t *testing.T) {
	e := newOfferITWithDB(t)
	e.source.down.Store(true)
	id := e.generate(t)["offer_id"].(string)
	other := newOfferIT(t, e.db, e.clock, e.audit, e.mail)
	e.source.down.Store(false)

	var wg sync.WaitGroup
	var total atomic.Int64
	for _, w := range []*orchestrator.RequoteWorker{e.worker, other.worker} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			total.Add(int64(w.ProcessOnce(context.Background())))
			w.Wait()
		}()
	}
	wg.Wait()

	assert.EqualValues(t, 1, total.Load())
	var n int
	require.NoError(t, e.db.QueryRow(`SELECT count(*) FROM offer WHERE supersedes = $1`, id).Scan(&n))
	assert.Equal(t, 1, n)
	assert.Len(t, e.mail.messages(), 1)
}

// CA-5: an expired offer is not accepted, says why, and a new one is offered.
func TestOfferCA5ExpiredOfferIsRefusedAndRenewedOnce(t *testing.T) {
	e := newOfferITWithDB(t)
	id := e.generate(t)["offer_id"].(string)
	_, _ = e.do(t, http.MethodGet, "/v1/offers/"+id, ownerCaller, "")

	status, body := e.do(t, http.MethodPost, "/v1/offers/"+id+"/renewal", ownerCaller, "")
	assert.Equal(t, http.StatusConflict, status)
	assert.Equal(t, "offer_still_valid", body["error"])

	e.clock.advance(24*time.Hour - time.Nanosecond)
	status, _ = e.accept(t, id, ownerCaller)
	assert.Equal(t, http.StatusAccepted, status, "one nanosecond before the expiry it still accepts")
	e.clock.advance(time.Nanosecond)
	_, body = e.accept(t, id, ownerCaller)
	assert.Equal(t, "registered", body["status"], "an acceptance registered before is returned as is")
}

func TestOfferCA5AnExpiredOfferIsRenewedWithTheSameCreditAndRenewalIsIdempotent(t *testing.T) {
	e := newOfferITWithDB(t)
	id := e.generate(t)["offer_id"].(string)
	_, _ = e.do(t, http.MethodGet, "/v1/offers/"+id, ownerCaller, "")
	e.clock.advance(25 * time.Hour)

	status, body := e.accept(t, id, ownerCaller)
	assert.Equal(t, http.StatusUnprocessableEntity, status)
	assert.Equal(t, "offer_expired", body["error"])
	_, read := e.do(t, http.MethodGet, "/v1/offers/"+id, ownerCaller, "")
	assert.Equal(t, "expired", read["status"])
	assert.Equal(t, true, read["can_renew"])

	status, renewed := e.do(t, http.MethodPost, "/v1/offers/"+id+"/renewal", ownerCaller, "")
	require.Equal(t, http.StatusCreated, status, "%v", renewed)
	assert.Equal(t, "active", renewed["status"])
	assert.Equal(t, read["terms"], renewed["terms"], "the same credit data")
	newID := renewed["offer_id"].(string)
	assert.NotEqual(t, id, newID)

	status, again := e.do(t, http.MethodPost, "/v1/offers/"+id+"/renewal", ownerCaller, "")
	assert.Equal(t, http.StatusOK, status)
	assert.Equal(t, newID, again["offer_id"])
	_, read = e.do(t, http.MethodGet, "/v1/offers/"+id, ownerCaller, "")
	assert.Equal(t, newID, read["renewed_by"])

	status, _ = e.accept(t, newID, ownerCaller)
	assert.Equal(t, http.StatusAccepted, status)
	status, _ = e.do(t, http.MethodPost, "/v1/offers/"+id+"/renewal", strangerCaller, "")
	assert.Equal(t, http.StatusForbidden, status)
	status, _ = e.do(t, http.MethodPost, "/v1/offers/"+id+"/renewal", "advisor:adv-1", "")
	assert.Equal(t, http.StatusForbidden, status)
}

func TestOfferRenewalWithoutAConsentIsRefused(t *testing.T) {
	e := newOfferITWithDB(t)
	id := e.generate(t)["offer_id"].(string)
	_, _ = e.do(t, http.MethodGet, "/v1/offers/"+id, ownerCaller, "")
	e.clock.advance(25 * time.Hour)
	e.audit.revoke("cust-1")
	before := e.source.calls.Load()

	status, body := e.do(t, http.MethodPost, "/v1/offers/"+id+"/renewal", ownerCaller, "")

	assert.Equal(t, http.StatusUnprocessableEntity, status)
	assert.Equal(t, "consent_required", body["error"])
	assert.Equal(t, before, e.source.calls.Load())
}

// HU-W1.2-01 CA-3 applied to the offer: no consent, no source.
func TestOfferWithoutConsentNoSourceIsQueried(t *testing.T) {
	e := newOfferITWithDB(t)
	e.audit.revoke("cust-1")

	status, body := e.do(t, http.MethodPost, "/v1/offers", "", offerBody)

	assert.Equal(t, http.StatusUnprocessableEntity, status)
	assert.Equal(t, "consent_required", body["error"])
	assert.Zero(t, e.source.calls.Load())
}

func TestOfferWithAnotherCustomersConsentIdIsAMismatch(t *testing.T) {
	e := newOfferITWithDB(t)

	status, body := e.do(t, http.MethodPost, "/v1/offers", "", strings.Replace(offerBody, `"cns-1"`, `"cns-of-someone-else"`, 1))

	assert.Equal(t, http.StatusUnprocessableEntity, status)
	assert.Equal(t, "consent_mismatch", body["error"])
	assert.Zero(t, e.source.calls.Load())
}

func TestOfferWhenAuditIsDownNoSourceIsQueried(t *testing.T) {
	e := newOfferITWithDB(t)
	e.audit.down.Store(true)

	status, body := e.do(t, http.MethodPost, "/v1/offers", "", offerBody)

	assert.Equal(t, http.StatusServiceUnavailable, status)
	assert.Equal(t, "consent_unverifiable", body["error"])
	assert.Zero(t, e.source.calls.Load())
}

// The adjusted and demo offers of other stories keep working through the same route.
func TestOfferTheAdjustedAndDemoOffersAreServedWithAReducedView(t *testing.T) {
	e := newOfferITWithDB(t)
	require.NoError(t, e.router.Create(context.Background(), domain.Offer{
		ID: "adj-acc-1", CustomerID: "sub-a", Premium: 120_000, Currency: "COP", Mode: domain.ModeNormal,
		ExpiresAt: e.clock.Now().Add(time.Hour),
	}))

	status, adj := e.do(t, http.MethodGet, "/v1/offers/adj-acc-1", ownerCaller, "")
	require.Equal(t, http.StatusOK, status)
	assert.Equal(t, "adjusted", adj["kind"])
	assert.Equal(t, 120000.0, adj["premium"])
	assert.Equal(t, "active", adj["status"])
	assert.Nil(t, adj["terms"])
	assert.Nil(t, adj["profile"])

	status, demo := e.do(t, http.MethodGet, "/v1/offers/demo-active-it1", ownerCaller, "")
	require.Equal(t, http.StatusOK, status)
	assert.Equal(t, "demo", demo["kind"])
	status, _ = e.do(t, http.MethodGet, "/v1/offers/adj-acc-1", strangerCaller, "")
	assert.Equal(t, http.StatusForbidden, status)
	status, _ = e.accept(t, "adj-acc-1", ownerCaller)
	assert.Equal(t, http.StatusAccepted, status, "the acceptance still serves them")
	status, _ = e.do(t, http.MethodGet, "/v1/offers/off-00000000000000000000000000000000", ownerCaller, "")
	assert.Equal(t, http.StatusNotFound, status)
}

// revoke leaves the customer without an active consent in the fake audit.
func (a *fakeAudit) revoke(customerID string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.consents, customerID)
}
