package postgres_test

import (
	"context"
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

// These tests run against a real PostgreSQL (Testcontainers). With
// REQUIRE_DOCKER=1 they fail instead of skipping when Docker is missing.

var acceptedAt = time.Date(2026, 10, 23, 14, 0, 0, 123_456_789, time.UTC)

func newAcceptanceStore(t *testing.T) *postgres.AcceptanceStore {
	t.Helper()
	requireOfferDocker(t)
	return postgres.NewAcceptanceStore(newDB(t))
}

func registered(id, offerID string, at time.Time) domain.Acceptance {
	return domain.Acceptance{
		ID: id, OfferID: offerID, CustomerID: "cust-1", Channel: domain.ChannelWeb, AcceptedAt: at,
		Status: domain.AcceptanceRegistered, NextAttemptAt: at, UpdatedAt: at,
	}
}

// inMicros truncates the times of an acceptance to the precision PostgreSQL keeps.
func inMicros(a domain.Acceptance) domain.Acceptance {
	tr := func(t time.Time) time.Time { return t.Truncate(time.Microsecond) }
	a.AcceptedAt, a.LastFailureAt, a.NextAttemptAt, a.UpdatedAt = tr(a.AcceptedAt), tr(a.LastFailureAt), tr(a.NextAttemptAt), tr(a.UpdatedAt)
	if a.Decision != nil {
		d := *a.Decision
		d.DecidedAt = tr(d.DecidedAt)
		a.Decision = &d
	}
	if a.Review != nil {
		r := *a.Review
		r.DecidedAt = tr(r.DecidedAt)
		if r.AdjustedOffer != nil {
			o := *r.AdjustedOffer
			o.ExpiresAt = tr(o.ExpiresAt)
			r.AdjustedOffer = &o
		}
		a.Review = &r
	}
	return a
}

// CF-2 / A-CA-1: every field of the acceptance round-trips, from the first
// write to the adjusted state the advisor decision leaves.
func TestAcceptanceStoreRoundTripsTheWholeAcceptance(t *testing.T) {
	s := newAcceptanceStore(t)
	ctx := context.Background()
	a := registered("acc-1", "demo-review-1", acceptedAt)

	stored, created, err := s.Create(ctx, a)
	require.NoError(t, err)
	assert.True(t, created)
	assert.Equal(t, inMicros(a), stored)

	a.Channel = domain.ChannelMobile
	a.Status = domain.AcceptanceAdjusted
	a.Decision = &domain.UnderwritingDecision{Outcome: domain.UnderwritingHumanReview, Reason: domain.ReviewReasonProvisionalOffer,
		DecidedAt: acceptedAt.Add(time.Second)}
	a.Review = &domain.AcceptanceReview{Outcome: domain.DecisionAdjust, DecidedAt: acceptedAt.Add(time.Hour),
		AdjustedOffer: &domain.AdjustedOffer{ID: "adj-acc-1", Premium: 98_450.55, Currency: "COP", ExpiresAt: acceptedAt.Add(25 * time.Hour)}}
	a.Attempts = 3
	a.LastFailureAt = acceptedAt.Add(2 * time.Second)
	a.NextAttemptAt = time.Time{}
	a.UpdatedAt = acceptedAt.Add(time.Hour)
	require.NoError(t, s.Update(ctx, a))

	got, err := s.Get(ctx, "acc-1")
	require.NoError(t, err)
	assert.Equal(t, inMicros(a), got)
	byOffer, err := s.GetByOffer(ctx, "demo-review-1")
	require.NoError(t, err)
	assert.Equal(t, got, byOffer)

	// The other outcomes: approved without a reason, and a review without an
	// adjusted offer.
	a.Status = domain.AcceptanceRejected
	a.Decision = &domain.UnderwritingDecision{Outcome: domain.UnderwritingApproved, DecidedAt: acceptedAt}
	a.Review = &domain.AcceptanceReview{Outcome: domain.DecisionReject, DecidedAt: acceptedAt.Add(time.Minute)}
	require.NoError(t, s.Update(ctx, a))
	got, err = s.Get(ctx, "acc-1")
	require.NoError(t, err)
	assert.Equal(t, inMicros(a), got)
	assert.Empty(t, got.Decision.Reason)
	assert.Nil(t, got.Review.AdjustedOffer)
}

// CF-2: an offer has one acceptance, however many requests race to create it.
func TestAcceptanceStoreKeepsOneAcceptancePerOfferUnderConcurrency(t *testing.T) {
	s := newAcceptanceStore(t)
	ctx := context.Background()
	const n = 20
	var wg sync.WaitGroup
	ids := make([]string, n)
	created := make([]bool, n)
	errs := make([]error, n)
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			stored, c, err := s.Create(ctx, registered(fmt.Sprintf("acc-%02d", i), "demo-active-race", acceptedAt))
			ids[i], created[i], errs[i] = stored.ID, c, err
		}(i)
	}
	close(start)
	wg.Wait()

	wins := 0
	for i := 0; i < n; i++ {
		require.NoError(t, errs[i])
		if created[i] {
			wins++
		}
		assert.Equal(t, ids[0], ids[i], "every request gets the same acceptance")
	}
	assert.Equal(t, 1, wins)
	stored, err := s.GetByOffer(ctx, "demo-active-race")
	require.NoError(t, err)
	assert.Equal(t, ids[0], stored.ID)
}

