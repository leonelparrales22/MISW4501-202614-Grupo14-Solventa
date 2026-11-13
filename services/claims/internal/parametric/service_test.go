package parametric_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/parametric"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/ports"
)

// HU-M2.4-01: the parametric engine resolves the observations of its queue.

var now = time.Date(2026, 9, 11, 21, 39, 30, 0, time.UTC)

type clock struct{}

func (clock) Now() time.Time { return now }

type fakeTerms struct {
	terms     domain.PolicyTerms
	holder    domain.Holder
	termsErr  error
	holderErr error
	gotPolicy string
}

func (f *fakeTerms) PolicyTerms(_ context.Context, _, policyID string) (domain.PolicyTerms, error) {
	f.gotPolicy = policyID
	return f.terms, f.termsErr
}

func (f *fakeTerms) Holder(context.Context, string) (domain.Holder, error) {
	return f.holder, f.holderErr
}

type fakeStore struct {
	resolved   []domain.ClaimRecord
	resolution domain.ParametricResolution
	decision   domain.Decision
	existing   *domain.ClaimRecord
	err        error
}

func (f *fakeStore) Resolve(_ context.Context, c domain.ClaimRecord, r domain.ParametricResolution,
	d domain.Decision) (domain.ClaimRecord, bool, error) {
	if f.err != nil {
		return domain.ClaimRecord{}, false, f.err
	}
	if f.existing != nil {
		return *f.existing, false, nil
	}
	c.FilingNumber = "SIN-2026-01702"
	f.resolved, f.resolution, f.decision = append(f.resolved, c), r, d
	return c, true, nil
}

var _ ports.ParametricStore = (*fakeStore)(nil)

func observation() domain.Observation {
	return domain.Observation{ID: "obs-1", Subject: "sub-a", PolicyID: "VJ-2026-000931",
		Parameter: domain.IncidentFlightDelay, EventReference: "AV-8421", Observed: 215, Unit: "minutes",
		ObservedAt: now.Add(-30 * time.Second)}
}

func travel() *fakeTerms {
	return &fakeTerms{
		terms: domain.PolicyTerms{
			Policy: domain.Policy{ID: "VJ-2026-000931", Line: domain.LineTravel, Status: domain.PolicyActive,
				ValidFrom: now.AddDate(0, -1, 0), ValidTo: now.AddDate(0, 11, 0)},
			Coverages: []domain.Coverage{{Incident: domain.IncidentFlightDelay, InsuredValueMinor: 500_000_000, Currency: "COP"}},
		},
		holder: domain.Holder{PayoutAccount: domain.PayoutAccount{BankCode: "1007", BankName: "Banco Aliado",
			AccountType: "savings", AccountNumber: "0123-4567-12"}},
	}
}

func engine(terms *fakeTerms, store *fakeStore) (*parametric.Service, *int) {
	woken := 0
	return parametric.NewService(terms, store, domain.DefaultParametricRules(), clock{}, func() { woken++ }), &woken
}

func TestAnObservationPastTheThresholdResolvesAClaimAndWakesThePayout(t *testing.T) {
	terms, store := travel(), &fakeStore{}
	svc, woken := engine(terms, store)

	res, err := svc.Observe(context.Background(), observation())
	require.NoError(t, err)

	assert.Equal(t, parametric.OutcomeResolved, res.Outcome)
	assert.Equal(t, "SIN-2026-01702", res.Claim.FilingNumber)
	assert.Equal(t, "VJ-2026-000931", terms.gotPolicy)
	require.Len(t, store.resolved, 1)
	assert.NotEmpty(t, store.resolved[0].ID)
	assert.Equal(t, store.resolved[0].ID, store.decision.ClaimID)
	assert.Equal(t, "obs-1", store.resolution.ObservationID)
	assert.Equal(t, int64(18_000_000), store.decision.Payout.AmountMinor)
	assert.Equal(t, 1, *woken)
}

func TestTheSameObservationAgainResolvesNothingNew(t *testing.T) {
	existing := domain.ClaimRecord{Claim: domain.Claim{ID: "cl-1", FilingNumber: "SIN-2026-01702"}}
	svc, woken := engine(travel(), &fakeStore{existing: &existing})

	res, err := svc.Observe(context.Background(), observation())
	require.NoError(t, err)
	assert.Equal(t, parametric.OutcomeDuplicate, res.Outcome)
	assert.Equal(t, "cl-1", res.Claim.ID)
	assert.Zero(t, *woken)
}

func TestAnObservationThatDoesNotPayIsIgnoredWithoutError(t *testing.T) {
	below := observation()
	below.Observed = 120
	store := &fakeStore{}
	svc, woken := engine(travel(), store)

	res, err := svc.Observe(context.Background(), below)
	require.NoError(t, err)
	assert.Equal(t, parametric.OutcomeIgnored, res.Outcome)
	assert.ErrorIs(t, res.Reason, domain.ErrBelowThreshold)
	assert.Empty(t, store.resolved)
	assert.Zero(t, *woken)
}

func TestAPolicyOrInsuredTheCoreDoesNotKnowIsIgnored(t *testing.T) {
	for name, terms := range map[string]*fakeTerms{
		"policy": {termsErr: ports.ErrPolicyNotFound},
		"holder": {terms: travel().terms, holderErr: ports.ErrHolderNotFound},
	} {
		t.Run(name, func(t *testing.T) {
			svc, _ := engine(terms, &fakeStore{})
			res, err := svc.Observe(context.Background(), observation())
			require.NoError(t, err)
			assert.Equal(t, parametric.OutcomeIgnored, res.Outcome)
		})
	}
}

func TestACoreThatDoesNotAnswerIsRetried(t *testing.T) {
	svc, _ := engine(&fakeTerms{termsErr: ports.ErrPolicyDirectoryUnavailable}, &fakeStore{})
	_, err := svc.Observe(context.Background(), observation())
	assert.ErrorIs(t, err, parametric.ErrPolicyUnavailable)
}

func TestAMalformedObservationIsAnError(t *testing.T) {
	o := observation()
	o.Subject = ""
	terms := travel()
	svc, _ := engine(terms, &fakeStore{})
	_, err := svc.Observe(context.Background(), o)
	assert.ErrorIs(t, err, domain.ErrInvalidObservation)
	assert.Empty(t, terms.gotPolicy, "the core is not asked")
}

func TestAStoreFailureIsRetried(t *testing.T) {
	svc, woken := engine(travel(), &fakeStore{err: errors.New("database down")})
	_, err := svc.Observe(context.Background(), observation())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "obs-1")
	assert.Zero(t, *woken)
}
