// This file checks the fix C5 end to end, over HTTP and a real PostgreSQL
// (Testcontainers): the advisor inbox of HU-W1.1-02, its decisions and the
// acceptances of HU-W1.3-01 survive a restart of origination, the claim of a
// decision holds across two instances over the same database, and the advisor
// is never the owner nor the applicant of an offer, an acceptance or a charge
// (decision #75). An instance is wired as main does it with DATABASE_URL: the
// router with observability, the test verifier, the acceptance processor, the
// review use case and its notice worker over the e-mail client, the charge
// starter over postgres.ChargeStore (without a payment gateway) and the
// PostgreSQL stores. A restart cancels the instance, closes its server and its
// pool, and builds another one over the same database with a new OfferStub:
// the demo offers (demo-*) live in memory (#77 B).
package integration_test

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
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

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/adapters/email"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/resilience"
	emailadapter "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/email"
	httpadapter "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/http"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/memory"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/postgres"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/billing"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/issuance"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/orchestrator"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/profiling"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/rating"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/underwriting"
)

// newC5Database creates an empty database in the package container and
// returns its URL, so several instances can open their own pool on it.
func newC5Database(t *testing.T) string {
	t.Helper()
	testcontainers.SkipIfProviderIsNotHealthy(t)
	ctx := context.Background()
	pgOnce.Do(func() {
		pgContainer, pgErr = tcpostgres.Run(ctx, "postgres:16-alpine",
			tcpostgres.WithDatabase("origination"), tcpostgres.WithUsername("origination"), tcpostgres.WithPassword("origination"),
			testcontainers.WithWaitStrategy(wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).WithStartupTimeout(2*time.Minute)))
		if pgErr == nil {
			pgAdminURL, pgErr = pgContainer.ConnectionString(ctx, "sslmode=disable")
		}
	})
	require.NoError(t, pgErr, "start PostgreSQL")
	admin, err := postgres.Open(ctx, pgAdminURL)
	require.NoError(t, err)
	defer func() { _ = admin.Close() }()
	b := make([]byte, 8)
	_, err = rand.Read(b)
	require.NoError(t, err)
	name := "c5_" + hex.EncodeToString(b)
	_, err = admin.ExecContext(ctx, "CREATE DATABASE "+name)
	require.NoError(t, err)
	return strings.Replace(pgAdminURL, "/origination?", "/"+name+"?", 1)
}

// switchableStarter is the charge starter of HA-D02 that fails while down is
// set, to leave an acceptance registered (HU-W1.3-01 CA-5).
type switchableStarter struct {
	next  *billing.Starter
	down  atomic.Bool
	calls atomic.Int32
}

func (s *switchableStarter) Start(ctx context.Context, a domain.Acceptance, o domain.Offer) error {
	s.calls.Add(1)
	if s.down.Load() {
		return errors.New("the charge cannot be started")
	}
	return s.next.Start(ctx, a, o)
}

type c5Options struct {
	// processor runs the acceptance processor; an instance without it only
	// serves the inbox (the instance B of CF-4).
	processor bool
	// noticeWorker runs the worker that sends the customer notices.
	noticeWorker bool
	// starterDown makes the charge start fail.
	starterDown bool
}

// c5Instance is one origination task over the shared database.
type c5Instance struct {
	*acceptanceEnv
	db      *sql.DB
	starter *switchableStarter
	stop    func()
}

