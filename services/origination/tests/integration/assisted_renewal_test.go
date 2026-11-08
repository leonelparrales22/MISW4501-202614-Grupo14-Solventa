// This file wires the assisted renewal (HU-W1.5-03) end to end, in process:
// the routes of the advisor and of the insured over HTTP, the real profiling
// with a controllable Open Finance and a Redis cache (miniredis), the real
// billing worker over PostgreSQL (Testcontainers) and a payment gateway that
// declines on demand. The consent verifier and the e-mail are doubles. It
// checks each acceptance criterion and the decisions of the plan.
package integration_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/adapters/email"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/adapters/openfinance"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/resilience"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/security"
	emailadapter "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/email"
	httpadapter "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/http"
	ofadapter "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/openfinance"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/postgres"
	redisadapter "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/redis"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/billing"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/profiling"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/renewal"
)

// namedVerifier reads "role:subject"; the advisor laura has the claim name,
// andres has none (as in the permanent user pool).
type namedVerifier struct{}

func (namedVerifier) Verify(_ context.Context, raw string) (security.Claims, error) {
	role, subject, ok := strings.Cut(raw, ":")
	if !ok {
		return security.Claims{}, fmt.Errorf("invalid token")
	}
	c := security.Claims{Subject: subject, Email: subject + "@example.com", Roles: []security.Role{security.Role(role)}}
	if subject == "laura" {
		c.Name = "Laura Gómez"
	}
	return c, nil
}

const (
	advLaura  = "advisor:laura"
	advAndres = "advisor:andres"
)

// switchConsents is the consent verifier of audit, switched by the test.
type switchConsents struct {
	inactive atomic.Bool
	down     atomic.Bool
	calls    atomic.Int32
}

func (s *switchConsents) Verify(context.Context, string) (domain.ConsentStatus, error) {
	s.calls.Add(1)
	if s.down.Load() {
		return domain.ConsentStatus{}, fmt.Errorf("%w: audit timeout", ports.ErrConsentUnverifiable)
	}
	if s.inactive.Load() {
		return domain.ConsentStatus{}, nil
	}
	return domain.ConsentStatus{Active: true, ConsentID: "cns-e2e"}, nil
}

// codeMailbox is the e-mail provider: it keeps the codes sent.
type codeMailbox struct {
	mu   sync.Mutex
	sent []email.Message
}

func (m *codeMailbox) Send(_ context.Context, msg email.Message) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sent = append(m.sent, msg)
	return nil
}

var codePattern = regexp.MustCompile(`\b[0-9]{6}\b`)

func (m *codeMailbox) lastCode(t *testing.T) string {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	require.NotEmpty(t, m.sent)
	code := codePattern.FindString(m.sent[len(m.sent)-1].Body)
	require.NotEmpty(t, code)
	return code
}

type assistedEnv struct {
	*renewalEnv
	api      *httptest.Server
	provider *provider
	cache    *redisadapter.ProfileCache
	consents *switchConsents
	mails    *codeMailbox
	offers   *postgres.OfferStore
	assisted *renewal.AssistedService
}

func newAssistedEnv(t *testing.T) *assistedEnv {
	t.Helper()
	e := &assistedEnv{renewalEnv: newRenewalEnv(t), provider: newProvider(t), consents: &switchConsents{}, mails: &codeMailbox{}}
	e.offers = postgres.NewOfferStore(e.db)
	mr := miniredis.RunT(t)
	rc := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rc.Close() })
	e.cache = redisadapter.NewProfileCache(rc, 0, 0)
	client := openfinance.NewClient(openfinance.Config{BaseURL: e.provider.srv.URL, Timeout: providerTimeout,
		Breaker: resilience.Config{ConsecutiveFailures: 10_000, OpenTimeout: openWindow, HalfOpenSuccesses: 1}})
	// As main builds it: the reuse of a complete cached profile is on, so a
	// requote that still queries the sources is ResolveFresh at work.
	resolver := profiling.NewResolver(ofadapter.NewSource(client), e.cache, e.clock, profiling.WithCachedProfileReuse())
	e.api = e.serve(t, resolver)
	return e
}

