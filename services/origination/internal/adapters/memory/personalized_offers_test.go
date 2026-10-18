package memory_test

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/security"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/memory"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
)

var offerT0 = time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)

type offerFixedClock struct{ t time.Time }

func (c offerFixedClock) Now() time.Time { return c.t }

func personalized(id string, mode domain.OfferMode, created time.Time) domain.PersonalizedOffer {
	o := domain.PersonalizedOffer{
		ID: id, CustomerID: "cust-1", Channel: domain.OfferChannelEmbedded, ConsentID: "cns-1", PartnerID: "p", ApplicationID: "a",
		Applicant: domain.Applicant{FullName: "Ana Pérez", Email: "ana@example.com"}, Premium: 98_400, Currency: "COP",
		Mode: mode, CreatedAt: created, ExpiresAt: created.Add(domain.OfferValidity),
	}
	if mode == domain.ModeProvisional {
		o.ProvisionalReason = domain.ProvisionalBaseProfile
	}
	return o
}

func TestPersonalizedStoreCreateIsIdempotentById(t *testing.T) {
	s := memory.NewPersonalizedOfferStore()
	ctx := context.Background()
	first, created, err := s.Create(ctx, personalized("off-1", domain.ModeNormal, offerT0))
	require.NoError(t, err)
	require.True(t, created)

	other := personalized("off-1", domain.ModeNormal, offerT0)
	other.Premium = 1
	again, created, err := s.Create(ctx, other)

	require.NoError(t, err)
	assert.False(t, created)
	assert.Equal(t, first, again, "an offer is never overwritten")
}

func TestPersonalizedStoreGetUnknown(t *testing.T) {
	_, err := memory.NewPersonalizedOfferStore().Get(context.Background(), "off-x")

	assert.ErrorIs(t, err, ports.ErrOfferNotFound)
}

func TestPersonalizedStoreClaimKeepsTheFirstOwner(t *testing.T) {
	s := memory.NewPersonalizedOfferStore()
	ctx := context.Background()
	_, _, _ = s.Create(ctx, personalized("off-1", domain.ModeNormal, offerT0))

	first, err := s.Claim(ctx, "off-1", "sub-a")
	require.NoError(t, err)
	second, err := s.Claim(ctx, "off-1", "sub-b")
	require.NoError(t, err)

	assert.Equal(t, "sub-a", first.OwnerID)
	assert.Equal(t, "sub-a", second.OwnerID)
	got, _ := s.Get(ctx, "off-1")
	assert.Equal(t, "sub-a", got.OwnerID)
}

func TestPersonalizedStoreClaimOfAnUnknownOffer(t *testing.T) {
	_, err := memory.NewPersonalizedOfferStore().Claim(context.Background(), "off-x", "sub-a")

	assert.ErrorIs(t, err, ports.ErrOfferNotFound)
}

func TestPersonalizedStoreConcurrentClaimsHaveOneWinner(t *testing.T) {
	s := memory.NewPersonalizedOfferStore()
	ctx := context.Background()
	_, _, _ = s.Create(ctx, personalized("off-1", domain.ModeNormal, offerT0))

	var wg sync.WaitGroup
	winners := make([]string, 20)
	for i := range winners {
		wg.Add(1)
		go func() {
			defer wg.Done()
			o, err := s.Claim(ctx, "off-1", fmt.Sprintf("sub-%d", i))
			assert.NoError(t, err)
			winners[i] = o.OwnerID
		}()
	}
	wg.Wait()

	for _, w := range winners {
		assert.Equal(t, winners[0], w, "everybody sees the same owner")
	}
}

func TestPersonalizedStoreSupersedeLinksBothOffersAndInheritsTheOwner(t *testing.T) {
	s := memory.NewPersonalizedOfferStore()
	ctx := context.Background()
	_, _, _ = s.Create(ctx, personalized("off-1", domain.ModeProvisional, offerT0))
	_, _ = s.Claim(ctx, "off-1", "sub-a")

	created, err := s.Supersede(ctx, "off-1", personalized("off-2", domain.ModeNormal, offerT0.Add(time.Hour)))

	require.NoError(t, err)
	assert.True(t, created)
	old, _ := s.Get(ctx, "off-1")
	assert.Equal(t, "off-2", old.SupersededBy)
	assert.Equal(t, offerT0.Add(time.Hour), old.SupersededAt)
	assert.Equal(t, domain.OfferSuperseded, old.Status(offerT0.Add(time.Hour)))
	replacement, _ := s.Get(ctx, "off-2")
	assert.Equal(t, "off-1", replacement.Supersedes)
	assert.Equal(t, "sub-a", replacement.OwnerID)
}

func TestPersonalizedStoreAnOfferHasASingleReplacement(t *testing.T) {
	s := memory.NewPersonalizedOfferStore()
	ctx := context.Background()
	_, _, _ = s.Create(ctx, personalized("off-1", domain.ModeProvisional, offerT0))

	var created atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, err := s.Supersede(ctx, "off-1", personalized(fmt.Sprintf("off-new-%d", i), domain.ModeNormal, offerT0.Add(time.Hour)))
			assert.NoError(t, err)
			if ok {
				created.Add(1)
			}
		}()
	}
	wg.Wait()

	assert.EqualValues(t, 1, created.Load())
}

func TestPersonalizedStoreSupersedeOfAnUnknownOffer(t *testing.T) {
	_, err := memory.NewPersonalizedOfferStore().Supersede(context.Background(), "off-x", personalized("off-2", domain.ModeNormal, offerT0))

	assert.ErrorIs(t, err, ports.ErrOfferNotFound)
}

