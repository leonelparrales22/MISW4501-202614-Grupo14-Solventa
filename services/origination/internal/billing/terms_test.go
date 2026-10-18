package billing_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/resilience"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/billing"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
)

func demoCoverage() domain.CoverageTerms {
	terms, _ := demoTerms(domain.Offer{})
	return terms
}

func personalizedTerms() domain.CoverageTerms {
	return domain.CoverageTerms{InsuredAmount: 180_000_000, TermMonths: 60, Coverages: []domain.Coverage{
		{Code: domain.CoverageDeath, InsuredAmount: 180_000_000},
		{Code: domain.CoverageDisability, InsuredAmount: 90_000_000},
	}}
}

func TestOfferTermsOfAPersonalizedOfferAreItsOwn(t *testing.T) {
	o := offer("acc-1")
	o.ID, o.Terms = "off-0123456789abcdef", personalizedTerms()

	got, err := billing.OfferTerms(demoCoverage())(o)

	require.NoError(t, err)
	assert.Equal(t, personalizedTerms(), got)
	got.Coverages[0].InsuredAmount = 1
	assert.InDelta(t, 180_000_000, o.Terms.Coverages[0].InsuredAmount, 0.001, "the offer is never changed through its terms")
}

func TestOfferTermsOfTheDemoAndAdjustedOffersAreTheDemonstrationOnes(t *testing.T) {
	terms := billing.OfferTerms(demoCoverage())
	for _, id := range []string{"demo-active-001", "adj-acc-1", "legacy-1"} {
		o := offer("acc-1")
		o.ID = id
		if id == "legacy-1" {
			o.Terms = personalizedTerms() // only a personalized offer is trusted with its own terms
		}

		got, err := terms(o)

		require.NoError(t, err, id)
		assert.Equal(t, demoCoverage(), got, id)
	}
	got, _ := terms(offer("acc-1"))
	got.Coverages[0].Code = "changed"
	again, _ := terms(offer("acc-1"))
	assert.Equal(t, domain.CoverageDeath, again.Coverages[0].Code, "every call gets its own copy")
}

func TestOfferTermsRefuseAPersonalizedOfferWithIncompleteTerms(t *testing.T) {
	cases := map[string]func(*domain.CoverageTerms){
		"no terms at all":              func(t *domain.CoverageTerms) { *t = domain.CoverageTerms{} },
		"no insured amount":            func(t *domain.CoverageTerms) { t.InsuredAmount = 0 },
		"no term":                      func(t *domain.CoverageTerms) { t.TermMonths = 0 },
		"no coverages":                 func(t *domain.CoverageTerms) { t.Coverages = nil },
		"a coverage without a code":    func(t *domain.CoverageTerms) { t.Coverages[1].Code = "" },
		"a coverage without an amount": func(t *domain.CoverageTerms) { t.Coverages[0].InsuredAmount = 0 },
	}
	for name, spoil := range cases {
		t.Run(name, func(t *testing.T) {
			o := offer("acc-1")
			o.ID, o.Terms = "off-0123456789abcdef", personalizedTerms()
			spoil(&o.Terms)

			_, err := billing.OfferTerms(demoCoverage())(o)

			assert.ErrorIs(t, err, billing.ErrIncompleteTerms)
			assert.Contains(t, err.Error(), "off-0123456789abcdef")
		})
	}
}

func TestStartWithAPersonalizedOfferSnapshotsItsTerms(t *testing.T) {
	repo := newMemRepo()
	s := billing.NewStarter(repo, billing.OfferTerms(demoCoverage()), &clock{now: t0}, resilience.DefaultRetryPolicy(), nil)
	o := offer("acc-1")
	o.ID, o.Terms = "off-0123456789abcdef", personalizedTerms()

	require.NoError(t, s.Start(context.Background(), acceptance("acc-1"), o))

	c := repo.get("acc-1")
	assert.Equal(t, personalizedTerms(), c.Terms)
	confirmed := c.Confirmed(t0, "ch-1")
	ev := confirmed.ActivationEvent()
	assert.InDelta(t, 180_000_000, ev.InsuredAmount, 0.001)
	assert.Equal(t, t0.AddDate(0, 60, 0), ev.CoverageEnd, "the term of the offer, not the 120 demonstration months")
}

func TestStartWithAnIncompletePersonalizedOfferStoresNothing(t *testing.T) {
	repo := newMemRepo()
	s := billing.NewStarter(repo, billing.OfferTerms(demoCoverage()), &clock{now: t0}, resilience.DefaultRetryPolicy(), nil)
	o := offer("acc-1")
	o.ID = "off-0123456789abcdef"

	err := s.Start(context.Background(), acceptance("acc-1"), o)

	assert.ErrorIs(t, err, billing.ErrIncompleteTerms)
	assert.Empty(t, repo.rows, "nothing is charged that cannot be activated with the real terms")
}