func TestAcceptanceStoreCreateReturnsTheExistingAcceptanceOfTheOffer(t *testing.T) {
	s := newAcceptanceStore(t)
	ctx := context.Background()
	first := registered("acc-1", "o-1", acceptedAt)
	_, _, err := s.Create(ctx, first)
	require.NoError(t, err)

	stored, created, err := s.Create(ctx, registered("acc-2", "o-1", acceptedAt.Add(time.Minute)))
	require.NoError(t, err)
	assert.False(t, created)
	assert.Equal(t, inMicros(first), stored)

	_, _, err = s.Create(ctx, registered("acc-1", "o-2", acceptedAt))
	assert.Error(t, err, "an id taken by another offer is not silently another acceptance")
}

// CF-2 / A-CA-5: ListDue returns the registered acceptances due at now (or with
// no due date), oldest first, with the limit; no other state is listed.
func TestAcceptanceStoreListsDueRegisteredAcceptancesOldestFirst(t *testing.T) {
	s := newAcceptanceStore(t)
	ctx := context.Background()
	now := acceptedAt.Add(time.Hour)

	mk := func(id string, accepted time.Time, next time.Time, status domain.AcceptanceStatus) {
		a := registered(id, "offer-"+id, accepted)
		a.NextAttemptAt, a.Status = next, status
		_, _, err := s.Create(ctx, a)
		require.NoError(t, err)
	}
	mk("c", acceptedAt.Add(3*time.Minute), now, domain.AcceptanceRegistered)                 // due exactly now
	mk("a", acceptedAt.Add(time.Minute), time.Time{}, domain.AcceptanceRegistered)           // no due date: due
	mk("b", acceptedAt.Add(time.Minute), now.Add(-time.Second), domain.AcceptanceRegistered) // same time as a
	mk("d", acceptedAt, now.Add(time.Second), domain.AcceptanceRegistered)                   // not due yet
	mk("e", acceptedAt, time.Time{}, domain.AcceptanceInReview)                              // other states never
	mk("f", acceptedAt, time.Time{}, domain.AcceptanceIssuancePending)                       // are listed
	mk("g", acceptedAt.Add(4*time.Minute), now.Add(-time.Hour), domain.AcceptanceRegistered) // due

	due, err := s.ListDue(ctx, now, 0)
	require.NoError(t, err)
	got := []string{}
	for _, a := range due {
		got = append(got, a.ID)
	}
	assert.Equal(t, []string{"a", "b", "c", "g"}, got, "by acceptance time, ties by id")

	due, err = s.ListDue(ctx, now, 2)
	require.NoError(t, err)
	require.Len(t, due, 2)
	assert.Equal(t, "a", due[0].ID)
	assert.Equal(t, "b", due[1].ID)
	assert.True(t, due[0].NextAttemptAt.IsZero(), "NULL is the zero time")
}

func TestAcceptanceStoreUnknownIdsAreNotFound(t *testing.T) {
	s := newAcceptanceStore(t)
	ctx := context.Background()
	for _, id := range []string{"missing", "", "with\x00nul"} {
		_, err := s.Get(ctx, id)
		assert.ErrorIs(t, err, ports.ErrAcceptanceNotFound, "%q", id)
		_, err = s.GetByOffer(ctx, id)
		assert.ErrorIs(t, err, ports.ErrAcceptanceNotFound, "%q", id)
		err = s.Update(ctx, registered(id, "o-1", acceptedAt))
		assert.ErrorIs(t, err, ports.ErrAcceptanceNotFound, "%q", id)
	}
}

// The table refuses what the domain never writes: an unknown status, half an
// underwriting decision.
func TestAcceptanceTableChecksItsStates(t *testing.T) {
	requireOfferDocker(t)
	db := newDB(t)
	ctx := context.Background()
	_, err := db.ExecContext(ctx, `INSERT INTO acceptance (acceptance_id, offer_id, customer_id, channel, accepted_at, status)
		VALUES ('a-1', 'o-1', 'c-1', 'web', now(), 'paid')`)
	assert.Error(t, err, "unknown status")
	_, err = db.ExecContext(ctx, `INSERT INTO acceptance (acceptance_id, offer_id, customer_id, channel, accepted_at, status, underwriting_outcome)
		VALUES ('a-2', 'o-2', 'c-1', 'web', now(), 'registered', 'approved')`)
	assert.Error(t, err, "an underwriting outcome without its date")
	_, err = db.ExecContext(ctx, `INSERT INTO acceptance (acceptance_id, offer_id, customer_id, channel, accepted_at, status, review_outcome, review_decided_at, review_adjusted_offer)
		VALUES ('a-3', 'o-3', 'c-1', 'web', now(), 'rejected', 'reject', now(), '{}')`)
	assert.Error(t, err, "an adjusted offer on a reject")
}
