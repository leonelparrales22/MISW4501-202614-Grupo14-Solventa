package renewal_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/renewal"
)

var (
	embeddedBirth = time.Date(1985, 3, 14, 0, 0, 0, 0, time.UTC)
	assistedBirth = time.Date(1979, 7, 2, 0, 0, 0, 0, time.UTC)
)

type originEnv struct {
	policies *fakePolicies
	charges  *fakeCharges
	offers   fakeOffers
	subject  fakeCustomers
	document fakeCustomersByDocument
}

func newOriginEnv() *originEnv {
	e := &originEnv{
		policies: newFakePolicies(), charges: newFakeCharges(),
		offers: fakeOffers{rows: map[string]domain.PersonalizedOffer{
			"off-embedded": {ID: "off-embedded", CustomerID: "partner-cust-1", Channel: domain.OfferChannelEmbedded, BirthDate: embeddedBirth},
			"off-q-q1":     {ID: "off-q-q1", CustomerID: "cus-q1", Channel: domain.OfferChannelAssisted, BirthDate: assistedBirth},
		}},
		subject:  fakeCustomers{err: ports.ErrCustomerNotFound},
		document: fakeCustomersByDocument{rows: map[string]domain.Customer{}},
	}
	e.charges.rows["acc-embedded"] = domain.Charge{AcceptanceID: "acc-embedded", OfferID: "off-embedded"}
	e.charges.rows["acc-assisted"] = domain.Charge{AcceptanceID: "acc-assisted", OfferID: "off-q-q1"}
	e.charges.rows["acc-demo"] = domain.Charge{AcceptanceID: "acc-demo", OfferID: "demo-1"}
	return e
}

func (e *originEnv) finder() renewal.OriginFinder {
	return renewal.OriginFinder{Policies: e.policies, Charges: e.charges, Offers: e.offers, BySubject: e.subject, ByDocument: e.document}
}

func policyBoughtWith(id, acceptanceID string) domain.Policy {
	end := time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)
	return domain.Policy{ID: id, AcceptanceID: acceptanceID, CustomerID: "sub-1",
		Holder:  domain.Applicant{FullName: "Carolina Pérez", DocumentID: "1032456789", Email: "carolina@example.com"},
		Premium: 142_300, Currency: "COP", InsuredAmount: 68_000_000,
		Coverages:     []domain.Coverage{{Code: domain.CoverageDeath, InsuredAmount: 68_000_000}},
		CoverageStart: end.AddDate(-1, 0, 0), CoverageEnd: end}
}

func TestTheOriginOfAnEmbeddedPolicyIsItsOffer(t *testing.T) {
	e := newOriginEnv()

	got, err := e.finder().Find(context.Background(), policyBoughtWith("pol-1", "acc-embedded"))

	require.NoError(t, err)
	assert.Equal(t, renewal.Origin{CustomerID: "partner-cust-1", BirthDate: embeddedBirth, Channel: domain.OfferChannelEmbedded,
		OfferID: "off-embedded"}, got, "the partner's customer id keys the profile and the consent")
}

func TestTheOriginOfAnAssistedPolicyIsTheOfferOfItsQuote(t *testing.T) {
	e := newOriginEnv()

	got, err := e.finder().Find(context.Background(), policyBoughtWith("pol-1", "acc-assisted"))

	require.NoError(t, err)
	assert.Equal(t, domain.OfferChannelAssisted, got.Channel)
	assert.Equal(t, "cus-q1", got.CustomerID)
	assert.Equal(t, assistedBirth, got.BirthDate)
}

func TestTheOriginOfARenewedPolicyIsTheOneOfThePolicyItRenews(t *testing.T) {
	e := newOriginEnv()
	first := policyBoughtWith("pol-1", "acc-embedded")
	second := policyBoughtWith("pol-2", "ren-pol-1-20261201")
	second.RenewalOf = "pol-1"
	third := policyBoughtWith("pol-3", "ren-pol-2-20271201")
	third.RenewalOf = "pol-2"
	for _, p := range []domain.Policy{first, second, third} {
		require.NoError(t, e.policies.Create(context.Background(), p))
	}

	got, err := e.finder().Find(context.Background(), third)

	require.NoError(t, err)
	assert.Equal(t, "off-embedded", got.OfferID)
	assert.Equal(t, "partner-cust-1", got.CustomerID)
}

