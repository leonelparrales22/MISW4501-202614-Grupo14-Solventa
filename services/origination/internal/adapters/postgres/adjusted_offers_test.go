package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/postgres"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
)

// adjustedEnv is an inbox whose case a-1 was decided with an adjust, so its
// adjusted offer may be stored.
func adjustedEnv(t *testing.T) (*inboxEnv, *postgres.AdjustedOfferStore) {
	t.Helper()
	e := inbox3(t)
	_, err := e.reviews.RecordDecision(context.Background(), humanDecision("a-1", domain.DecisionAdjust))
	require.NoError(t, err)
	return e, postgres.NewAdjustedOfferStore(e.db)
}

// adjustedOffer is the offer of the decision of a-1, of its customer.
func adjustedOffer() domain.Offer {
	req := routedRequest("a-1", domain.ReviewReasonProvisionalOffer, routedAt)
	return domain.Offer{
		ID: "adj-a-1", CustomerID: req.CustomerID, Applicant: req.Applicant, Premium: 154_800.56, Currency: "COP",
		Mode: req.OfferMode, ProfileScore: req.ProfileScore, ExpiresAt: routedAt.Add(25 * time.Hour), Terms: caseTerms(),
	}
}

// CF-3 / D-5 a: the adjusted offer round-trips with its terms.
func TestAdjustedOfferStoreRoundTripsTheOffer(t *testing.T) {
	_, s := adjustedEnv(t)
	ctx := context.Background()
	o := adjustedOffer()

	require.NoError(t, s.Create(ctx, o))

	got, err := s.Get(ctx, "adj-a-1")
	require.NoError(t, err)
	want := o
	want.ExpiresAt = micro(want.ExpiresAt)
	assert.Equal(t, want, got)
}

func TestAdjustedOfferStoreKeepsAnOfferWithoutTerms(t *testing.T) {
	_, s := adjustedEnv(t)
	ctx := context.Background()
	o := adjustedOffer()
	o.Terms = domain.CoverageTerms{}

	require.NoError(t, s.Create(ctx, o))
	got, err := s.Get(ctx, "adj-a-1")
	require.NoError(t, err)
	assert.Equal(t, domain.CoverageTerms{}, got.Terms, "the adjusted offer of a demo offer carries no terms")
}

// CF-3: Create is idempotent and never overwrites the stored offer.
func TestAdjustedOfferStoreCreateIsIdempotentAndNeverOverwrites(t *testing.T) {
	_, s := adjustedEnv(t)
	ctx := context.Background()
	o := adjustedOffer()
	require.NoError(t, s.Create(ctx, o))

	changed := o
	changed.Premium = 1
	require.NoError(t, s.Create(ctx, changed), "creating it again is not an error")

	got, err := s.Get(ctx, "adj-a-1")
	require.NoError(t, err)
	assert.InDelta(t, 154_800.56, got.Premium, 1e-9, "the first offer is kept")
}

// CF-3 / B-1: there is no adjusted offer without the decision that created it,
// and CF-10: it belongs to the customer of the case, never to another one.
func TestAdjustedOfferStoreRefusesAnOfferWithoutItsDecisionOrOfAnotherCustomer(t *testing.T) {
	e, s := adjustedEnv(t)
	ctx := context.Background()

	noDecision := adjustedOffer()
	noDecision.ID, noDecision.CustomerID = "adj-a-2", "cust-a-2"
	assert.Error(t, s.Create(ctx, noDecision), "a-2 has no adjust decision")

	_, err := e.reviews.RecordDecision(ctx, humanDecision("a-3", domain.DecisionReject))
	require.NoError(t, err)
	rejected := adjustedOffer()
	rejected.ID, rejected.CustomerID = "adj-a-3", "cust-a-3"
	assert.Error(t, s.Create(ctx, rejected), "a reject creates no offer")

	advisors := adjustedOffer()
	advisors.CustomerID = "adv-1"
	assert.Error(t, s.Create(ctx, advisors), "the adjusted offer is never the advisor's")

	for _, id := range []string{"adj-a-2", "adj-a-3", "adj-a-1"} {
		_, err := s.Get(ctx, id)
		assert.ErrorIs(t, err, ports.ErrOfferNotFound, id)
	}
}

func TestAdjustedOfferTableIsImmutable(t *testing.T) {
	e, s := adjustedEnv(t)
	ctx := context.Background()
	require.NoError(t, s.Create(ctx, adjustedOffer()))

	for _, stmt := range []string{
		`UPDATE adjusted_offer SET premium_minor = 1 WHERE offer_id = 'adj-a-1'`,
		`DELETE FROM adjusted_offer WHERE offer_id = 'adj-a-1'`,
		`TRUNCATE adjusted_offer`,
	} {
		_, err := e.db.ExecContext(ctx, stmt)
		assert.Error(t, err, stmt)
	}
	got, err := s.Get(ctx, "adj-a-1")
	require.NoError(t, err)
	assert.InDelta(t, 154_800.56, got.Premium, 1e-9)
}

func TestAdjustedOfferStoreOnlyKnowsAdjustedIds(t *testing.T) {
	_, s := adjustedEnv(t)
	ctx := context.Background()
	o := adjustedOffer()
	for _, id := range []string{"demo-review-1", "off-1", "adj-", "a-1", "adj-a\x001"} {
		o.ID = id
		assert.Error(t, s.Create(ctx, o), "%q", id)
		_, err := s.Get(ctx, id)
		assert.ErrorIs(t, err, ports.ErrOfferNotFound, "%q", id)
	}
	_, err := s.Get(ctx, "adj-unknown")
	assert.ErrorIs(t, err, ports.ErrOfferNotFound)
}