// serve builds the assisted renewal over the database and starts its routes,
// next to the insured's renewal and charge routes. Calling it again is a
// restart of origination.
func (e *assistedEnv) serve(t *testing.T, resolver *profiling.Resolver) *httptest.Server {
	t.Helper()
	customers := postgres.NewCustomerStore(e.db)
	svc, err := renewal.NewAssistedService(renewal.AssistedConfig{
		Renewals: e.svc, Requotes: postgres.NewRenewalRequoteStore(e.db), Assisted: postgres.NewRenewalStore(e.db),
		Origins: renewal.OriginFinder{Policies: e.policies, Charges: e.charges, Offers: e.offers, BySubject: customers,
			ByDocument: customers},
		Profiles: resolver, Consents: e.consents,
		Challenges: postgres.NewRenewalConsentChallengeStore(e.db), Sender: emailadapter.NewRenewalConsentNotifier(e.mails),
	})
	require.NoError(t, err)
	e.assisted = svc
	policy := resilience.RetryPolicy{Base: 20 * time.Millisecond, Max: 100 * time.Millisecond, Window: 24 * time.Hour}
	srv := httptest.NewServer(httpadapter.NewRouter(resolver, httpadapter.WithObservability("origination"),
		httpadapter.WithAssistedRenewals(svc, namedVerifier{}),
		httpadapter.WithPolicies(e.svc, namedVerifier{}),
		httpadapter.WithCharges(billing.NewService(e.charges, e.clock, policy, func() {}), namedVerifier{})))
	t.Cleanup(srv.Close)
	return srv
}

func (e *assistedEnv) call(t *testing.T, method, path, token, body string) (int, map[string]any) {
	t.Helper()
	return (&acceptanceEnv{srv: e.api}).do(t, method, path, token, body)
}

// seedSold stores an active policy of carolina bought from the offer, paid
// with a card (or with none), as the activation of its charge leaves it.
func (e *assistedEnv) seedSold(t *testing.T, id string, offer domain.PersonalizedOffer, endsIn time.Duration, card bool) domain.Policy {
	t.Helper()
	ctx := context.Background()
	now := e.clock.Now().UTC().Truncate(time.Microsecond)
	offer.Applicant = domain.Applicant{FullName: "Carolina Pérez", DocumentID: "1032456789", Email: "carolina@example.com"}
	offer.Terms = domain.CoverageTerms{InsuredAmount: 68_000_000, TermMonths: 12, Coverages: []domain.Coverage{{Code: domain.CoverageDeath, InsuredAmount: 68_000_000}}}
	offer.Premium, offer.Currency, offer.Mode = 142_300, "COP", domain.ModeNormal
	offer.BirthDate = time.Date(1985, 5, 1, 0, 0, 0, 0, time.UTC)
	offer.CreatedAt, offer.ExpiresAt = now.Add(-365*24*time.Hour), now.Add(-364*24*time.Hour)
	_, _, err := e.offers.Create(ctx, offer)
	require.NoError(t, err)
	p := domain.Policy{
		ID: id, AcceptanceID: "acc-" + id, CustomerID: "carolina", Holder: offer.Applicant, Premium: 142_300, Currency: "COP",
		InsuredAmount: 68_000_000, Coverages: offer.Terms.Coverages,
		CoverageStart: now.Add(endsIn).AddDate(-1, 0, 0), CoverageEnd: now.Add(endsIn),
	}
	require.NoError(t, e.policies.Create(ctx, p))
	c := domain.NewCharge(domain.Acceptance{ID: p.AcceptanceID, Channel: domain.ChannelWeb, AcceptedAt: now},
		domain.Offer{ID: offer.ID, CustomerID: "carolina", Applicant: p.Holder, Premium: p.Premium, Currency: p.Currency},
		offer.Terms, now, now.Add(time.Hour))
	_, _, err = e.charges.CreatePending(ctx, c)
	require.NoError(t, err)
	if card {
		_, err = e.charges.AttachPaymentMethod(ctx, p.AcceptanceID,
			domain.PaymentMethod{Kind: domain.PaymentCard, Token: "tok_7b3c1f9a", Brand: "visa", Last4: "1111", Installments: 1}, now)
		require.NoError(t, err)
	}
	_, err = e.db.ExecContext(ctx, `UPDATE charge_attempt SET status = 'confirmed', next_attempt_at = NULL, confirmed_at = $2,
		policy_id = $3, coverage_start = $4, coverage_end = $5, activation_published_at = $2 WHERE acceptance_id = $1`,
		p.AcceptanceID, now, p.ID, p.CoverageStart, p.CoverageEnd)
	require.NoError(t, err)
	return p
}