func startC5(t *testing.T, url string, mail *c5Mailbox, o c5Options) *c5Instance {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	db, err := postgres.Open(ctx, url)
	require.NoError(t, err)
	require.NoError(t, postgres.Migrate(ctx, db))
	clock := wallClock{}

	personal := postgres.NewOfferStore(db)
	router := memory.NewOfferRouterWithAdjusted(memory.NewOfferStub(clock), personal, postgres.NewAdjustedOfferStore(db))
	acceptances := postgres.NewAcceptanceStore(db)
	reviews := postgres.NewReviewStore(db)
	charges := postgres.NewChargeStore(db)
	policy := resilience.RetryPolicy{Base: 20 * time.Millisecond, Max: 100 * time.Millisecond, Window: 24 * time.Hour}
	starter := &switchableStarter{next: billing.NewStarter(charges, billing.OfferTerms(fixtureCoverageTerms()), clock, policy, nil)}
	starter.down.Store(o.starterDown)

	underwriter := underwriting.NewHumanAdjustedUnderwriter(reviews,
		memory.NewUnderwriterStub(clock, 0, memory.DefaultReviewScoreThreshold), clock)
	processor := issuance.NewProcessor(router, acceptances, underwriter, reviews, starter, clock,
		issuance.ProcessorConfig{PollInterval: 20 * time.Millisecond, RetryBase: 30 * time.Millisecond, RetryMax: 60 * time.Millisecond})
	if o.processor {
		go processor.Run(ctx)
	}
	client := email.NewClient(email.Config{BaseURL: mail.url, Breaker: resilience.DefaultConfig()})
	worker := underwriting.NewReviewNotificationWorker(reviews, emailadapter.NewReviewNotifier(client), clock,
		underwriting.NotificationConfig{PollInterval: 20 * time.Millisecond, RetryBase: 20 * time.Millisecond, RetryMax: 40 * time.Millisecond})
	if o.noticeWorker {
		go worker.Run(ctx)
	}
	reviewSvc := underwriting.NewReviewService(underwriting.ReviewDeps{
		Reviews: reviews, Acceptances: acceptances, OfferWriter: router, Clock: clock,
		AcceptanceResumed: processor.Notify, NotificationScheduled: worker.Notify,
	})
	resolver := profiling.NewResolver(nil, nil, clock)
	offerSvc := orchestrator.NewOfferService(orchestrator.OfferDeps{
		Store: personal, Offers: router, Profiles: resolver, Rater: rating.New(), Clock: clock,
	})
	srv := httptest.NewServer(httpadapter.NewRouter(resolver,
		httpadapter.WithObservability("origination"),
		httpadapter.WithAcceptances(issuance.NewAcceptanceService(router, acceptances, clock, processor.Notify), subjectVerifier{}),
		httpadapter.WithReviews(reviewSvc, subjectVerifier{}),
		httpadapter.WithOffers(offerSvc, subjectVerifier{})))

	var once sync.Once
	stop := func() {
		once.Do(func() {
			cancel()
			srv.Close()
			_ = db.Close()
		})
	}
	t.Cleanup(stop)
	return &c5Instance{acceptanceEnv: &acceptanceEnv{srv: srv}, db: db, starter: starter, stop: stop}
}

// c5Mailbox is the e-mail provider of the notices, shared by the instances.
type c5Mailbox struct {
	*mailbox
	url string
}

func newC5Mailbox(t *testing.T) *c5Mailbox {
	t.Helper()
	m := &mailbox{seen: map[string]bool{}}
	srv := httptest.NewServer(m)
	t.Cleanup(srv.Close)
	return &c5Mailbox{mailbox: m, url: srv.URL}
}

// routedC5 accepts the offer as the insured and waits until it is in review.
func (e *c5Instance) routed(t *testing.T, offerID, token string) string {
	t.Helper()
	status, body := e.accept(t, offerID, token)
	require.Equal(t, http.StatusAccepted, status, "%v", body)
	id := body["acceptance_id"].(string)
	e.waitStatus(t, id, token, "in_review")
	return id
}

func (e *c5Instance) decide(t *testing.T, id, body string) (int, map[string]any) {
	t.Helper()
	return e.do(t, http.MethodPost, "/v1/review-requests/"+id+"/decision", advisorCaller, body)
}

func (e *c5Instance) inbox(t *testing.T, query string) map[string]any {
	t.Helper()
	status, body := e.do(t, http.MethodGet, "/v1/review-requests"+query, advisorCaller, "")
	require.Equal(t, http.StatusOK, status, "%v", body)
	return body
}

// storePersonalized stores a personalized offer (HU-W1.2-03) claimed by owner.
func storePersonalized(t *testing.T, db *sql.DB, o domain.PersonalizedOffer, owner string) {
	t.Helper()
	store := postgres.NewOfferStore(db)
	_, _, err := store.Create(context.Background(), o)
	require.NoError(t, err)
	_, err = store.Claim(context.Background(), o.ID, owner)
	require.NoError(t, err)
}

func c5Terms() domain.CoverageTerms {
	return domain.CoverageTerms{InsuredAmount: 180_000_000, TermMonths: 60, Coverages: []domain.Coverage{
		{Code: domain.CoverageDeath, InsuredAmount: 180_000_000},
		{Code: domain.CoverageDisability, InsuredAmount: 90_000_000},
	}}
}

