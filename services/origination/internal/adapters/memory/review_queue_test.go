package memory_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/memory"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
)

func routed(id, reason string, at time.Time) domain.ReviewRequest {
	return domain.ReviewRequest{
		AcceptanceID: id, OfferID: "demo-review-" + id, CustomerID: "cust-" + id,
		Applicant: domain.Applicant{FullName: "Ana Pérez", DocumentID: "CC-1", Email: "ana@example.com"},
		Premium:   89500, Currency: "COP", Reason: reason, ReceivedAt: at,
	}
}

func decision(id string, typ domain.DecisionType) domain.HumanDecision {
	return domain.HumanDecision{
		ID: "dec-" + id, AcceptanceID: id, Type: typ, Advisor: domain.Advisor{ID: "adv-1", Email: "adv@example.com"},
		DecidedAt: t0.Add(time.Hour), Observations: "revisado",
	}
}

func inbox(t *testing.T) *memory.ReviewQueueStub {
	t.Helper()
	q := memory.NewReviewQueueStub()
	ctx := context.Background()
	require.NoError(t, q.Enqueue(ctx, routed("a-2", domain.ReviewReasonHighRiskScore, t0.Add(2*time.Minute))))
	require.NoError(t, q.Enqueue(ctx, routed("a-1", domain.ReviewReasonProvisionalOffer, t0)))
	require.NoError(t, q.Enqueue(ctx, routed("a-3", domain.ReviewReasonProvisionalOffer, t0.Add(time.Minute))))
	return q
}

func ids(cases []domain.ReviewCase) []string {
	out := make([]string, 0, len(cases))
	for _, c := range cases {
		out = append(out, c.Request.AcceptanceID)
	}
	return out
}

// CA-1: the routed request appears pending with its data.
func TestReviewQueueKeepsTheRoutedRequestPending(t *testing.T) {
	q := inbox(t)

	c, err := q.Get(context.Background(), "a-1")
	require.NoError(t, err)
	assert.Equal(t, domain.ReviewPending, c.Status)
	assert.Equal(t, routed("a-1", domain.ReviewReasonProvisionalOffer, t0), c.Request)
	assert.Nil(t, c.Decision)
	assert.Nil(t, c.Notification)

	_, err = q.Get(context.Background(), "missing")
	assert.ErrorIs(t, err, ports.ErrReviewRequestNotFound)
}

// CA-5: filter by status and reason, order by arrival.
func TestReviewQueueListFiltersAndOrdersByArrival(t *testing.T) {
	q := inbox(t)
	ctx := context.Background()
	_, err := q.RecordDecision(ctx, decision("a-3", domain.DecisionAccept))
	require.NoError(t, err)

	pending, counts, err := q.List(ctx, domain.ReviewFilter{Status: domain.ReviewPending})
	require.NoError(t, err)
	assert.Equal(t, []string{"a-1", "a-2"}, ids(pending))
	assert.Equal(t, domain.ReviewCounts{Pending: 2, Decided: 1}, counts)

	desc, _, err := q.List(ctx, domain.ReviewFilter{Status: domain.ReviewPending, Descending: true})
	require.NoError(t, err)
	assert.Equal(t, []string{"a-2", "a-1"}, ids(desc))

	decided, _, err := q.List(ctx, domain.ReviewFilter{Status: domain.ReviewDecided})
	require.NoError(t, err)
	assert.Equal(t, []string{"a-3"}, ids(decided))
	require.NotNil(t, decided[0].Decision)

	byReason, counts, err := q.List(ctx, domain.ReviewFilter{Status: domain.ReviewPending, Reason: domain.ReviewReasonProvisionalOffer})
	require.NoError(t, err)
	assert.Equal(t, []string{"a-1"}, ids(byReason))
	assert.Equal(t, domain.ReviewCounts{Pending: 1, Decided: 1}, counts, "the counts follow the reason, not the status")

	all, _, err := q.List(ctx, domain.ReviewFilter{})
	require.NoError(t, err)
	assert.Equal(t, []string{"a-1", "a-3", "a-2"}, ids(all))

	none, counts, err := q.List(ctx, domain.ReviewFilter{Status: domain.ReviewPending, Reason: "unknown"})
	require.NoError(t, err)
	assert.Empty(t, none)
	assert.NotNil(t, none, "an empty list, never nil")
	assert.Equal(t, domain.ReviewCounts{}, counts)
}