func embeddedOffer(id string) domain.PersonalizedOffer {
	return domain.PersonalizedOffer{ID: id, CustomerID: "partner-" + id, Channel: domain.OfferChannelEmbedded, ConsentID: "cns-1",
		PartnerID: "partner-1", ApplicationID: "app-" + id}
}

// assistedOffer is the offer the formalization of a quote stores
// (HU-W1.1-03, formalization.buildOffer).
func assistedOffer(quoteID string) domain.PersonalizedOffer {
	return domain.PersonalizedOffer{ID: domain.FormalizationOfferID(quoteID), CustomerID: "cus-" + quoteID,
		Channel: domain.OfferChannelAssisted, QuoteID: quoteID, ConsentID: domain.FormalizationID(quoteID),
		PartnerID: domain.AdvisorPartnerID, ApplicationID: quoteID}
}

func (e *assistedEnv) count(t *testing.T, table string) int {
	t.Helper()
	var n int
	require.NoError(t, e.db.QueryRowContext(context.Background(), `SELECT count(*) FROM `+table).Scan(&n))
	return n
}

func (e *assistedEnv) requote(t *testing.T, policyID, body string) map[string]any {
	t.Helper()
	code, rq := e.call(t, http.MethodPost, "/v1/policies/"+policyID+"/assisted-renewal/requotes", advLaura, body)
	require.Equal(t, http.StatusCreated, code, "%v", rq)
	return rq
}

func acceptanceBody(requoteID, method, reference, code string, at time.Time) string {
	return fmt.Sprintf(`{"requote_id":%q,"acceptance":{"method":%q,"accepted_at":%q,"reference":%q},"confirmation_code":%q}`,
		requoteID, method, at.UTC().Format(time.RFC3339), reference, code)
}

func (e *assistedEnv) waitAssisted(t *testing.T, policyID string, done func(map[string]any) bool) map[string]any {
	t.Helper()
	var last map[string]any
	require.Eventually(t, func() bool {
		code, body := e.call(t, http.MethodGet, "/v1/policies/"+policyID+"/assisted-renewal", advLaura, "")
		last = body
		return code == http.StatusOK && done(body)
	}, 15*time.Second, 20*time.Millisecond, "last view: %v", last)
	return last
}

func renewalStatus(want string) func(map[string]any) bool {
	return func(b map[string]any) bool {
		r, ok := b["renewal"].(map[string]any)
		return ok && r["status"] == want
	}
}

// CA-1: the advisor requotes a policy about to expire with the rating engine
// and sees the resulting conditions.
func TestAssistedRenewalCA1TheAdvisorRequotesWithTheEngine(t *testing.T) {
	e := newAssistedEnv(t)
	p := e.seedSold(t, "pol-a1", embeddedOffer("off-a1"), 10*24*time.Hour, true)

	code, view := e.call(t, http.MethodGet, "/v1/policies/"+p.ID+"/assisted-renewal", advLaura, "")
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, true, view["can_requote"])
	assert.Equal(t, false, view["can_formalize"], "nothing requoted yet")
	assert.Equal(t, "audit", view["consent_mode"])

	rq := e.requote(t, p.ID, `{}`)
	assert.Equal(t, true, rq["requoted"])
	assert.Equal(t, "v1", rq["rating_version"])
	assert.InDelta(t, domain.DiscountRate, rq["discount_rate"], 1e-9)
	assert.Equal(t, "normal", rq["mode"])
	assert.Len(t, rq["applied_rules"], 5, "the factors of the engine")
	assert.Equal(t, "open_finance", rq["source"], "a fresh profile from the source")
	assert.Nil(t, rq["notice"])
	current, renewed := rq["current"].(map[string]any), rq["renewal"].(map[string]any)
	assert.InDelta(t, 142_300, current["premium"], 0.001)
	assert.Equal(t, current["coverage_end"], renewed["coverage_start"])
	assert.NotContains(t, fmt.Sprint(rq), "score")

	_, view = e.call(t, http.MethodGet, "/v1/policies/"+p.ID+"/assisted-renewal", advLaura, "")
	assert.Equal(t, rq["requote_id"], view["requote"].(map[string]any)["requote_id"], "the page works on reload")
	assert.Equal(t, true, view["can_formalize"])
}