// CF-1 / B-CA-1 / B-CA-4 / B-CA-5: the pending and the decided cases, with the
// whole record of the decision, survive a restart; a pending demo case opens
// with the mode and the validity of its offer although the demo offer is gone.
func TestC5PendingAndDecidedCasesSurviveARestart(t *testing.T) {
	url := newC5Database(t)
	mail := newC5Mailbox(t)
	a := startC5(t, url, mail, c5Options{processor: true})
	ids := []string{
		a.routed(t, "demo-review-c5r1", customer),
		a.routed(t, "demo-highrisk-c5r2", customer),
		a.routed(t, "demo-review-c5r3", customer),
	}
	status, decided := a.decide(t, ids[0], `{"type":"adjust","observations":"Ingreso no verificado","premium_adjustment_pct":10}`)
	require.Equal(t, http.StatusCreated, status, "%v", decided)
	before := decided["decision"].(map[string]any)

	a.stop()
	b := startC5(t, url, mail, c5Options{processor: true})

	pending := b.inbox(t, "")
	assert.Equal(t, map[string]any{"pending": 2.0, "decided": 1.0}, pending["counts"])
	got := items(pending)
	require.Len(t, got, 2)
	assert.Equal(t, []any{ids[1], ids[2]}, []any{got[0]["acceptance_id"], got[1]["acceptance_id"]}, "by arrival")
	assert.Equal(t, "pending", got[0]["status"])
	assert.Equal(t, map[string]any{"full_name": "cust-1@example.com", "document_id": "demo-document", "email": "cust-1@example.com"}, got[0]["applicant"])

	desc := items(b.inbox(t, "?order=desc"))
	assert.Equal(t, ids[2], desc[0]["acceptance_id"])

	done := items(b.inbox(t, "?status=decided"))
	require.Len(t, done, 1)
	assert.Equal(t, ids[0], done[0]["acceptance_id"])
	assert.Equal(t, "decided", done[0]["status"])
	after := done[0]["decision"].(map[string]any)
	assert.Equal(t, before["decision_id"], after["decision_id"])
	assert.Equal(t, map[string]any{"id": "adv-1", "email": "adv-1@example.com"}, after["advisor"])
	assert.Equal(t, before["decided_at"], after["decided_at"])
	assert.Equal(t, "adjust", after["type"])
	assert.Equal(t, "Ingreso no verificado", after["observations"])
	assert.InDelta(t, 10.0, after["premium_adjustment_pct"].(float64), 1e-9)
	assert.Equal(t, before["adjusted_offer"], after["adjusted_offer"])

	status, detail := b.do(t, http.MethodGet, "/v1/review-requests/"+ids[1], advisorCaller, "")
	require.Equal(t, http.StatusOK, status)
	offer := detail["offer"].(map[string]any)
	assert.Equal(t, "normal", offer["mode"], "from the snapshot, the demo offer is gone")
	assert.NotNil(t, offer["expires_at"])

	// The pending demo cases are decided after the restart (D-7 a).
	status, body := b.decide(t, ids[2], `{"type":"reject","observations":"Fuera de política"}`)
	require.Equal(t, http.StatusCreated, status, "%v", body)
	b.waitStatus(t, ids[2], customer, "rejected")
}

// CF-1: a notice still pending when the platform stops is sent once after the
// restart.
func TestC5ANoticePendingAtTheRestartIsSentOnceAfterIt(t *testing.T) {
	url := newC5Database(t)
	mail := newC5Mailbox(t)
	a := startC5(t, url, mail, c5Options{processor: true}) // its notice worker never runs
	id := a.routed(t, "demo-highrisk-c5n", customer)
	status, body := a.decide(t, id, `{"type":"reject","observations":"Fuera de política"}`)
	require.Equal(t, http.StatusCreated, status, "%v", body)
	assert.Equal(t, "pending", body["decision"].(map[string]any)["notification"].(map[string]any)["status"])
	assert.Empty(t, mail.received())

	a.stop()
	b := startC5(t, url, mail, c5Options{processor: true, noticeWorker: true})

	require.Eventually(t, func() bool { return len(mail.received()) == 1 }, 5*time.Second, 20*time.Millisecond)
	require.Eventually(t, func() bool {
		_, d := b.do(t, http.MethodGet, "/v1/review-requests/"+id, advisorCaller, "")
		n, _ := d["decision"].(map[string]any)["notification"].(map[string]any)
		return n != nil && n["status"] == "sent"
	}, 5*time.Second, 20*time.Millisecond)
	assert.Equal(t, "cust-1@example.com", mail.received()[0].To)
	assert.Equal(t, "review-outcome:"+id, mail.received()[0].Key)
	time.Sleep(200 * time.Millisecond)
	assert.Len(t, mail.received(), 1, "one e-mail per notice")
}

