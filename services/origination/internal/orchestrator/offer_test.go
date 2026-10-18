package orchestrator_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/security"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/memory"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/orchestrator"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/rating"
)

var offerStart = time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)

type offerClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *offerClock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *offerClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

type scriptedProfiles struct {
	mu    sync.Mutex
	res   domain.ProfileResolution
	err   error
	calls int
}

func (f *scriptedProfiles) set(res domain.ProfileResolution, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.res, f.err = res, err
}

func (f *scriptedProfiles) Resolve(_ context.Context, id string, _ bool) (domain.ProfileResolution, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.err != nil {
		return domain.ProfileResolution{}, f.err
	}
	r := f.res
	r.Profile.CustomerID = id
	return r, nil
}

type scriptedConsent struct {
	mu     sync.Mutex
	status domain.ConsentStatus
	err    error
	calls  int
}

func (f *scriptedConsent) Verify(context.Context, string) (domain.ConsentStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return f.status, f.err
}

func freshProfile() domain.ProfileResolution {
	return domain.ProfileResolution{
		Profile: domain.RiskProfile{Score: 0.4, Category: "standard", CalculatedAt: offerStart.Add(-time.Minute)},
		Mode:    domain.ModeNormal, Source: domain.SourceExternal,
	}
}

func baseProfile() domain.ProfileResolution {
	return domain.ProfileResolution{
		Profile: domain.BaseActuarialProfile("c"), Mode: domain.ModeProvisional,
		Source: domain.SourceBaseActuarial, UnavailableSource: "open_finance",
	}
}

func cachedProfile() domain.ProfileResolution {
	return domain.ProfileResolution{
		Profile: domain.RiskProfile{Score: 0.3, Category: "standard", CalculatedAt: offerStart.Add(-48 * time.Hour), Partial: true},
		Mode:    domain.ModeProvisional, Source: domain.SourceCache, UnavailableSource: "open_finance",
	}
}

type offerEnv struct {
	svc      *orchestrator.OfferService
	clock    *offerClock
	profiles *scriptedProfiles
	consent  *scriptedConsent
	store    *memory.PersonalizedOfferStore
	router   *memory.OfferRouter
}

func newOfferEnv() *offerEnv {
	e := &offerEnv{
		clock: &offerClock{t: offerStart}, profiles: &scriptedProfiles{res: freshProfile()},
		consent: &scriptedConsent{status: domain.ConsentStatus{Active: true, ConsentID: "cns-1"}},
		store:   memory.NewPersonalizedOfferStore(),
	}
	e.router = memory.NewOfferRouter(memory.NewOfferStub(e.clock), e.store)
	e.svc = orchestrator.NewOfferService(orchestrator.OfferDeps{
		Store: e.store, Offers: e.router, Consents: e.consent, Profiles: e.profiles, Rater: rating.New(), Clock: e.clock,
	})
	return e
}

func offerReq() domain.EmbeddedOfferRequest {
	return domain.EmbeddedOfferRequest{
		CustomerID: "cust-1", ConsentID: "cns-1", PartnerID: "partner-1", ApplicationID: "app-1",
		DocumentID: "1020304050", FullName: "Ana María Pérez", Email: "ana@example.com",
		BirthDate: "1985-03-14", InsuredAmount: 250_000_000, TermMonths: 120,
	}
}

func as(role security.Role, sub string) context.Context {
	return security.WithClaims(context.Background(), security.Claims{Subject: sub, Roles: []security.Role{role}})
}

func TestGenerateBuildsAndPersistsTheCompleteOffer(t *testing.T) {
	e := newOfferEnv()

	o, err := e.svc.Generate(context.Background(), offerReq())

	require.NoError(t, err)
	assert.Regexp(t, `^off-[0-9a-f]{32}$`, o.ID)
	assert.Equal(t, domain.ModeNormal, o.Mode)
	assert.Empty(t, o.ProvisionalReason)
	assert.Equal(t, offerStart, o.CreatedAt)
	assert.Equal(t, offerStart.Add(24*time.Hour), o.ExpiresAt)
	assert.Equal(t, 41, o.AgeAtQuote)
	assert.Equal(t, "cns-1", o.ConsentID)
	assert.Equal(t, rating.Version, o.RaterVersion)
	assert.Equal(t, 0.4, o.Profile.Score, "the profile applied is copied")
	assert.Equal(t, rating.Conditions(domain.ModeNormal), o.Conditions)
	assert.Len(t, o.Rules, 5)
	assert.Equal(t, o.Premium, rating.PremiumFromRules(250_000_000, o.Rules), "the stored rules explain the price")
	assert.Len(t, o.Terms.Coverages, 2)
	assert.Empty(t, o.OwnerID)
	stored, err := e.store.Get(context.Background(), o.ID)
	require.NoError(t, err)
	assert.Equal(t, o, stored)
}