// CA-2: the acceptance (date and method) is recorded before formalizing; an
// invalid one formalizes nothing.
func TestAssistedRenewalCA2NothingIsFormalizedWithoutAValidAcceptance(t *testing.T) {
	e := newAssistedEnv(t)
	p := e.seedSold(t, "pol-a2", embeddedOffer("off-a2"), 10*24*time.Hour, true)
	id := e.requote(t, p.ID, `{}`)["requote_id"].(string)
	now := e.clock.Now()

	for name, body := range map[string]string{
		"no acceptance":  fmt.Sprintf(`{"requote_id":%q}`, id),
		"unknown method": acceptanceBody(id, "fax", "GRAB-1", "", now),
		"in the future":  acceptanceBody(id, "recorded_call", "GRAB-1", "", now.Add(time.Hour)),
		"no reference":   acceptanceBody(id, "remote_signature", "", "", now),
	} {
		code, body := e.call(t, http.MethodPost, "/v1/policies/"+p.ID+"/assisted-renewal/formalization", advLaura, body)
		assert.Equal(t, http.StatusUnprocessableEntity, code, name)
		assert.Equal(t, "validation_failed", body["error"], name)
	}
	assert.Zero(t, e.count(t, "renewal"))
	assert.Zero(t, e.count(t, "renewal_formalization"))
}

// CA-3 and CA-4: formalizing charges the requoted premium and issues the
// renewed policy through the flow of the issuance; the renewal keeps the
// advisor, the date, the acceptance and the conditions, also after a restart.
func TestAssistedRenewalCA3CA4FormalizingChargesIssuesAndRecordsTheAdvisor(t *testing.T) {
	e := newAssistedEnv(t)
	p := e.seedSold(t, "pol-a3", embeddedOffer("off-a3"), 10*24*time.Hour, true)
	rq := e.requote(t, p.ID, `{}`)
	id := rq["requote_id"].(string)

	code, body := e.call(t, http.MethodPost, "/v1/policies/"+p.ID+"/assisted-renewal/formalization", advLaura,
		acceptanceBody(id, "recorded_call", "GRAB-2026-88931", "", e.clock.Now()))
	require.Equal(t, http.StatusCreated, code, "%v", body)
	renewalBody := body["renewal"].(map[string]any)
	assert.Equal(t, "advisor", renewalBody["channel"])
	chargeID := renewalBody["charge_id"].(string)
	charge := mustCharge(t, e.renewalEnv, chargeID)
	assert.Equal(t, domain.ChannelRenewal, charge.Channel, "the monitor of HU-W1.3-04 lists it")
	assert.Equal(t, domain.ToMinorUnits(rq["renewal"].(map[string]any)["premium"].(float64)), charge.AmountMinor, "the requoted premium")
	assert.Equal(t, "1111", charge.PaymentMethod.Last4, "the registered card")

	done := e.waitAssisted(t, p.ID, renewalStatus("renewed"))
	newID := done["renewal"].(map[string]any)["new_policy_id"].(string)
	renewed, err := e.policies.Get(context.Background(), newID)
	require.NoError(t, err)
	assert.Equal(t, p.ID, renewed.RenewalOf)
	assert.True(t, renewed.CoverageStart.Equal(p.CoverageEnd), "continuous coverage")
	assert.Equal(t, "confirmed", done["charge"].(map[string]any)["status"])
	assert.Equal(t, newID, done["charge"].(map[string]any)["policy_id"])

	// A restart of origination: everything comes from PostgreSQL.
	e.api = e.serve(t, profiling.NewResolver(nil, nil, e.clock))
	code, view := e.call(t, http.MethodGet, "/v1/policies/"+p.ID+"/assisted-renewal", advLaura, "")
	require.Equal(t, http.StatusOK, code)
	f := view["formalization"].(map[string]any)
	assert.Equal(t, "laura", f["advisor_id"])
	assert.Equal(t, "Laura Gómez", f["advisor_name"])
	assert.Equal(t, "laura@example.com", f["advisor_email"])
	assert.Equal(t, id, f["requote_id"], "the requote applied")
	assert.Equal(t, "recorded_call", f["acceptance"].(map[string]any)["method"])
	assert.Equal(t, "GRAB-2026-88931", f["acceptance"].(map[string]any)["reference"])
	assert.NotEmpty(t, f["formalized_at"])
	assert.Equal(t, rq["renewal"], view["requote"].(map[string]any)["renewal"], "the conditions applied")
	assert.Equal(t, 1, e.gw.totalCharged())
}

