package memory_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/security"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/memory"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
)

var t0 = time.Date(2026, 10, 11, 15, 0, 0, 0, time.UTC)

type fixedClock struct{ now time.Time }

func (c *fixedClock) Now() time.Time { return c.now }

func caller(subject string) context.Context {
	return security.WithClaims(context.Background(), security.Claims{Subject: subject, Username: subject + "-name", Email: subject + "@example.com"})
}

func TestAcceptanceStoreKeepsOneAcceptancePerOffer(t *testing.T) {
	s := memory.NewAcceptanceStore()
	ctx := context.Background()
	d := &domain.UnderwritingDecision{Outcome: domain.UnderwritingApproved}
	first := domain.Acceptance{ID: "a-1", OfferID: "o-1", Status: domain.AcceptanceRegistered, Decision: d}

	stored, created, err := s.Create(ctx, first)
	require.NoError(t, err)
	assert.True(t, created)
	assert.Equal(t, first, stored)

	stored, created, err = s.Create(ctx, domain.Acceptance{ID: "a-2", OfferID: "o-1"})
	require.NoError(t, err)
	assert.False(t, created)
	assert.Equal(t, "a-1", stored.ID)

	d.Reason = "mutated"
	got, err := s.Get(ctx, "a-1")
	require.NoError(t, err)
	assert.Empty(t, got.Decision.Reason, "the store keeps its own copy")

	got, err = s.GetByOffer(ctx, "o-1")
	require.NoError(t, err)
	assert.Equal(t, "a-1", got.ID)

	_, err = s.Get(ctx, "a-2")
	assert.ErrorIs(t, err, ports.ErrAcceptanceNotFound)
	_, err = s.GetByOffer(ctx, "o-2")
	assert.ErrorIs(t, err, ports.ErrAcceptanceNotFound)
	assert.ErrorIs(t, s.Update(ctx, domain.Acceptance{ID: "a-2"}), ports.ErrAcceptanceNotFound)
}

func TestAcceptanceStoreListsDueRegisteredAcceptancesOldestFirst(t *testing.T) {
	s := memory.NewAcceptanceStore()
	ctx := context.Background()
	for _, a := range []domain.Acceptance{
		{ID: "late", OfferID: "o-1", Status: domain.AcceptanceRegistered, AcceptedAt: t0.Add(2 * time.Minute), NextAttemptAt: t0},
		{ID: "early", OfferID: "o-2", Status: domain.AcceptanceRegistered, AcceptedAt: t0, NextAttemptAt: t0},
		{ID: "future", OfferID: "o-3", Status: domain.AcceptanceRegistered, AcceptedAt: t0, NextAttemptAt: t0.Add(time.Second)},
		{ID: "done", OfferID: "o-4", Status: domain.AcceptanceIssuancePending, AcceptedAt: t0},
	} {
		_, _, err := s.Create(ctx, a)
		require.NoError(t, err)
	}

	due, err := s.ListDue(ctx, t0, 10)
	require.NoError(t, err)
	require.Len(t, due, 2)
	assert.Equal(t, "early", due[0].ID)
	assert.Equal(t, "late", due[1].ID)

	due, err = s.ListDue(ctx, t0, 1)
	require.NoError(t, err)
	assert.Len(t, due, 1)

	got, _ := s.Get(ctx, "future")
	got.Status = domain.AcceptanceInReview
	require.NoError(t, s.Update(ctx, got))
	due, _ = s.ListDue(ctx, t0.Add(time.Hour), 10)
	assert.Len(t, due, 2)
}

