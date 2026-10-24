package postgres_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/postgres"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
)

var routedAt = time.Date(2026, 10, 23, 15, 0, 0, 987_654_321, time.UTC)

type inboxEnv struct {
	db          *sql.DB
	acceptances *postgres.AcceptanceStore
	reviews     *postgres.ReviewStore
}

func newInbox(t *testing.T) *inboxEnv {
	t.Helper()
	requireOfferDocker(t)
	db := newDB(t)
	return &inboxEnv{db: db, acceptances: postgres.NewAcceptanceStore(db), reviews: postgres.NewReviewStore(db)}
}

// caseTerms are the coverage terms of a personalized offer.
func caseTerms() domain.CoverageTerms {
	return domain.CoverageTerms{InsuredAmount: 180_000_000, TermMonths: 60, Coverages: []domain.Coverage{
		{Code: domain.CoverageDeath, InsuredAmount: 180_000_000},
		{Code: domain.CoverageDisability, InsuredAmount: 90_000_000},
	}}
}

// routedRequest is a request of the customer cust-<id> with the snapshot of
// its routed offer (decision D-7 a).
func routedRequest(id, reason string, at time.Time) domain.ReviewRequest {
	return domain.ReviewRequest{
		AcceptanceID: id, OfferID: "off-" + id, CustomerID: "cust-" + id,
		Applicant: domain.Applicant{FullName: "Ana María Pérez", DocumentID: "1020304050", Email: "ana@example.com"},
		Premium:   137_600.5, Currency: "COP", Reason: reason, ReceivedAt: at,
		OfferMode: domain.ModeProvisional, ProfileScore: 0.875, OfferExpiresAt: at.Add(24 * time.Hour), Terms: caseTerms(),
	}
}

// route stores the acceptance in review and its case, as the processor does.
func (e *inboxEnv) route(t *testing.T, req domain.ReviewRequest) {
	t.Helper()
	ctx := context.Background()
	_, _, err := e.acceptances.Create(ctx, domain.Acceptance{
		ID: req.AcceptanceID, OfferID: req.OfferID, CustomerID: req.CustomerID, Channel: domain.ChannelWeb,
		AcceptedAt: req.ReceivedAt, Status: domain.AcceptanceInReview,
		Decision: &domain.UnderwritingDecision{Outcome: domain.UnderwritingHumanReview, Reason: req.Reason, DecidedAt: req.ReceivedAt},
	})
	require.NoError(t, err)
	require.NoError(t, e.reviews.Enqueue(ctx, req))
}

// inbox3 routes a-2 (high risk), a-1 and a-3 (provisional), in that order.
func inbox3(t *testing.T) *inboxEnv {
	t.Helper()
	e := newInbox(t)
	e.route(t, routedRequest("a-2", domain.ReviewReasonHighRiskScore, routedAt.Add(2*time.Minute)))
	e.route(t, routedRequest("a-1", domain.ReviewReasonProvisionalOffer, routedAt))
	e.route(t, routedRequest("a-3", domain.ReviewReasonProvisionalOffer, routedAt.Add(time.Minute)))
	return e
}

func humanDecision(id string, typ domain.DecisionType) domain.HumanDecision {
	d := domain.HumanDecision{
		ID: "dec-" + id, AcceptanceID: id, Type: typ, Advisor: domain.Advisor{ID: "adv-1", Email: "adv@example.com"},
		DecidedAt: routedAt.Add(time.Hour), Observations: "Revisado con el soporte de ingresos",
	}
	if typ == domain.DecisionAdjust {
		p := 12.5
		d.PremiumAdjustmentPct = &p
		d.AdjustedOffer = &domain.AdjustedOffer{ID: "adj-" + id, Premium: 154_800.56, Currency: "COP", ExpiresAt: routedAt.Add(25 * time.Hour)}
	}
	return d
}

func caseIDs(cases []domain.ReviewCase) []string {
	out := make([]string, 0, len(cases))
	for _, c := range cases {
		out = append(out, c.Request.AcceptanceID)
	}
	return out
}

func micro(t time.Time) time.Time { return t.Truncate(time.Microsecond) }

func requestInMicros(r domain.ReviewRequest) domain.ReviewRequest {
	r.ReceivedAt, r.OfferExpiresAt = micro(r.ReceivedAt), micro(r.OfferExpiresAt)
	return r
}

func decisionInMicros(d domain.HumanDecision) domain.HumanDecision {
	d.DecidedAt = micro(d.DecidedAt)
	if d.AdjustedOffer != nil {
		o := *d.AdjustedOffer
		o.ExpiresAt = micro(o.ExpiresAt)
		d.AdjustedOffer = &o
	}
	return d
}