func TestWithoutTheOfferTheCustomerIsLookedForBySubjectThenByDocument(t *testing.T) {
	ctx := context.Background()
	demo := policyBoughtWith("pol-1", "acc-demo")

	e := newOriginEnv()
	e.subject = fakeCustomers{customer: domain.Customer{ID: "cus-by-sub", BirthDate: embeddedBirth}}
	got, err := e.finder().Find(ctx, demo)
	require.NoError(t, err)
	assert.Equal(t, renewal.Origin{CustomerID: "cus-by-sub", BirthDate: embeddedBirth}, got, "no channel: it is unknown")

	e = newOriginEnv()
	e.document.rows[demo.Holder.DocumentID] = domain.Customer{ID: "cus-by-doc", BirthDate: assistedBirth}
	got, err = e.finder().Find(ctx, demo)
	require.NoError(t, err)
	assert.Equal(t, renewal.Origin{CustomerID: "cus-by-doc", BirthDate: assistedBirth}, got)

	e = newOriginEnv()
	got, err = e.finder().Find(ctx, demo)
	require.NoError(t, err)
	assert.Equal(t, renewal.Origin{}, got, "nobody found: the renewal is not re-priced")
}

func TestAPolicyWithoutChargeOrWithABrokenChainFallsBack(t *testing.T) {
	ctx := context.Background()
	e := newOriginEnv()
	e.subject = fakeCustomers{customer: domain.Customer{ID: "cus-by-sub", BirthDate: embeddedBirth}}

	got, err := e.finder().Find(ctx, policyBoughtWith("pol-1", "acc-unknown"))
	require.NoError(t, err)
	assert.Equal(t, "cus-by-sub", got.CustomerID)

	orphan := policyBoughtWith("pol-2", "ren-x")
	orphan.RenewalOf = "pol-gone"
	got, err = e.finder().Find(ctx, orphan)
	require.NoError(t, err)
	assert.Equal(t, "cus-by-sub", got.CustomerID)

	got, err = renewal.OriginFinder{BySubject: e.subject}.Find(ctx, policyBoughtWith("pol-3", "acc-embedded"))
	require.NoError(t, err)
	assert.Equal(t, "cus-by-sub", got.CustomerID, "without the stores of the charges and offers it goes to the fallback")
}

func TestAFailureReadingTheOriginIsReported(t *testing.T) {
	ctx := context.Background()

	e := newOriginEnv()
	e.offers.err = errBoom
	_, err := e.finder().Find(ctx, policyBoughtWith("pol-1", "acc-embedded"))
	assert.ErrorIs(t, err, errBoom)

	e = newOriginEnv()
	e.subject = fakeCustomers{err: errBoom}
	_, err = e.finder().Find(ctx, policyBoughtWith("pol-1", "acc-demo"))
	assert.ErrorIs(t, err, errBoom)

	e = newOriginEnv()
	e.document.err = errBoom
	_, err = e.finder().Find(ctx, policyBoughtWith("pol-1", "acc-demo"))
	assert.ErrorIs(t, err, errBoom)

	e = newOriginEnv()
	failing := &failingCharges{fakeCharges: e.charges}
	_, err = renewal.OriginFinder{Policies: e.policies, Charges: failing, Offers: e.offers}.Find(ctx, policyBoughtWith("pol-1", "acc-embedded"))
	assert.ErrorIs(t, err, errBoom)

	broken := policyBoughtWith("pol-2", "ren-x")
	broken.RenewalOf = "pol-1"
	_, err = renewal.OriginFinder{Policies: failingPolicies{}, Charges: e.charges, Offers: e.offers}.Find(ctx, broken)
	assert.ErrorIs(t, err, errBoom)
}

type failingCharges struct{ *fakeCharges }

func (failingCharges) Get(context.Context, string) (domain.Charge, error) {
	return domain.Charge{}, errBoom
}

type failingPolicies struct{ ports.PolicyRepository }

func (failingPolicies) Get(context.Context, string) (domain.Policy, error) {
	return domain.Policy{}, errBoom
}