// CA-5: with the sources down the requote uses the last cached profile and
// says so; without a recent one, the base actuarial profile.
func TestAssistedRenewalCA5DegradedRequoteUsesTheCachedProfileAndSaysSo(t *testing.T) {
	e := newAssistedEnv(t)
	p := e.seedSold(t, "pol-a5", embeddedOffer("off-a5"), 10*24*time.Hour, true)

	healthy := e.requote(t, p.ID, `{}`)
	assert.Equal(t, "normal", healthy["mode"])
	_, found, err := e.cache.Get(context.Background(), "partner-off-a5")
	require.NoError(t, err)
	require.True(t, found, "the healthy requote warms the cache")

	e.provider.set(providerDown)
	hits := e.provider.hits.Load()
	degraded := e.requote(t, p.ID, `{}`)
	assert.Greater(t, e.provider.hits.Load(), hits, "the sources are queried first (D-5 b)")
	assert.Equal(t, "provisional", degraded["mode"])
	assert.Equal(t, "cache", degraded["source"])
	assert.Equal(t, "last_profile_in_use", degraded["notice"])
	assert.NotNil(t, degraded["profile_date"])
	assert.Equal(t, "open_finance", degraded["unavailable_source"])
	assert.Equal(t, true, degraded["requoted"])

	stale := e.seedSold(t, "pol-a5b", embeddedOffer("off-a5b"), 10*24*time.Hour, true)
	old := domain.RiskProfile{CustomerID: "partner-off-a5b", Score: 0.3, Category: "standard", CalculatedAt: e.clock.Now().Add(-31 * 24 * time.Hour)}
	require.NoError(t, e.cache.Set(context.Background(), old))
	base := e.requote(t, stale.ID, `{}`)
	assert.Equal(t, "base_actuarial", base["source"])
	assert.Equal(t, "personalization_unavailable", base["notice"])

	e.provider.set(providerHealthy)
	back := e.requote(t, p.ID, `{}`)
	assert.Equal(t, "normal", back["mode"], "the sources are back")
	var notices []string
	rows, err := e.db.QueryContext(context.Background(), `SELECT profile_notice FROM renewal_requote ORDER BY created_at, requote_id`)
	require.NoError(t, err)
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var n string
		require.NoError(t, rows.Scan(&n))
		notices = append(notices, n)
	}
	assert.ElementsMatch(t, []string{"", "last_profile_in_use", "personalization_unavailable", ""}, notices, "each requote keeps its case")
}

// D-8 (b): a policy born in the embedded channel is requoted only with an
// active consent in audit, and nothing is consulted without it.
func TestAssistedRenewalD8TheConsentOfAnEmbeddedPolicyIsVerifiedFirst(t *testing.T) {
	e := newAssistedEnv(t)
	p := e.seedSold(t, "pol-d8", embeddedOffer("off-d8"), 10*24*time.Hour, true)
	hits := e.provider.hits.Load()

	e.consents.inactive.Store(true)
	code, body := e.call(t, http.MethodPost, "/v1/policies/"+p.ID+"/assisted-renewal/requotes", advLaura, `{"consent_affirmed":true}`)
	assert.Equal(t, http.StatusUnprocessableEntity, code)
	assert.Equal(t, "consent_required", body["error"])

	e.consents.inactive.Store(false)
	e.consents.down.Store(true)
	code, body = e.call(t, http.MethodPost, "/v1/policies/"+p.ID+"/assisted-renewal/requotes", advLaura, `{}`)
	assert.Equal(t, http.StatusServiceUnavailable, code)
	assert.Equal(t, "consent_unverifiable", body["error"])

	assert.Equal(t, hits, e.provider.hits.Load(), "no source was queried")
	assert.Zero(t, e.count(t, "renewal_requote"))
}