// CF-1 / B-CA-1: the case round-trips with the data of the applicant, the offer
// and its snapshot, the reason and the arrival, pending.
func TestReviewStoreRoundTripsTheCaseWithItsSnapshot(t *testing.T) {
	e := inbox3(t)
	ctx := context.Background()

	c, err := e.reviews.Get(ctx, "a-1")
	require.NoError(t, err)
	assert.Equal(t, domain.ReviewPending, c.Status)
	assert.Equal(t, requestInMicros(routedRequest("a-1", domain.ReviewReasonProvisionalOffer, routedAt)), c.Request)
	assert.Nil(t, c.Decision)
	assert.Nil(t, c.Notification)

	// A demo offer carries no terms: the snapshot keeps none.
	demo := routedRequest("a-4", domain.ReviewReasonHighRiskScore, routedAt)
	demo.OfferMode, demo.Terms = domain.ModeNormal, domain.CoverageTerms{}
	e.route(t, demo)
	c, err = e.reviews.Get(ctx, "a-4")
	require.NoError(t, err)
	assert.Equal(t, requestInMicros(demo), c.Request)

	var terms sql.NullString
	require.NoError(t, e.db.QueryRowContext(ctx, `SELECT offer_terms::text FROM review_request WHERE acceptance_id = 'a-4'`).Scan(&terms))
	assert.False(t, terms.Valid, "NULL when the offer carries no terms")
}

func TestReviewStoreEnqueueIsIdempotent(t *testing.T) {
	e := inbox3(t)
	ctx := context.Background()
	again := routedRequest("a-1", domain.ReviewReasonHighRiskScore, routedAt.Add(time.Hour))
	require.NoError(t, e.reviews.Enqueue(ctx, again))

	c, err := e.reviews.Get(ctx, "a-1")
	require.NoError(t, err)
	assert.Equal(t, domain.ReviewReasonProvisionalOffer, c.Request.Reason, "the first request is kept")
	assert.Equal(t, micro(routedAt), c.Request.ReceivedAt)
}

// CF-10 and the orphans of #74: a case needs its acceptance, with the same customer.
func TestReviewStoreRefusesACaseWithoutItsAcceptanceOrWithAnotherCustomer(t *testing.T) {
	e := inbox3(t)
	ctx := context.Background()

	orphan := routedRequest("ghost", domain.ReviewReasonProvisionalOffer, routedAt)
	assert.Error(t, e.reviews.Enqueue(ctx, orphan), "no case without its acceptance")

	_, _, err := e.acceptances.Create(ctx, domain.Acceptance{
		ID: "a-9", OfferID: "off-a-9", CustomerID: "cust-a-9", Channel: domain.ChannelWeb, AcceptedAt: routedAt,
		Status: domain.AcceptanceInReview,
	})
	require.NoError(t, err)
	other := routedRequest("a-9", domain.ReviewReasonProvisionalOffer, routedAt)
	other.CustomerID = "adv-1"
	assert.Error(t, e.reviews.Enqueue(ctx, other), "the case has the customer of its acceptance, never another one")
	_, err = e.reviews.Get(ctx, "a-9")
	assert.ErrorIs(t, err, ports.ErrReviewRequestNotFound)
}