func TestOfferStubServesTheDemoScenariosToTheirFirstCaller(t *testing.T) {
	s := memory.NewOfferStub(&fixedClock{now: t0})

	active, err := s.Get(caller("cust-1"), "demo-active-001")
	require.NoError(t, err)
	assert.Equal(t, "cust-1", active.CustomerID)
	assert.Equal(t, domain.Applicant{FullName: "cust-1-name", DocumentID: "demo-document", Email: "cust-1@example.com"}, active.Applicant)
	assert.Equal(t, domain.ModeNormal, active.Mode)
	assert.Equal(t, t0.Add(memory.OfferValidity), active.ExpiresAt)
	assert.False(t, active.IsExpired(t0))

	again, err := s.Get(caller("cust-2"), "demo-active-001")
	require.NoError(t, err)
	assert.Equal(t, active, again, "the offer keeps its first owner")
	again, err = s.Get(context.Background(), "demo-active-001")
	require.NoError(t, err)
	assert.Equal(t, active, again, "the processor reads it without a caller")

	review, err := s.Get(caller("cust-1"), "demo-review-x")
	require.NoError(t, err)
	assert.Equal(t, domain.ModeProvisional, review.Mode)

	low, err := s.Get(caller("cust-1"), "demo-lowscore-x")
	require.NoError(t, err)
	assert.Less(t, low.ProfileScore, memory.LowRiskScoreThreshold)

	expired, err := s.Get(caller("cust-1"), "demo-expired-x")
	require.NoError(t, err)
	assert.True(t, expired.IsExpired(t0))

	flaky, err := s.Get(caller("cust-1"), "demo-flaky-x")
	require.NoError(t, err)
	assert.False(t, flaky.IsExpired(t0))

	for _, id := range []string{"offer-1", "demo-unknown-1", "demo-active-", "demo-active-a/b"} {
		_, err := s.Get(caller("cust-1"), id)
		assert.ErrorIs(t, err, ports.ErrOfferNotFound, id)
	}
	_, err = s.Get(context.Background(), "demo-active-new")
	assert.ErrorIs(t, err, ports.ErrOfferNotFound, "a new demo offer needs a caller")
}

func TestOfferStubFallsBackToTheEmailAsName(t *testing.T) {
	s := memory.NewOfferStub(&fixedClock{now: t0})
	ctx := security.WithClaims(context.Background(), security.Claims{Subject: "s", Email: "s@example.com"})
	o, err := s.Get(ctx, "demo-active-1")
	require.NoError(t, err)
	assert.Equal(t, "s@example.com", o.Applicant.FullName)
}

func TestUnderwriterStubRules(t *testing.T) {
	u := memory.NewUnderwriterStub(&fixedClock{now: t0}, 1)
	ctx := context.Background()
	a := domain.Acceptance{ID: "a-1"}

	d, err := u.Decide(ctx, domain.Offer{ID: "o", Mode: domain.ModeNormal, ProfileScore: 0.7}, a)
	require.NoError(t, err)
	assert.Equal(t, domain.UnderwritingDecision{Outcome: domain.UnderwritingApproved, DecidedAt: t0}, d)

	d, err = u.Decide(ctx, domain.Offer{ID: "o", Mode: domain.ModeProvisional, ProfileScore: 0.9}, a)
	require.NoError(t, err)
	assert.Equal(t, domain.UnderwritingHumanReview, d.Outcome)
	assert.Equal(t, domain.ReviewReasonProvisionalOffer, d.Reason)

	d, err = u.Decide(ctx, domain.Offer{ID: "o", Mode: domain.ModeNormal, ProfileScore: 0.39}, a)
	require.NoError(t, err)
	assert.Equal(t, domain.ReviewReasonLowRiskScore, d.Reason)

	flaky := domain.Offer{ID: "demo-flaky-1", Mode: domain.ModeNormal, ProfileScore: 0.7}
	_, err = u.Decide(ctx, flaky, a)
	assert.ErrorIs(t, err, memory.ErrUnderwritingUnavailable)
	d, err = u.Decide(ctx, flaky, a)
	require.NoError(t, err)
	assert.Equal(t, domain.UnderwritingApproved, d.Outcome)
	_, err = u.Decide(ctx, flaky, domain.Acceptance{ID: "a-2"})
	assert.ErrorIs(t, err, memory.ErrUnderwritingUnavailable, "counted per acceptance")
}

func TestReviewQueueAndIssuanceStubsAreIdempotent(t *testing.T) {
	ctx := context.Background()
	q := memory.NewReviewQueueStub()
	require.NoError(t, q.Enqueue(ctx, domain.ReviewRequest{AcceptanceID: "a-2", Reason: "r", ReceivedAt: t0.Add(time.Second)}))
	require.NoError(t, q.Enqueue(ctx, domain.ReviewRequest{AcceptanceID: "a-1", ReceivedAt: t0}))
	require.NoError(t, q.Enqueue(ctx, domain.ReviewRequest{AcceptanceID: "a-2", Reason: "other"}))
	pending := q.Pending()
	require.Len(t, pending, 2)
	assert.Equal(t, "a-1", pending[0].AcceptanceID)
	assert.Equal(t, "r", pending[1].Reason)

	s := memory.NewIssuanceStub()
	assert.False(t, s.Started("a-1"))
	require.NoError(t, s.Start(ctx, domain.Acceptance{ID: "a-1"}, domain.Offer{ID: "o-1"}))
	require.NoError(t, s.Start(ctx, domain.Acceptance{ID: "a-1"}, domain.Offer{ID: "o-1"}))
	assert.True(t, s.Started("a-1"))
}
