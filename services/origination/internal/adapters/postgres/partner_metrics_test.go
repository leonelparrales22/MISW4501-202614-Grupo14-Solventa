package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/postgres"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
)

// HU-W3.2-01: the counts of the summary of the dashboard of a partner.

// metricsDay is 16 September 2026 in Colombia: [05:00 UTC of the 16th, 05:00 UTC of the 17th).
var (
	metricsStart = time.Date(2026, 9, 16, 5, 0, 0, 0, time.UTC)
	metricsEnd   = metricsStart.Add(24 * time.Hour)
)

type metricsFixture struct {
	store       *postgres.PartnerActivityStore
	offers      *postgres.OfferStore
	acceptances *postgres.AcceptanceStore
	policies    *postgres.PolicyStore
}

func newMetricsFixture(t *testing.T) *metricsFixture {
	t.Helper()
	requireOfferDocker(t)
	db := newDB(t)
	return &metricsFixture{store: postgres.NewPartnerActivityStore(db), offers: postgres.NewOfferStore(db),
		acceptances: postgres.NewAcceptanceStore(db), policies: postgres.NewPolicyStore(db)}
}

// offer stores an embedded offer of partner created at created.
func (f *metricsFixture) offer(t *testing.T, id, partner string, created time.Time) domain.PersonalizedOffer {
	t.Helper()
	o := storedOffer(id, domain.ModeNormal, created)
	o.PartnerID = partner
	_, isNew, err := f.offers.Create(context.Background(), o)
	require.NoError(t, err)
	require.True(t, isNew)
	return o
}

// issue accepts the offer and stores the policy activated at issued.
func (f *metricsFixture) issue(t *testing.T, o domain.PersonalizedOffer, issued time.Time, renewalOf string) {
	t.Helper()
	acc := "acc-" + o.ID
	_, _, err := f.acceptances.Create(context.Background(), registered(acc, o.ID, issued.Add(-time.Hour)))
	require.NoError(t, err)
	p := samplePolicy("pol-"+acc, "cust-1", issued.AddDate(1, 0, 0))
	p.AcceptanceID, p.CoverageStart, p.RenewalOf = acc, issued, renewalOf
	require.NoError(t, f.policies.Create(context.Background(), p))
}

func (f *metricsFixture) count(t *testing.T, partner string) domain.PartnerActivity {
	t.Helper()
	a, err := f.store.CountPartnerActivity(context.Background(), partner, metricsStart, metricsEnd)
	require.NoError(t, err)
	return a
}

func TestPartnerActivityCountsTheQuotesAndPoliciesOfTheRange(t *testing.T) {
	f := newMetricsFixture(t)
	in := metricsStart.Add(15 * time.Hour)

	issued := f.offer(t, "off-1", "socio-a", metricsStart)
	f.offer(t, "off-2", "socio-a", in)
	f.offer(t, "off-3", "socio-a", metricsEnd.Add(-time.Microsecond))
	f.issue(t, issued, in, "")

	assert.Equal(t, domain.PartnerActivity{QuotesRequested: 3, PoliciesIssued: 1}, f.count(t, "socio-a"),
		"the first and the last instant of the day count")
}

func TestPartnerActivityLeavesOutWhatIsOutsideTheRange(t *testing.T) {
	f := newMetricsFixture(t)

	before := f.offer(t, "off-before", "socio-a", metricsStart.Add(-time.Microsecond))
	f.offer(t, "off-after", "socio-a", metricsEnd)
	// A quote of the day before issued today counts as a policy of today.
	f.issue(t, before, metricsStart.Add(2*time.Hour), "")
	late := f.offer(t, "off-today", "socio-a", metricsStart.Add(time.Hour))
	f.issue(t, late, metricsEnd.Add(time.Hour), "")

	assert.Equal(t, domain.PartnerActivity{QuotesRequested: 1, PoliciesIssued: 1}, f.count(t, "socio-a"))
}

func TestPartnerActivityNeverCountsAnotherPartner(t *testing.T) {
	f := newMetricsFixture(t)
	at := metricsStart.Add(3 * time.Hour)

	mine := f.offer(t, "off-a", "socio-a", at)
	theirs := f.offer(t, "off-b", "socio-b", at)
	f.offer(t, "off-b2", "socio-b", at)
	f.issue(t, mine, at.Add(time.Hour), "")
	f.issue(t, theirs, at.Add(time.Hour), "")

	assert.Equal(t, domain.PartnerActivity{QuotesRequested: 1, PoliciesIssued: 1}, f.count(t, "socio-a"), "CA-4")
	assert.Equal(t, domain.PartnerActivity{QuotesRequested: 2, PoliciesIssued: 1}, f.count(t, "socio-b"))
	assert.Equal(t, domain.PartnerActivity{}, f.count(t, "socio-c"), "CA-3: a partner without activity has zeros")
}

func TestPartnerActivityLeavesOutTheAdjustedOffersAndTheRenewals(t *testing.T) {
	f := newMetricsFixture(t)
	at := metricsStart.Add(3 * time.Hour)
	ctx := context.Background()

	original := f.offer(t, "off-1", "socio-a", at)
	adjusted := storedOffer("off-1-adjusted", domain.ModeNormal, at.Add(time.Minute))
	adjusted.PartnerID = "socio-a"
	_, err := f.offers.Supersede(ctx, original.ID, adjusted)
	require.NoError(t, err)
	renewed := storedOffer("off-1-renewed", domain.ModeNormal, at.Add(2*time.Minute))
	renewed.PartnerID = "socio-a"
	_, _, err = f.offers.Renew(ctx, original.ID, renewed)
	require.NoError(t, err)
	f.issue(t, original, at.Add(time.Hour), "pol-old")

	assert.Equal(t, domain.PartnerActivity{QuotesRequested: 1, PoliciesIssued: 0}, f.count(t, "socio-a"),
		"one request of the partner; the renewed policy is not a new one")
}