func TestGeneratePriceMatchesTheRater(t *testing.T) {
	e := newOfferEnv()
	o, err := e.svc.Generate(context.Background(), offerReq())
	require.NoError(t, err)

	want, err := rating.New().Rate(context.Background(), ports.RatingInput{
		Age: 41, InsuredAmount: 250_000_000, TermMonths: 120, Profile: domain.RiskProfile{Score: 0.4},
	})

	require.NoError(t, err)
	assert.Equal(t, want.Premium, o.Premium)
}

func TestGenerateWithInvalidFieldsDoesNotAskAnything(t *testing.T) {
	e := newOfferEnv()
	req := offerReq()
	req.TermMonths = 1

	_, err := e.svc.Generate(context.Background(), req)

	var invalid *domain.ValidationError
	require.ErrorAs(t, err, &invalid)
	assert.Zero(t, e.consent.calls)
	assert.Zero(t, e.profiles.calls)
}

func TestGenerateConsentFailuresQueryNoSource(t *testing.T) {
	cases := []struct {
		name   string
		status domain.ConsentStatus
		err    error
		want   error
	}{
		{"inactive", domain.ConsentStatus{}, nil, orchestrator.ErrConsentRequired},
		{"unverifiable", domain.ConsentStatus{}, ports.ErrConsentUnverifiable, orchestrator.ErrConsentUnverifiable},
		{"another consent", domain.ConsentStatus{Active: true, ConsentID: "cns-other"}, nil, orchestrator.ErrConsentMismatch},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newOfferEnv()
			e.consent.status, e.consent.err = tc.status, tc.err

			_, err := e.svc.Generate(context.Background(), offerReq())

			assert.ErrorIs(t, err, tc.want)
			assert.Zero(t, e.profiles.calls, "no source and no cache")
		})
	}
}

func TestGenerateWithoutAVerifierFailsClosed(t *testing.T) {
	e := newOfferEnv()
	svc := orchestrator.NewOfferService(orchestrator.OfferDeps{
		Store: e.store, Offers: e.router, Profiles: e.profiles, Rater: rating.New(), Clock: e.clock,
	})

	_, err := svc.Generate(context.Background(), offerReq())

	assert.ErrorIs(t, err, orchestrator.ErrConsentUnverifiable)
	assert.Zero(t, e.profiles.calls)
}

func TestGenerateProvisionalWithTheBaseProfile(t *testing.T) {
	e := newOfferEnv()
	e.profiles.set(baseProfile(), nil)

	o, err := e.svc.Generate(context.Background(), offerReq())

	require.NoError(t, err)
	assert.Equal(t, domain.ModeProvisional, o.Mode)
	assert.Equal(t, "base_actuarial_profile", o.ProvisionalReason)
	assert.True(t, o.ProfileDate.IsZero())
	assert.Equal(t, "open_finance", o.UnavailableSource)
	assert.Contains(t, o.Conditions, "provisional_requote_when_source_restored")
}

func TestGenerateProvisionalWithTheCachedProfileSaysWhen(t *testing.T) {
	e := newOfferEnv()
	e.profiles.set(cachedProfile(), nil)

	o, err := e.svc.Generate(context.Background(), offerReq())

	require.NoError(t, err)
	assert.Equal(t, "cached_profile", o.ProvisionalReason)
	assert.Equal(t, offerStart.Add(-48*time.Hour), o.ProfileDate)
	assert.Equal(t, domain.SourceCache, o.ProfileSource)
}

func TestGeneratePropagatesInfrastructureFailures(t *testing.T) {
	e := newOfferEnv()
	e.profiles.set(domain.ProfileResolution{}, errors.New("boom"))

	_, err := e.svc.Generate(context.Background(), offerReq())

	assert.ErrorContains(t, err, "boom")
}

func generated(t *testing.T, e *offerEnv) domain.PersonalizedOffer {
	t.Helper()
	o, err := e.svc.Generate(context.Background(), offerReq())
	require.NoError(t, err)
	return o
}

func TestGetClaimsTheOfferForTheFirstInsured(t *testing.T) {
	e := newOfferEnv()
	o := generated(t, e)

	v, err := e.svc.Get(as(security.RoleInsured, "sub-a"), o.ID)

	require.NoError(t, err)
	assert.Equal(t, orchestrator.OfferKindPersonalized, v.Kind)
	assert.Equal(t, "sub-a", v.Personalized.OwnerID)
	assert.True(t, v.CanAccept)
	assert.Equal(t, domain.OfferActive, v.Status)
	acc, err := e.router.Get(context.Background(), o.ID)
	require.NoError(t, err)
	assert.Equal(t, "sub-a", acc.CustomerID, "the acceptance now sees the owner")
}