func TestPersonalizedStoreRenewReturnsTheExistingRenewal(t *testing.T) {
	s := memory.NewPersonalizedOfferStore()
	ctx := context.Background()
	_, _, _ = s.Create(ctx, personalized("off-1", domain.ModeNormal, offerT0))
	_, _ = s.Claim(ctx, "off-1", "sub-a")

	first, created, err := s.Renew(ctx, "off-1", personalized("off-2", domain.ModeNormal, offerT0.Add(25*time.Hour)))
	require.NoError(t, err)
	require.True(t, created)
	again, created, err := s.Renew(ctx, "off-1", personalized("off-3", domain.ModeNormal, offerT0.Add(26*time.Hour)))

	require.NoError(t, err)
	assert.False(t, created)
	assert.Equal(t, "off-2", again.ID)
	assert.Equal(t, "sub-a", first.OwnerID, "the renewal inherits the owner")
	assert.Equal(t, "off-1", first.Renews)
	old, _ := s.Get(ctx, "off-1")
	assert.Equal(t, "off-2", old.RenewedBy)
	_, err = s.Get(ctx, "off-3")
	assert.ErrorIs(t, err, ports.ErrOfferNotFound, "the losing renewal is not stored")
}

func TestPersonalizedStoreRenewOfAnUnknownOffer(t *testing.T) {
	_, _, err := memory.NewPersonalizedOfferStore().Renew(context.Background(), "off-x", personalized("off-2", domain.ModeNormal, offerT0))

	assert.ErrorIs(t, err, ports.ErrOfferNotFound)
}

func TestPersonalizedStoreListRequotable(t *testing.T) {
	s := memory.NewPersonalizedOfferStore()
	ctx := context.Background()
	now := offerT0.Add(time.Hour)
	_, _, _ = s.Create(ctx, personalized("off-normal", domain.ModeNormal, offerT0))
	_, _, _ = s.Create(ctx, personalized("off-prov-b", domain.ModeProvisional, offerT0.Add(time.Minute)))
	_, _, _ = s.Create(ctx, personalized("off-prov-a", domain.ModeProvisional, offerT0))
	_, _, _ = s.Create(ctx, personalized("off-prov-old", domain.ModeProvisional, offerT0.Add(-25*time.Hour)))
	_, _, _ = s.Create(ctx, personalized("off-prov-done", domain.ModeProvisional, offerT0.Add(2*time.Minute)))
	_, _ = s.Supersede(ctx, "off-prov-done", personalized("off-new", domain.ModeNormal, now))

	got, err := s.ListRequotable(ctx, now, 10)

	require.NoError(t, err)
	ids := make([]string, 0, len(got))
	for _, o := range got {
		ids = append(ids, o.ID)
	}
	assert.Equal(t, []string{"off-prov-a", "off-prov-b"}, ids, "only valid, provisional, not replaced; oldest first")
	limited, _ := s.ListRequotable(ctx, now, 1)
	assert.Len(t, limited, 1)
}

func TestPersonalizedStoreListRequotableAtTheExpiryInstant(t *testing.T) {
	s := memory.NewPersonalizedOfferStore()
	ctx := context.Background()
	o := personalized("off-prov", domain.ModeProvisional, offerT0)
	_, _, _ = s.Create(ctx, o)

	before, _ := s.ListRequotable(ctx, o.ExpiresAt.Add(-time.Nanosecond), 10)
	at, _ := s.ListRequotable(ctx, o.ExpiresAt, 10)

	assert.Len(t, before, 1)
	assert.Empty(t, at)
}

func TestOfferRouterServesPersonalizedOffersAsOffers(t *testing.T) {
	store := memory.NewPersonalizedOfferStore()
	router := memory.NewOfferRouter(memory.NewOfferStub(offerFixedClock{offerT0}), store)
	ctx := context.Background()
	_, _, _ = store.Create(ctx, personalized("off-1", domain.ModeNormal, offerT0))
	_, _ = store.Claim(ctx, "off-1", "sub-a")

	got, err := router.Get(ctx, "off-1")

	require.NoError(t, err)
	assert.Equal(t, "sub-a", got.CustomerID, "the owner is the subject the acceptance checks")
	assert.Equal(t, 98_400.0, got.Premium)
	assert.Equal(t, offerT0.Add(domain.OfferValidity), got.ExpiresAt)
}

func TestOfferRouterUnknownPersonalizedOffer(t *testing.T) {
	router := memory.NewOfferRouter(memory.NewOfferStub(offerFixedClock{offerT0}), memory.NewPersonalizedOfferStore())

	_, err := router.Get(context.Background(), "off-x")

	assert.ErrorIs(t, err, ports.ErrOfferNotFound)
}

func TestOfferRouterDelegatesDemoAndAdjustedOffersToTheStub(t *testing.T) {
	router := memory.NewOfferRouter(memory.NewOfferStub(offerFixedClock{offerT0}), memory.NewPersonalizedOfferStore())
	ctx := security.WithClaims(context.Background(), security.Claims{Subject: "sub-a", Roles: []security.Role{security.RoleInsured}})

	demo, err := router.Get(ctx, "demo-active-001")
	require.NoError(t, err)
	assert.Equal(t, "sub-a", demo.CustomerID)

	require.NoError(t, router.Create(ctx, domain.Offer{ID: "adj-acc-1", CustomerID: "sub-a", Premium: 120_000, ExpiresAt: offerT0.Add(time.Hour)}))
	adjusted, err := router.Get(ctx, "adj-acc-1")
	require.NoError(t, err)
	assert.Equal(t, 120_000.0, adjusted.Premium)
}

func TestIsStubOffer(t *testing.T) {
	assert.True(t, memory.IsStubOffer("demo-active-1"))
	assert.True(t, memory.IsStubOffer("adj-1"))
	assert.False(t, memory.IsStubOffer("off-1"))
	assert.False(t, memory.IsStubOffer(""))
}