// D-13 (c): a policy born in the assisted channel (the offer of a quote
// formalized with HU-W1.1-03) is requoted on the advisor's affirmation.
func TestAssistedRenewalD13TheAdvisorAffirmsTheConsentOfAnAssistedPolicy(t *testing.T) {
	e := newAssistedEnv(t)
	p := e.seedSold(t, "pol-d13", assistedOffer("q-d13"), 10*24*time.Hour, true)
	hits := e.provider.hits.Load()

	_, view := e.call(t, http.MethodGet, "/v1/policies/"+p.ID+"/assisted-renewal", advLaura, "")
	assert.Equal(t, "advisor_affirmation", view["consent_mode"])

	code, body := e.call(t, http.MethodPost, "/v1/policies/"+p.ID+"/assisted-renewal/requotes", advLaura, `{}`)
	require.Equal(t, http.StatusUnprocessableEntity, code)
	assert.Equal(t, "validation_failed", body["error"])
	assert.Equal(t, []any{map[string]any{"field": "consent_affirmed", "code": "required"}}, body["fields"])
	assert.Zero(t, e.consents.calls.Load(), "audit is not asked")
	assert.Equal(t, hits, e.provider.hits.Load(), "no source was queried")
	assert.Zero(t, e.count(t, "renewal_requote"))

	rq := e.requote(t, p.ID, `{"consent_affirmed":true}`)
	assert.Equal(t, "advisor_affirmed", rq["consent_basis"])
	assert.NotNil(t, rq["consent_affirmed_at"])
	var by string
	require.NoError(t, e.db.QueryRowContext(context.Background(),
		`SELECT consent_affirmed_by FROM renewal_requote WHERE requote_id = $1`, rq["requote_id"]).Scan(&by))
	assert.Equal(t, "laura", by)

	code, _ = e.call(t, http.MethodPost, "/v1/policies/"+p.ID+"/assisted-renewal/formalization", advLaura,
		acceptanceBody(rq["requote_id"].(string), "recorded_call", "GRAB-1", "", e.clock.Now()))
	require.Equal(t, http.StatusCreated, code)
	e.waitAssisted(t, p.ID, renewalStatus("renewed"))
}

// D-9 (a): the confirmation code, read from the e-mail sent to the holder.
func TestAssistedRenewalD9TheCustomerConfirmsWithTheCodeSentByEmail(t *testing.T) {
	e := newAssistedEnv(t)
	p := e.seedSold(t, "pol-d9", embeddedOffer("off-d9"), 10*24*time.Hour, true)
	id := e.requote(t, p.ID, `{}`)["requote_id"].(string)

	code, sent := e.call(t, http.MethodPost, "/v1/policies/"+p.ID+"/assisted-renewal/consent", advLaura, fmt.Sprintf(`{"requote_id":%q}`, id))
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, "ca***@example.com", sent["sent_to"])
	assert.NotContains(t, fmt.Sprint(sent), e.mails.lastCode(t), "the code never comes back")

	code, body := e.call(t, http.MethodPost, "/v1/policies/"+p.ID+"/assisted-renewal/formalization", advLaura,
		acceptanceBody(id, "confirmation_code", "", "000000", e.clock.Now()))
	assert.Equal(t, http.StatusUnprocessableEntity, code)
	assert.Equal(t, "invalid_confirmation_code", body["error"])

	for i := 0; i < 2; i++ {
		code, _ = e.call(t, http.MethodPost, "/v1/policies/"+p.ID+"/assisted-renewal/consent", advLaura, fmt.Sprintf(`{"requote_id":%q}`, id))
		require.Equal(t, http.StatusOK, code)
	}
	code, body = e.call(t, http.MethodPost, "/v1/policies/"+p.ID+"/assisted-renewal/consent", advLaura, fmt.Sprintf(`{"requote_id":%q}`, id))
	assert.Equal(t, http.StatusTooManyRequests, code, "a fourth code")
	assert.Equal(t, "too_many_codes", body["error"])

	code, body = e.call(t, http.MethodPost, "/v1/policies/"+p.ID+"/assisted-renewal/formalization", advLaura,
		acceptanceBody(id, "confirmation_code", "", e.mails.lastCode(t), e.clock.Now()))
	require.Equal(t, http.StatusCreated, code, "%v", body)
	assert.Equal(t, "confirmation_code", body["formalization"].(map[string]any)["acceptance"].(map[string]any)["method"])
}