func TestGetByAnotherInsuredIsForbidden(t *testing.T) {
	e := newOfferEnv()
	o := generated(t, e)
	_, err := e.svc.Get(as(security.RoleInsured, "sub-a"), o.ID)
	require.NoError(t, err)

	_, err = e.svc.Get(as(security.RoleInsured, "sub-b"), o.ID)

	assert.ErrorIs(t, err, security.ErrForbidden)
}

func TestGetByStaffReadsWithoutClaiming(t *testing.T) {
	e := newOfferEnv()
	o := generated(t, e)

	for _, role := range []security.Role{security.RoleAdvisor, security.RoleOperations} {
		v, err := e.svc.Get(as(role, "staff"), o.ID)
		require.NoError(t, err)
		assert.Empty(t, v.Personalized.OwnerID)
	}
	got, _ := e.store.Get(context.Background(), o.ID)
	assert.Empty(t, got.OwnerID, "staff never claim")
}

func TestGetWithoutSessionIsUnauthenticated(t *testing.T) {
	e := newOfferEnv()
	o := generated(t, e)

	_, err := e.svc.Get(context.Background(), o.ID)

	assert.ErrorIs(t, err, security.ErrUnauthenticated)
}

func TestGetUnknownOffer(t *testing.T) {
	_, err := newOfferEnv().svc.Get(as(security.RoleInsured, "sub-a"), "off-nope")

	assert.ErrorIs(t, err, ports.ErrOfferNotFound)
}

func TestGetStatusFollowsTheClock(t *testing.T) {
	e := newOfferEnv()
	o := generated(t, e)
	ctx := as(security.RoleInsured, "sub-a")

	e.clock.advance(24*time.Hour - time.Nanosecond)
	v, _ := e.svc.Get(ctx, o.ID)
	assert.Equal(t, domain.OfferActive, v.Status)
	assert.False(t, v.CanRenew)

	e.clock.advance(time.Nanosecond)
	v, _ = e.svc.Get(ctx, o.ID)
	assert.Equal(t, domain.OfferExpired, v.Status)
	assert.True(t, v.CanRenew)
	assert.False(t, v.CanAccept)
}

func TestGetServesAdjustedAndDemoOffersWithAReducedView(t *testing.T) {
	e := newOfferEnv()
	ctx := as(security.RoleInsured, "sub-a")
	require.NoError(t, e.router.Create(ctx, domain.Offer{
		ID: "adj-acc-1", CustomerID: "sub-a", Premium: 120_000, Currency: "COP", Mode: domain.ModeNormal,
		ExpiresAt: offerStart.Add(time.Hour),
	}))

	adj, err := e.svc.Get(ctx, "adj-acc-1")
	require.NoError(t, err)
	assert.Equal(t, orchestrator.OfferKindAdjusted, adj.Kind)
	assert.Nil(t, adj.Personalized)
	assert.Equal(t, 120_000.0, adj.Reduced.Premium)
	assert.True(t, adj.CanAccept)
	assert.False(t, adj.CanRenew)

	demo, err := e.svc.Get(ctx, "demo-expired-1")
	require.NoError(t, err)
	assert.Equal(t, orchestrator.OfferKindDemo, demo.Kind)
	assert.Equal(t, domain.OfferExpired, demo.Status)
	assert.False(t, demo.CanAccept)

	_, err = e.svc.Get(as(security.RoleInsured, "sub-b"), "adj-acc-1")
	assert.ErrorIs(t, err, security.ErrForbidden, "owner is the customer id; no claim")
	_, err = e.svc.Get(as(security.RoleAdvisor, "adv"), "adj-acc-1")
	assert.NoError(t, err)
}

func TestRenewRequiresAnExpiredOffer(t *testing.T) {
	e := newOfferEnv()
	o := generated(t, e)
	ctx := as(security.RoleInsured, "sub-a")
	_, _ = e.svc.Get(ctx, o.ID)
	e.clock.advance(24*time.Hour - time.Nanosecond)

	_, _, err := e.svc.Renew(ctx, o.ID)

	var still *orchestrator.OfferStillValidError
	require.ErrorAs(t, err, &still)
	assert.Equal(t, o.ExpiresAt, still.ExpiresAt)
}