// HU-W1.3-01 CA-5 / CF-2: an acceptance whose next step failed stays
// registered; after a restart of the platform it resumes from the same
// acceptance, and accepting again returns it.
func TestHUW1301CA5AnAcceptanceSurvivesAPlatformRestartAndResumes(t *testing.T) {
	url := newC5Database(t)
	mail := newC5Mailbox(t)
	a := startC5(t, url, mail, c5Options{processor: true, starterDown: true})
	storePersonalized(t, a.db, personalizedOffer("off-c5-ca5", c5Terms()), "cust-1")

	status, body := a.accept(t, "off-c5-ca5", customer)
	require.Equal(t, http.StatusAccepted, status, "%v", body)
	id := body["acceptance_id"].(string)
	require.Eventually(t, func() bool {
		_, got := a.do(t, http.MethodGet, "/v1/acceptances/"+id, customer, "")
		p, _ := got["processing"].(map[string]any)
		return got["status"] == "registered" && p != nil && p["attempts"].(float64) >= 1
	}, 5*time.Second, 20*time.Millisecond, "the failed step keeps it registered")

	a.stop()
	b := startC5(t, url, mail, c5Options{processor: true})

	got := b.waitStatus(t, id, customer, "issuance_pending")
	assert.Equal(t, id, got["acceptance_id"])
	assert.Equal(t, "approved", got["underwriting"].(map[string]any)["outcome"])
	status, again := b.accept(t, "off-c5-ca5", customer)
	assert.Equal(t, http.StatusOK, status, "accepting again returns the stored acceptance")
	assert.Equal(t, id, again["acceptance_id"])
	var charges int
	require.NoError(t, b.db.QueryRow(`SELECT count(*) FROM charge_attempt WHERE acceptance_id = $1`, id).Scan(&charges))
	assert.Equal(t, 1, charges, "the issuance started once, from the same acceptance")
}

// CF-3 / B-CA-3: an adjusted acceptance and its adjusted offer survive a
// restart, and the customer accepts the new offer by the same flow without it
// going back to the inbox.
func TestC5AnAdjustedAcceptanceAndItsOfferSurviveARestart(t *testing.T) {
	url := newC5Database(t)
	mail := newC5Mailbox(t)
	a := startC5(t, url, mail, c5Options{processor: true})
	id := a.routed(t, "demo-review-c5adj", customer)
	status, body := a.decide(t, id, `{"type":"adjust","observations":"Recargo","premium_adjustment_pct":10}`)
	require.Equal(t, http.StatusCreated, status, "%v", body)
	adjID := body["decision"].(map[string]any)["adjusted_offer"].(map[string]any)["offer_id"].(string)

	a.stop()
	b := startC5(t, url, mail, c5Options{processor: true})

	got := b.waitStatus(t, id, customer, "adjusted")
	review := got["review"].(map[string]any)
	assert.Equal(t, "adjust", review["outcome"])
	assert.Equal(t, adjID, review["adjusted_offer"].(map[string]any)["offer_id"])
	assert.InDelta(t, 98_450, review["adjusted_offer"].(map[string]any)["premium"].(float64), 1e-9)

	status, offer := b.do(t, http.MethodGet, "/v1/offers/"+adjID, customer, "")
	require.Equal(t, http.StatusOK, status, "%v", offer)

	status, accepted := b.accept(t, adjID, customer)
	require.Equal(t, http.StatusAccepted, status, "%v", accepted)
	b.waitStatus(t, accepted["acceptance_id"].(string), customer, "issuance_pending")
	assert.Empty(t, items(b.inbox(t, "")), "the adjusted offer does not go back to the inbox")
}