// D-10 (a): without a registered payment method the advisor cannot formalize.
func TestAssistedRenewalD10WithoutPaymentMethodTheAdvisorCannotFormalize(t *testing.T) {
	e := newAssistedEnv(t)
	p := e.seedSold(t, "pol-d10", embeddedOffer("off-d10"), 10*24*time.Hour, false)

	_, view := e.call(t, http.MethodGet, "/v1/policies/"+p.ID+"/assisted-renewal", advLaura, "")
	assert.Equal(t, "payment_method_missing", view["blocked_reason"])
	assert.Nil(t, view["payment_method"])
	id := e.requote(t, p.ID, `{}`)["requote_id"].(string)

	code, body := e.call(t, http.MethodPost, "/v1/policies/"+p.ID+"/assisted-renewal/formalization", advLaura,
		acceptanceBody(id, "recorded_call", "GRAB-1", "", e.clock.Now()))
	assert.Equal(t, http.StatusConflict, code)
	assert.Equal(t, "payment_method_missing", body["error"])
	assert.Zero(t, e.count(t, "renewal"))
}

// 01·D-3 (a+): "Ver" shows the charge of the renewal; when it failed, until
// when the insured can retry, whoever renewed.
func TestAssistedRenewalTheViewShowsAFailedPaymentAndItsRetryDeadline(t *testing.T) {
	e := newAssistedEnv(t)
	e.gw.setDeclineFirst(true)

	byAdvisor := e.seedSold(t, "pol-v1", embeddedOffer("off-v1"), 10*24*time.Hour, true)
	id := e.requote(t, byAdvisor.ID, `{}`)["requote_id"].(string)
	code, _ := e.call(t, http.MethodPost, "/v1/policies/"+byAdvisor.ID+"/assisted-renewal/formalization", advLaura,
		acceptanceBody(id, "recorded_call", "GRAB-1", "", e.clock.Now()))
	require.Equal(t, http.StatusCreated, code)
	failed := e.waitAssisted(t, byAdvisor.ID, func(b map[string]any) bool {
		c, ok := b["charge"].(map[string]any)
		return ok && c["status"] == "failed"
	})
	charge := failed["charge"].(map[string]any)
	assert.Equal(t, "payment_rejected", charge["failure_reason"])
	assert.NotNil(t, charge["last_failure_at"])
	assert.NotNil(t, failed["renewal"].(map[string]any)["retry_until"])
	assert.Equal(t, "accepted", failed["renewal"].(map[string]any)["status"], "the request is not lost")

	// The insured hands over another card from their portal: renewed.
	chargeID := failed["renewal"].(map[string]any)["charge_id"].(string)
	code, _ = e.call(t, http.MethodPost, "/v1/acceptances/"+chargeID+"/charge/payment-method", carolina,
		`{"method":"card","payment_token":"tok_other","brand":"mastercard","last4":"4444","installments":1}`)
	require.Equal(t, http.StatusAccepted, code)
	e.waitAssisted(t, byAdvisor.ID, renewalStatus("renewed"))

	byInsured := e.seedSold(t, "pol-v2", embeddedOffer("off-v2"), 10*24*time.Hour, true)
	code, _ = e.call(t, http.MethodPost, "/v1/policies/"+byInsured.ID+"/renewal", carolina, `{}`)
	require.Equal(t, http.StatusCreated, code)
	view := e.waitAssisted(t, byInsured.ID, func(b map[string]any) bool {
		c, ok := b["charge"].(map[string]any)
		return ok && c["status"] == "failed"
	})
	assert.Equal(t, "insured", view["renewal"].(map[string]any)["channel"], "read only: the insured renewed")
	assert.Nil(t, view["formalization"])
	assert.NotNil(t, view["renewal"].(map[string]any)["retry_until"])
}