// CF-1 / B-CA-5: filter by status and reason, order by arrival, ties by id;
// the counts follow the reason, not the status.
func TestReviewStoreListFiltersAndOrdersByArrival(t *testing.T) {
	e := inbox3(t)
	ctx := context.Background()
	_, err := e.reviews.RecordDecision(ctx, humanDecision("a-3", domain.DecisionAccept))
	require.NoError(t, err)

	pending, counts, err := e.reviews.List(ctx, domain.ReviewFilter{Status: domain.ReviewPending})
	require.NoError(t, err)
	assert.Equal(t, []string{"a-1", "a-2"}, caseIDs(pending))
	assert.Equal(t, domain.ReviewCounts{Pending: 2, Decided: 1}, counts)

	desc, _, err := e.reviews.List(ctx, domain.ReviewFilter{Status: domain.ReviewPending, Descending: true})
	require.NoError(t, err)
	assert.Equal(t, []string{"a-2", "a-1"}, caseIDs(desc))

	decided, _, err := e.reviews.List(ctx, domain.ReviewFilter{Status: domain.ReviewDecided})
	require.NoError(t, err)
	assert.Equal(t, []string{"a-3"}, caseIDs(decided))
	require.NotNil(t, decided[0].Decision)
	assert.Equal(t, decisionInMicros(humanDecision("a-3", domain.DecisionAccept)), *decided[0].Decision)

	byReason, counts, err := e.reviews.List(ctx, domain.ReviewFilter{Status: domain.ReviewPending, Reason: domain.ReviewReasonProvisionalOffer})
	require.NoError(t, err)
	assert.Equal(t, []string{"a-1"}, caseIDs(byReason))
	assert.Equal(t, domain.ReviewCounts{Pending: 1, Decided: 1}, counts, "the counts follow the reason, not the status")

	all, _, err := e.reviews.List(ctx, domain.ReviewFilter{})
	require.NoError(t, err)
	assert.Equal(t, []string{"a-1", "a-3", "a-2"}, caseIDs(all))

	none, counts, err := e.reviews.List(ctx, domain.ReviewFilter{Status: domain.ReviewPending, Reason: "unknown"})
	require.NoError(t, err)
	assert.Empty(t, none)
	assert.NotNil(t, none, "an empty list, never nil")
	assert.Equal(t, domain.ReviewCounts{}, counts)
}

func TestReviewStoreOrdersTiesByAcceptanceID(t *testing.T) {
	e := newInbox(t)
	e.route(t, routedRequest("b", domain.ReviewReasonProvisionalOffer, routedAt))
	e.route(t, routedRequest("a", domain.ReviewReasonProvisionalOffer, routedAt))

	asc, _, err := e.reviews.List(context.Background(), domain.ReviewFilter{Status: domain.ReviewPending})
	require.NoError(t, err)
	assert.Equal(t, []string{"a", "b"}, caseIDs(asc))
	desc, _, err := e.reviews.List(context.Background(), domain.ReviewFilter{Status: domain.ReviewPending, Descending: true})
	require.NoError(t, err)
	assert.Equal(t, []string{"a", "b"}, caseIDs(desc), "ties stay by acceptance id, also descending")
}

// CF-1 / B-CA-4: the decision is recorded once, with the advisor, the date and
// time (µs), the type, the observations, the NUMERIC surcharge and the adjusted
// offer; a second decision never replaces it.
func TestReviewStoreRecordsADecisionOnlyOnce(t *testing.T) {
	e := inbox3(t)
	ctx := context.Background()
	d := humanDecision("a-1", domain.DecisionAdjust)

	c, err := e.reviews.RecordDecision(ctx, d)
	require.NoError(t, err)
	assert.Equal(t, domain.ReviewDecided, c.Status)
	assert.Equal(t, decisionInMicros(d), *c.Decision)
	assert.InDelta(t, 12.5, *c.Decision.PremiumAdjustmentPct, 1e-9)

	got, err := e.reviews.Get(ctx, "a-1")
	require.NoError(t, err)
	assert.Equal(t, c, got)

	_, err = e.reviews.RecordDecision(ctx, humanDecision("a-1", domain.DecisionReject))
	assert.ErrorIs(t, err, ports.ErrReviewAlreadyDecided)
	got, err = e.reviews.Get(ctx, "a-1")
	require.NoError(t, err)
	assert.Equal(t, domain.DecisionAdjust, got.Decision.Type, "the first decision is never replaced")

	_, err = e.reviews.RecordDecision(ctx, humanDecision("missing", domain.DecisionAccept))
	assert.ErrorIs(t, err, ports.ErrReviewRequestNotFound)

	// The other decision types store no surcharge nor offer, and no e-mail is NULL.
	r := humanDecision("a-2", domain.DecisionReject)
	r.Advisor.Email = ""
	c, err = e.reviews.RecordDecision(ctx, r)
	require.NoError(t, err)
	assert.Equal(t, decisionInMicros(r), *c.Decision)
	assert.Nil(t, c.Decision.PremiumAdjustmentPct)
	assert.Nil(t, c.Decision.AdjustedOffer)
}

func TestReviewStoreRefusesADecisionThatBreaksItsInvariants(t *testing.T) {
	e := inbox3(t)
	ctx := context.Background()
	bad := humanDecision("a-1", domain.DecisionReject)
	p := 10.0
	bad.PremiumAdjustmentPct = &p
	_, err := e.reviews.RecordDecision(ctx, bad)
	require.Error(t, err, "a surcharge only for an adjust")
	assert.NotErrorIs(t, err, ports.ErrReviewAlreadyDecided)

	c, err := e.reviews.Get(ctx, "a-1")
	require.NoError(t, err)
	assert.Equal(t, domain.ReviewPending, c.Status, "a failed decision leaves the case pending")
	assert.Nil(t, c.Decision)
}