// CF-4 / B-CA-4: two instances over the same database. Only A runs the
// processor and creates the cases; B only serves the inbox. Adjusts with
// different surcharges, split between the two, race for each of twenty cases:
// one 201, the rest 409, one row in human_decision, one adjusted offer, and its
// premium is the one of the decision and of the acceptance. There is no accept
// decision (the acceptance writes are not safe across tasks, decision D-3 c).
func TestC5TwoInstancesDecideTheSameCaseOnce(t *testing.T) {
	url := newC5Database(t)
	mail := newC5Mailbox(t)
	a := startC5(t, url, mail, c5Options{processor: true})
	b := startC5(t, url, mail, c5Options{})
	const cases, racers = 20, 6
	ids := make([]string, cases)
	for i := range ids {
		ids[i] = a.routed(t, fmt.Sprintf("demo-review-c5two%02d", i), customer)
	}

	for i, id := range ids {
		start := make(chan struct{})
		codes := make([]int, racers)
		var wg sync.WaitGroup
		for r := 0; r < racers; r++ {
			inst := a
			if r%2 == 1 {
				inst = b
			}
			wg.Add(1)
			go func(r int, inst *c5Instance) {
				defer wg.Done()
				<-start
				codes[r], _ = inst.decide(t, id, fmt.Sprintf(`{"type":"adjust","observations":"Recargo %d","premium_adjustment_pct":%d}`, r, 5*(r+1)))
			}(r, inst)
		}
		close(start)
		wg.Wait()

		created := 0
		for _, c := range codes {
			if c == http.StatusCreated {
				created++
			} else {
				require.Equal(t, http.StatusConflict, c, "case %d: %v", i, codes)
			}
		}
		require.Equal(t, 1, created, "case %d: %v", i, codes)

		var decisions, offers int
		var decisionPremium, offerPremium int64
		require.NoError(t, a.db.QueryRow(`SELECT count(*), max(adjusted_premium_minor) FROM human_decision WHERE acceptance_id = $1`, id).
			Scan(&decisions, &decisionPremium))
		require.NoError(t, a.db.QueryRow(`SELECT count(*), max(premium_minor) FROM adjusted_offer WHERE acceptance_id = $1`, id).
			Scan(&offers, &offerPremium))
		require.Equal(t, 1, decisions, "case %d", i)
		require.Equal(t, 1, offers, "case %d", i)
		require.Equal(t, decisionPremium, offerPremium, "case %d: the offer has the winner's terms", i)
		var raw []byte
		require.NoError(t, a.db.QueryRow(`SELECT review_adjusted_offer FROM acceptance WHERE acceptance_id = $1`, id).Scan(&raw))
		var review struct {
			PremiumMinor int64 `json:"premium_minor"`
		}
		require.NoError(t, json.Unmarshal(raw, &review), "case %d", i)
		require.Equal(t, decisionPremium, review.PremiumMinor, "case %d: the acceptance shows the winner's terms", i)
	}
}

// customerOf reads the customer id and the applicant of a row.
func customerOf(t *testing.T, db *sql.DB, query, id string) (customerID string, applicant map[string]any) {
	t.Helper()
	var raw []byte
	require.NoError(t, db.QueryRow(query, id).Scan(&customerID, &raw))
	require.NoError(t, json.Unmarshal(raw, &applicant))
	return customerID, applicant
}