// HA-S01: only the advisor; the insured and operations get 403 and nothing is
// stored.
func TestAssistedRenewalOnlyTheAdvisorRenewsOnBehalfOfTheCustomer(t *testing.T) {
	e := newAssistedEnv(t)
	p := e.seedSold(t, "pol-r", embeddedOffer("off-r"), 10*24*time.Hour, true)

	for _, token := range []string{carolina, "operations:ops", "partner:socio"} {
		code, _ := e.call(t, http.MethodGet, "/v1/policies/"+p.ID+"/assisted-renewal", token, "")
		assert.Equal(t, http.StatusForbidden, code, token)
		code, _ = e.call(t, http.MethodPost, "/v1/policies/"+p.ID+"/assisted-renewal/requotes", token, `{}`)
		assert.Equal(t, http.StatusForbidden, code, token)
	}
	assert.Zero(t, e.count(t, "renewal_requote"))
}

// One outcome per period: two advisors at once, or the advisor and the
// insured, produce one renewal and one charge.
func TestAssistedRenewalOneOutcomePerPeriodUnderConcurrency(t *testing.T) {
	e := newAssistedEnv(t)
	p := e.seedSold(t, "pol-c1", embeddedOffer("off-c1"), 10*24*time.Hour, true)
	id := e.requote(t, p.ID, `{}`)["requote_id"].(string)

	var wg sync.WaitGroup
	codes := make([]int, 2)
	for i, token := range []string{advLaura, advAndres} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			codes[i], _ = e.call(t, http.MethodPost, "/v1/policies/"+p.ID+"/assisted-renewal/formalization", token,
				acceptanceBody(id, "recorded_call", "GRAB-1", "", e.clock.Now()))
		}()
	}
	wg.Wait()
	assert.ElementsMatch(t, []int{http.StatusCreated, http.StatusConflict}, codes, "one advisor wins, the other gets already_formalized")
	assert.Equal(t, 1, e.count(t, "renewal"))
	assert.Equal(t, 1, e.count(t, "renewal_formalization"))

	q := e.seedSold(t, "pol-c2", embeddedOffer("off-c2"), 10*24*time.Hour, true)
	qid := e.requote(t, q.ID, `{}`)["requote_id"].(string)
	var advisorCode, insuredCode int
	wg.Add(2)
	go func() {
		defer wg.Done()
		advisorCode, _ = e.call(t, http.MethodPost, "/v1/policies/"+q.ID+"/assisted-renewal/formalization", advAndres,
			acceptanceBody(qid, "recorded_call", "GRAB-2", "", e.clock.Now()))
	}()
	go func() {
		defer wg.Done()
		insuredCode, _ = e.call(t, http.MethodPost, "/v1/policies/"+q.ID+"/renewal", carolina, `{}`)
	}()
	wg.Wait()
	assert.Contains(t, []int{http.StatusCreated, http.StatusConflict}, advisorCode)
	assert.Contains(t, []int{http.StatusCreated, http.StatusOK, http.StatusConflict}, insuredCode)
	var renewals, charges int
	require.NoError(t, e.db.QueryRowContext(context.Background(), `SELECT count(*) FROM renewal WHERE policy_id = $1`, q.ID).Scan(&renewals))
	require.NoError(t, e.db.QueryRowContext(context.Background(), `SELECT count(*) FROM charge_attempt WHERE renewal_of = $1`, q.ID).Scan(&charges))
	assert.Equal(t, 1, renewals, "one renewal for the period")
	assert.Equal(t, 1, charges, "one charge")

	// Without the claim name, the screen shows the e-mail (andres has none).
	r := e.seedSold(t, "pol-c3", embeddedOffer("off-c3"), 10*24*time.Hour, true)
	rid := e.requote(t, r.ID, `{}`)["requote_id"].(string)
	code, body := e.call(t, http.MethodPost, "/v1/policies/"+r.ID+"/assisted-renewal/formalization", advAndres,
		acceptanceBody(rid, "remote_signature", "FIRMA-1", "", e.clock.Now()))
	require.Equal(t, http.StatusCreated, code)
	assert.Nil(t, body["formalization"].(map[string]any)["advisor_name"])
	assert.Equal(t, "andres@example.com", body["formalization"].(map[string]any)["advisor_email"])
}