// CF-4 / EC-D02: of twenty decisions of the same case at once, one wins, in
// every repetition, and the case holds the winner's decision.
func TestReviewStoreOnlyOneOfTwentyConcurrentDecisionsWinsInEveryRepetition(t *testing.T) {
	e := newInbox(t)
	ctx := context.Background()
	for rep := 0; rep < 10; rep++ {
		id := fmt.Sprintf("race-%d", rep)
		e.route(t, routedRequest(id, domain.ReviewReasonProvisionalOffer, routedAt))
		var wg sync.WaitGroup
		var mu sync.Mutex
		winners := []string{}
		start := make(chan struct{})
		for i := 0; i < 20; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				d := humanDecision(id, domain.DecisionAdjust)
				d.ID = fmt.Sprintf("dec-%d-%d", rep, i)
				p := float64(i + 1)
				d.PremiumAdjustmentPct = &p
				<-start
				_, err := e.reviews.RecordDecision(ctx, d)
				mu.Lock()
				defer mu.Unlock()
				switch {
				case err == nil:
					winners = append(winners, d.ID)
				case errors.Is(err, ports.ErrReviewAlreadyDecided):
				default:
					t.Errorf("rep %d: unexpected error: %v", rep, err)
				}
			}(i)
		}
		close(start)
		wg.Wait()
		require.Len(t, winners, 1, "rep %d", rep)
		c, err := e.reviews.Get(ctx, id)
		require.NoError(t, err)
		assert.Equal(t, winners[0], c.Decision.ID, "rep %d", rep)
		var n int
		require.NoError(t, e.db.QueryRowContext(ctx, `SELECT count(*) FROM human_decision WHERE acceptance_id = $1`, id).Scan(&n))
		assert.Equal(t, 1, n, "rep %d: one row in human_decision", rep)
	}
}

// B-CA-4: the record is append-only and a case never goes back nor disappears.
func TestTheInboxTablesKeepTheRecord(t *testing.T) {
	e := inbox3(t)
	ctx := context.Background()
	_, err := e.reviews.RecordDecision(ctx, humanDecision("a-1", domain.DecisionReject))
	require.NoError(t, err)

	for _, stmt := range []string{
		`UPDATE human_decision SET observations = 'cambiada' WHERE acceptance_id = 'a-1'`,
		`DELETE FROM human_decision WHERE acceptance_id = 'a-1'`,
		`TRUNCATE human_decision CASCADE`,
		`UPDATE review_request SET status = 'pending' WHERE acceptance_id = 'a-1'`,
		`DELETE FROM review_request WHERE acceptance_id = 'a-2'`,
		`TRUNCATE review_request CASCADE`,
	} {
		_, err := e.db.ExecContext(ctx, stmt)
		assert.Error(t, err, stmt)
	}
	c, err := e.reviews.Get(ctx, "a-1")
	require.NoError(t, err)
	assert.Equal(t, domain.ReviewDecided, c.Status)
	assert.Equal(t, "Revisado con el soporte de ingresos", c.Decision.Observations)
	_, err = e.reviews.Get(ctx, "a-2")
	assert.NoError(t, err)
}

// CF-10: decided_by counts the cases each advisor resolved.
func TestTheDecisionsCanBeCountedByAdvisor(t *testing.T) {
	e := inbox3(t)
	ctx := context.Background()
	for _, id := range []string{"a-1", "a-2"} {
		_, err := e.reviews.RecordDecision(ctx, humanDecision(id, domain.DecisionReject))
		require.NoError(t, err)
	}
	other := humanDecision("a-3", domain.DecisionAccept)
	other.Advisor.ID = "adv-2"
	_, err := e.reviews.RecordDecision(ctx, other)
	require.NoError(t, err)

	rows, err := e.db.QueryContext(ctx, `SELECT decided_by, count(*) FROM human_decision GROUP BY decided_by ORDER BY decided_by`)
	require.NoError(t, err)
	defer func() { _ = rows.Close() }()
	got := map[string]int{}
	for rows.Next() {
		var by string
		var n int
		require.NoError(t, rows.Scan(&by, &n))
		got[by] = n
	}
	require.NoError(t, rows.Err())
	assert.Equal(t, map[string]int{"adv-1": 2, "adv-2": 1}, got)
}