func TestReviewQueueOrdersTiesByAcceptanceID(t *testing.T) {
	q := memory.NewReviewQueueStub()
	ctx := context.Background()
	require.NoError(t, q.Enqueue(ctx, routed("b", domain.ReviewReasonProvisionalOffer, t0)))
	require.NoError(t, q.Enqueue(ctx, routed("a", domain.ReviewReasonProvisionalOffer, t0)))

	got, _, err := q.List(ctx, domain.ReviewFilter{Status: domain.ReviewPending})
	require.NoError(t, err)
	assert.Equal(t, []string{"a", "b"}, ids(got))
}

// CA-3 and CA-4: a decision closes the case once and is kept as recorded.
func TestReviewQueueRecordsADecisionOnlyOnce(t *testing.T) {
	q := inbox(t)
	ctx := context.Background()
	p := 10.0
	d := decision("a-1", domain.DecisionAdjust)
	d.PremiumAdjustmentPct = &p
	d.AdjustedOffer = &domain.AdjustedOffer{ID: "adj-a-1", Premium: 98450, Currency: "COP", ExpiresAt: t0.Add(25 * time.Hour)}

	c, err := q.RecordDecision(ctx, d)
	require.NoError(t, err)
	assert.Equal(t, domain.ReviewDecided, c.Status)
	assert.Equal(t, d, *c.Decision)
	assert.Len(t, q.Pending(), 2, "the decided case left the pending list")

	again := decision("a-1", domain.DecisionReject)
	_, err = q.RecordDecision(ctx, again)
	assert.ErrorIs(t, err, ports.ErrReviewAlreadyDecided)
	got, err := q.Get(ctx, "a-1")
	require.NoError(t, err)
	assert.Equal(t, domain.DecisionAdjust, got.Decision.Type, "the first decision is never replaced")

	_, err = q.RecordDecision(ctx, decision("missing", domain.DecisionAccept))
	assert.ErrorIs(t, err, ports.ErrReviewRequestNotFound)
}

func TestReviewQueueReturnsCopies(t *testing.T) {
	q := inbox(t)
	ctx := context.Background()
	p := 10.0
	d := decision("a-1", domain.DecisionAdjust)
	d.PremiumAdjustmentPct = &p
	d.AdjustedOffer = &domain.AdjustedOffer{ID: "adj-a-1", Premium: 98450}
	_, err := q.RecordDecision(ctx, d)
	require.NoError(t, err)
	require.NoError(t, q.UpdateNotification(ctx, "a-1", domain.ReviewNotification{Status: domain.NotificationPending}))

	p = 50
	d.AdjustedOffer.Premium = 1
	c, err := q.Get(ctx, "a-1")
	require.NoError(t, err)
	assert.InDelta(t, 10.0, *c.Decision.PremiumAdjustmentPct, 1e-9)
	assert.InDelta(t, 98450.0, c.Decision.AdjustedOffer.Premium, 1e-9)

	c.Decision.Observations = "changed"
	*c.Decision.PremiumAdjustmentPct = 99
	c.Decision.AdjustedOffer.Premium = 2
	c.Notification.Attempts = 9
	again, err := q.Get(ctx, "a-1")
	require.NoError(t, err)
	assert.Equal(t, "revisado", again.Decision.Observations)
	assert.InDelta(t, 10.0, *again.Decision.PremiumAdjustmentPct, 1e-9)
	assert.InDelta(t, 98450.0, again.Decision.AdjustedOffer.Premium, 1e-9)
	assert.Zero(t, again.Notification.Attempts)
}