func TestRenewCreatesTheNewOfferWithTheSameCreditAndTheAgeRecalculated(t *testing.T) {
	e := newOfferEnv()
	o := generated(t, e)
	ctx := as(security.RoleInsured, "sub-a")
	_, _ = e.svc.Get(ctx, o.ID)
	e.clock.advance(24 * time.Hour)

	r, created, err := e.svc.Renew(ctx, o.ID)

	require.NoError(t, err)
	assert.True(t, created)
	assert.NotEqual(t, o.ID, r.ID)
	assert.Equal(t, o.ID, r.Renews)
	assert.Equal(t, "sub-a", r.OwnerID, "the renewal inherits the owner")
	assert.Equal(t, o.Terms.InsuredAmount, r.Terms.InsuredAmount)
	assert.Equal(t, o.Terms.TermMonths, r.Terms.TermMonths)
	assert.Equal(t, e.clock.Now().Add(24*time.Hour), r.ExpiresAt)
	assert.Equal(t, 2, e.consent.calls, "the consent was verified at generation and again at renewal")
	old, _ := e.svc.Get(ctx, o.ID)
	assert.Equal(t, r.ID, old.Personalized.RenewedBy)
	assert.False(t, old.CanRenew)
}

func TestRenewIsIdempotent(t *testing.T) {
	e := newOfferEnv()
	o := generated(t, e)
	ctx := as(security.RoleInsured, "sub-a")
	_, _ = e.svc.Get(ctx, o.ID)
	e.clock.advance(25 * time.Hour)
	first, _, err := e.svc.Renew(ctx, o.ID)
	require.NoError(t, err)

	again, created, err := e.svc.Renew(ctx, o.ID)

	require.NoError(t, err)
	assert.False(t, created)
	assert.Equal(t, first.ID, again.ID)
}

func TestRenewByAnotherInsuredOrAnUnclaimedOfferIsForbidden(t *testing.T) {
	e := newOfferEnv()
	o := generated(t, e)
	e.clock.advance(25 * time.Hour)

	_, _, err := e.svc.Renew(as(security.RoleInsured, "sub-a"), o.ID)
	assert.ErrorIs(t, err, security.ErrForbidden, "unclaimed: no owner matches")

	_, _ = e.store.Claim(context.Background(), o.ID, "sub-a")
	_, _, err = e.svc.Renew(as(security.RoleInsured, "sub-b"), o.ID)
	assert.ErrorIs(t, err, security.ErrForbidden)
}

func TestRenewOfASupersededOfferPointsToTheNewerOne(t *testing.T) {
	e := newOfferEnv()
	e.profiles.set(baseProfile(), nil)
	o := generated(t, e)
	ctx := as(security.RoleInsured, "sub-a")
	_, _ = e.svc.Get(ctx, o.ID)
	newer := o
	newer.ID = "off-newer"
	newer.Mode, newer.ProvisionalReason = domain.ModeNormal, ""
	newer.CreatedAt = offerStart.Add(time.Hour)
	_, err := e.store.Supersede(context.Background(), o.ID, newer)
	require.NoError(t, err)
	e.clock.advance(30 * time.Hour)

	_, _, err = e.svc.Renew(ctx, o.ID)

	var sup *orchestrator.OfferSupersededError
	require.ErrorAs(t, err, &sup)
	assert.Equal(t, "off-newer", sup.SupersededBy)
}

func TestRenewWithoutConsentIsRefusedAndNothingIsStored(t *testing.T) {
	e := newOfferEnv()
	o := generated(t, e)
	ctx := as(security.RoleInsured, "sub-a")
	_, _ = e.svc.Get(ctx, o.ID)
	e.clock.advance(25 * time.Hour)
	e.consent.status = domain.ConsentStatus{}
	before := e.profiles.calls

	_, _, err := e.svc.Renew(ctx, o.ID)

	assert.ErrorIs(t, err, orchestrator.ErrConsentRequired)
	assert.Equal(t, before, e.profiles.calls)
}

func TestRenewWhenTheHolderIsNoLongerInsurable(t *testing.T) {
	e := newOfferEnv()
	req := offerReq()
	req.BirthDate = "1956-10-18" // exactly 70 today: insurable
	o, err := e.svc.Generate(context.Background(), req)
	require.NoError(t, err)
	ctx := as(security.RoleInsured, "sub-a")
	_, _ = e.svc.Get(ctx, o.ID)
	e.clock.advance(365 * 24 * time.Hour) // 2027-10-18: 71 years old

	_, _, err = e.svc.Renew(ctx, o.ID)

	var invalid *domain.ValidationError
	assert.ErrorAs(t, err, &invalid)
}

func TestRenewUnknownOffer(t *testing.T) {
	_, _, err := newOfferEnv().svc.Renew(as(security.RoleInsured, "sub-a"), "off-nope")

	assert.ErrorIs(t, err, ports.ErrOfferNotFound)
}