// CF-10 (decision #75) and CF-11 (D-5 a): after a restart, an advisor opens and
// adjusts a demo case and a personalized case. The adjusted offers, their
// acceptances and their charges belong to the customer, with the customer's
// name, document and e-mail; the advisor only appears as decided_by. The
// customer sees and accepts the offer; the advisor cannot accept it. The
// charge of the adjusted personalized offer activates its own terms.
func TestC5TheAdvisorIsNeverTheOwnerAfterARestart(t *testing.T) {
	url := newC5Database(t)
	mail := newC5Mailbox(t)
	a := startC5(t, url, mail, c5Options{processor: true})
	risky := personalizedOffer("off-c5-owner", c5Terms())
	risky.Profile.Score = 0.9 // high risk: routed to the advisor
	storePersonalized(t, a.db, risky, "cust-1")
	demoID := a.routed(t, "demo-highrisk-c5owner", customer)
	personalID := a.routed(t, "off-c5-owner", customer)

	a.stop()
	b := startC5(t, url, mail, c5Options{processor: true})

	adjusted := map[string]string{}
	for _, id := range []string{demoID, personalID} {
		status, detail := b.do(t, http.MethodGet, "/v1/review-requests/"+id, advisorCaller, "")
		require.Equal(t, http.StatusOK, status, "%v", detail)
		status, body := b.decide(t, id, `{"type":"adjust","observations":"Recargo por riesgo","premium_adjustment_pct":10}`)
		require.Equal(t, http.StatusCreated, status, "%v", body)
		adjusted[id] = body["decision"].(map[string]any)["adjusted_offer"].(map[string]any)["offer_id"].(string)
	}

	wantApplicant := map[string]map[string]any{
		demoID:     {"full_name": "cust-1@example.com", "document_id": "demo-document", "email": "cust-1@example.com"},
		personalID: {"full_name": "Ana Perez", "document_id": "1020304050", "email": "ana@example.com"},
	}
	for id, adjID := range adjusted {
		cust, applicant := customerOf(t, b.db, `SELECT customer_id, applicant FROM adjusted_offer WHERE offer_id = $1`, adjID)
		assert.Equal(t, "cust-1", cust, adjID)
		assert.Equal(t, wantApplicant[id], applicant, adjID)
		var accCustomer, decidedBy string
		require.NoError(t, b.db.QueryRow(`SELECT customer_id FROM acceptance WHERE acceptance_id = $1`, id).Scan(&accCustomer))
		assert.Equal(t, "cust-1", accCustomer)
		require.NoError(t, b.db.QueryRow(`SELECT decided_by FROM human_decision WHERE acceptance_id = $1`, id).Scan(&decidedBy))
		assert.Equal(t, "adv-1", decidedBy, "the advisor is only decided_by")

		status, offer := b.do(t, http.MethodGet, "/v1/offers/"+adjID, customer, "")
		require.Equal(t, http.StatusOK, status, "the customer sees the offer: %v", offer)
		status, refused := b.accept(t, adjID, advisorCaller)
		assert.Equal(t, http.StatusForbidden, status, "the advisor cannot accept it: %v", refused)
		status, accepted := b.accept(t, adjID, customer)
		require.Equal(t, http.StatusAccepted, status, "%v", accepted)
		newID := accepted["acceptance_id"].(string)
		b.waitStatus(t, newID, customer, "issuance_pending")

		var chargeCustomer, holderName, holderDocument, holderEmail string
		require.NoError(t, b.db.QueryRow(`SELECT customer_id, holder_full_name, holder_document_id, holder_email
			FROM charge_attempt WHERE acceptance_id = $1`, newID).Scan(&chargeCustomer, &holderName, &holderDocument, &holderEmail))
		assert.Equal(t, "cust-1", chargeCustomer)
		assert.Equal(t, wantApplicant[id], map[string]any{"full_name": holderName, "document_id": holderDocument, "email": holderEmail})
	}

	var count int
	require.NoError(t, b.db.QueryRow(`SELECT count(*) FROM human_decision WHERE decided_by = 'adv-1'`).Scan(&count))
	assert.Equal(t, 2, count, "decided_by counts the cases the advisor resolved")
	var owned int
	require.NoError(t, b.db.QueryRow(`SELECT (SELECT count(*) FROM adjusted_offer WHERE customer_id = 'adv-1')
		+ (SELECT count(*) FROM acceptance WHERE customer_id = 'adv-1')
		+ (SELECT count(*) FROM charge_attempt WHERE customer_id = 'adv-1')`).Scan(&owned))
	assert.Zero(t, owned, "the advisor owns nothing")

	// CF-11: the adjusted personalized offer is charged with its own terms.
	var insured int64
	var term int
	var coverages []byte
	require.NoError(t, b.db.QueryRow(`SELECT c.insured_amount_minor, c.term_months, c.coverages FROM charge_attempt c
		WHERE c.offer_id = $1`, adjusted[personalID]).Scan(&insured, &term, &coverages))
	assert.Equal(t, domain.ToMinorUnits(180_000_000), insured)
	assert.Equal(t, 60, term)
	assert.JSONEq(t, `[{"code":"`+domain.CoverageDeath+`","insured_amount_minor":18000000000},{"code":"`+domain.CoverageDisability+`","insured_amount_minor":9000000000}]`, string(coverages))
	require.NoError(t, b.db.QueryRow(`SELECT c.term_months FROM charge_attempt c WHERE c.offer_id = $1`, adjusted[demoID]).Scan(&term))
	assert.Equal(t, fixtureTermMonths, term, "the adjusted demo offer keeps the demonstration terms")
}