// D-7 a (fix C5): the inbox keeps its own copy of the snapshot of the routed
// offer, like the PostgreSQL one.
func TestReviewQueueKeepsACopyOfTheOfferSnapshot(t *testing.T) {
	q := memory.NewReviewQueueStub()
	ctx := context.Background()
	req := routed("a-1", domain.ReviewReasonProvisionalOffer, t0)
	req.OfferMode, req.ProfileScore, req.OfferExpiresAt = domain.ModeProvisional, 0.5, t0.Add(24*time.Hour)
	req.Terms = domain.CoverageTerms{InsuredAmount: 100, TermMonths: 12, Coverages: []domain.Coverage{{Code: domain.CoverageDeath, InsuredAmount: 100}}}
	require.NoError(t, q.Enqueue(ctx, req))

	req.Terms.Coverages[0].InsuredAmount = 1
	c, err := q.Get(ctx, "a-1")
	require.NoError(t, err)
	assert.InDelta(t, 100, c.Request.Terms.Coverages[0].InsuredAmount, 1e-9)
	assert.Equal(t, domain.ModeProvisional, c.Request.OfferMode)
	c.Request.Terms.Coverages[0].InsuredAmount = 2
	again, err := q.Get(ctx, "a-1")
	require.NoError(t, err)
	assert.InDelta(t, 100, again.Request.Terms.Coverages[0].InsuredAmount, 1e-9)
}

// EC-D02 claim: twenty advisors decide the same case at once, one wins.
func TestReviewQueueConcurrentDecisionsRecordExactlyOne(t *testing.T) {
	q := inbox(t)
	ctx := context.Background()
	var wg sync.WaitGroup
	var mu sync.Mutex
	won, lost := 0, 0
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			d := decision("a-1", domain.DecisionReject)
			d.ID = fmt.Sprintf("dec-%d", i)
			_, err := q.RecordDecision(ctx, d)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				won++
			case errors.Is(err, ports.ErrReviewAlreadyDecided):
				lost++
			default:
				t.Errorf("unexpected error: %v", err)
			}
		}(i)
	}
	wg.Wait()
	assert.Equal(t, 1, won)
	assert.Equal(t, 19, lost)
}

func TestReviewQueueFindsTheDecisionOfAnAdjustedOffer(t *testing.T) {
	q := inbox(t)
	ctx := context.Background()
	d := decision("a-2", domain.DecisionAdjust)
	d.AdjustedOffer = &domain.AdjustedOffer{ID: "adj-a-2", Premium: 98450}
	_, err := q.RecordDecision(ctx, d)
	require.NoError(t, err)
	_, err = q.RecordDecision(ctx, decision("a-1", domain.DecisionReject))
	require.NoError(t, err)

	got, found, err := q.DecisionForAdjustedOffer(ctx, "adj-a-2")
	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, "a-2", got.AcceptanceID)

	_, found, err = q.DecisionForAdjustedOffer(ctx, "demo-review-a-1")
	require.NoError(t, err)
	assert.False(t, found)
}

// D-3 c: the customer notices that are due, the earliest first.
func TestReviewQueueListsTheDueNotifications(t *testing.T) {
	q := inbox(t)
	ctx := context.Background()
	require.NoError(t, q.UpdateNotification(ctx, "a-2", domain.ReviewNotification{Status: domain.NotificationPending, NextAttemptAt: t0}))
	require.NoError(t, q.UpdateNotification(ctx, "a-1", domain.ReviewNotification{Status: domain.NotificationPending, NextAttemptAt: t0.Add(-time.Second)}))
	require.NoError(t, q.UpdateNotification(ctx, "a-3", domain.ReviewNotification{Status: domain.NotificationPending, NextAttemptAt: t0.Add(time.Minute)}))

	due, err := q.ListDueNotifications(ctx, t0, 10)
	require.NoError(t, err)
	assert.Equal(t, []string{"a-1", "a-2"}, ids(due))

	limited, err := q.ListDueNotifications(ctx, t0.Add(time.Hour), 2)
	require.NoError(t, err)
	assert.Equal(t, []string{"a-1", "a-2"}, ids(limited))

	require.NoError(t, q.UpdateNotification(ctx, "a-1", domain.ReviewNotification{Status: domain.NotificationSent, SentAt: t0}))
	require.NoError(t, q.UpdateNotification(ctx, "a-2", domain.ReviewNotification{Status: domain.NotificationFailed}))
	due, err = q.ListDueNotifications(ctx, t0.Add(time.Hour), 10)
	require.NoError(t, err)
	assert.Equal(t, []string{"a-3"}, ids(due), "sent and failed notices are not due")

	assert.ErrorIs(t, q.UpdateNotification(ctx, "missing", domain.ReviewNotification{}), ports.ErrReviewRequestNotFound)
}