func TestReviewStoreFindsTheDecisionOfAnAdjustedOffer(t *testing.T) {
	e := inbox3(t)
	ctx := context.Background()
	d := humanDecision("a-2", domain.DecisionAdjust)
	_, err := e.reviews.RecordDecision(ctx, d)
	require.NoError(t, err)
	_, err = e.reviews.RecordDecision(ctx, humanDecision("a-1", domain.DecisionReject))
	require.NoError(t, err)

	got, found, err := e.reviews.DecisionForAdjustedOffer(ctx, "adj-a-2")
	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, decisionInMicros(d), got)

	for _, id := range []string{"off-a-1", "adj-missing", "", "adj-\x00"} {
		_, found, err = e.reviews.DecisionForAdjustedOffer(ctx, id)
		require.NoError(t, err, "%q", id)
		assert.False(t, found, "%q", id)
	}
}

// D-3 c of HU-W1.1-02: the customer notices that are due, the earliest first;
// the notice round-trips.
func TestReviewStoreListsTheDueNotifications(t *testing.T) {
	e := inbox3(t)
	ctx := context.Background()
	at := routedAt
	require.NoError(t, e.reviews.UpdateNotification(ctx, "a-2", domain.ReviewNotification{Channel: domain.ChannelEmail, Status: domain.NotificationPending, NextAttemptAt: at}))
	require.NoError(t, e.reviews.UpdateNotification(ctx, "a-1", domain.ReviewNotification{Channel: domain.ChannelEmail, Status: domain.NotificationPending, NextAttemptAt: at.Add(-time.Second)}))
	require.NoError(t, e.reviews.UpdateNotification(ctx, "a-3", domain.ReviewNotification{Channel: domain.ChannelEmail, Status: domain.NotificationPending, NextAttemptAt: at.Add(time.Minute)}))

	due, err := e.reviews.ListDueNotifications(ctx, at, 10)
	require.NoError(t, err)
	assert.Equal(t, []string{"a-1", "a-2"}, caseIDs(due))

	limited, err := e.reviews.ListDueNotifications(ctx, at.Add(time.Hour), 2)
	require.NoError(t, err)
	assert.Equal(t, []string{"a-1", "a-2"}, caseIDs(limited))

	sent := domain.ReviewNotification{Channel: domain.ChannelEmail, Status: domain.NotificationSent, Attempts: 2,
		SentAt: at.Add(time.Second), LastFailureAt: at, LastFailure: "provider unavailable"}
	require.NoError(t, e.reviews.UpdateNotification(ctx, "a-1", sent))
	require.NoError(t, e.reviews.UpdateNotification(ctx, "a-2", domain.ReviewNotification{Channel: domain.ChannelEmail, Status: domain.NotificationFailed}))
	due, err = e.reviews.ListDueNotifications(ctx, at.Add(time.Hour), 0)
	require.NoError(t, err)
	assert.Equal(t, []string{"a-3"}, caseIDs(due), "sent and failed notices are not due")

	c, err := e.reviews.Get(ctx, "a-1")
	require.NoError(t, err)
	want := sent
	want.SentAt, want.LastFailureAt = micro(want.SentAt), micro(want.LastFailureAt)
	assert.Equal(t, &want, c.Notification)

	// A notice with no due date is due at once (NULL is the zero time).
	require.NoError(t, e.reviews.UpdateNotification(ctx, "a-2", domain.ReviewNotification{Channel: domain.ChannelEmail, Status: domain.NotificationPending}))
	due, err = e.reviews.ListDueNotifications(ctx, at, 0)
	require.NoError(t, err)
	assert.Equal(t, []string{"a-2"}, caseIDs(due))

	assert.ErrorIs(t, e.reviews.UpdateNotification(ctx, "missing", domain.ReviewNotification{}), ports.ErrReviewRequestNotFound)
	assert.ErrorIs(t, e.reviews.UpdateNotification(ctx, "a\x00", domain.ReviewNotification{}), ports.ErrReviewRequestNotFound)
}

func TestReviewStoreUnknownIdsAreNotFound(t *testing.T) {
	e := newInbox(t)
	ctx := context.Background()
	for _, id := range []string{"missing", "", "with\x00nul"} {
		_, err := e.reviews.Get(ctx, id)
		assert.ErrorIs(t, err, ports.ErrReviewRequestNotFound, "%q", id)
		d := humanDecision(id, domain.DecisionAccept)
		_, err = e.reviews.RecordDecision(ctx, d)
		assert.ErrorIs(t, err, ports.ErrReviewRequestNotFound, "%q", id)
	}
}