func TestReviewQueueOrdersDueNotificationTiesByAcceptanceID(t *testing.T) {
	q := inbox(t)
	ctx := context.Background()
	for _, id := range []string{"a-3", "a-1"} {
		require.NoError(t, q.UpdateNotification(ctx, id, domain.ReviewNotification{Status: domain.NotificationPending, NextAttemptAt: t0}))
	}
	due, err := q.ListDueNotifications(ctx, t0, 0)
	require.NoError(t, err)
	assert.Equal(t, []string{"a-1", "a-3"}, ids(due))
}

func TestReviewQueueConcurrentNotificationUpdatesAndDecisions(t *testing.T) {
	q := inbox(t)
	ctx := context.Background()
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(3)
		go func(i int) {
			defer wg.Done()
			assert.NoError(t, q.UpdateNotification(ctx, "a-1", domain.ReviewNotification{Status: domain.NotificationPending, Attempts: i}))
		}(i)
		go func() {
			defer wg.Done()
			_, err := q.ListDueNotifications(ctx, t0.Add(time.Hour), 10)
			assert.NoError(t, err)
		}()
		go func() {
			defer wg.Done()
			_, _, err := q.List(ctx, domain.ReviewFilter{})
			assert.NoError(t, err)
		}()
	}
	wg.Wait()
	c, err := q.Get(ctx, "a-1")
	require.NoError(t, err)
	require.NotNil(t, c.Notification)
	assert.Equal(t, domain.NotificationPending, c.Notification.Status)
}

func TestOfferStubCreateStoresTheAdjustedOfferOnce(t *testing.T) {
	s := memory.NewOfferStub(&fixedClock{now: t0})
	ctx := context.Background()
	adjusted := domain.Offer{ID: "adj-a-1", CustomerID: "cust-1", Premium: 98450, Currency: "COP",
		Mode: domain.ModeProvisional, ProfileScore: 0.5, ExpiresAt: t0.Add(24 * time.Hour)}

	require.NoError(t, s.Create(ctx, adjusted))
	changed := adjusted
	changed.Premium = 1
	require.NoError(t, s.Create(ctx, changed), "creating it again is not an error")

	got, err := s.Get(ctx, "adj-a-1") // no caller needed: it is not a demo offer
	require.NoError(t, err)
	assert.Equal(t, adjusted, got, "the first offer is kept")
}

func TestAcceptanceStoreCopiesTheReview(t *testing.T) {
	s := memory.NewAcceptanceStore()
	ctx := context.Background()
	review := &domain.AcceptanceReview{Outcome: domain.DecisionAdjust, DecidedAt: t0,
		AdjustedOffer: &domain.AdjustedOffer{ID: "adj-a-1", Premium: 98450}}
	_, _, err := s.Create(ctx, domain.Acceptance{ID: "a-1", OfferID: "o-1", Status: domain.AcceptanceAdjusted, Review: review})
	require.NoError(t, err)

	review.AdjustedOffer.Premium = 1
	review.Outcome = domain.DecisionReject
	got, err := s.Get(ctx, "a-1")
	require.NoError(t, err)
	assert.Equal(t, domain.DecisionAdjust, got.Review.Outcome)
	assert.InDelta(t, 98450.0, got.Review.AdjustedOffer.Premium, 1e-9)

	_, _, err = s.Create(ctx, domain.Acceptance{ID: "a-2", OfferID: "o-2", Review: &domain.AcceptanceReview{Outcome: domain.DecisionReject}})
	require.NoError(t, err)
	got, err = s.Get(ctx, "a-2")
	require.NoError(t, err)
	assert.Nil(t, got.Review.AdjustedOffer)
}
