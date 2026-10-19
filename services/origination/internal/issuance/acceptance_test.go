package issuance_test

import (
	"context"
	"errors"
	"regexp"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/security"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/memory"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/issuance"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
)

var t0 = time.Date(2026, 10, 11, 15, 0, 0, 0, time.UTC)

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func as(subject string, roles ...security.Role) context.Context {
	return security.WithClaims(context.Background(), security.Claims{Subject: subject, Email: subject + "@example.com", Roles: roles})
}

func insured(subject string) context.Context { return as(subject, security.RoleInsured) }

// offerRepo is a fixed-offer repository.
type offerRepo map[string]domain.Offer

func (r offerRepo) Get(_ context.Context, id string) (domain.Offer, error) {
	o, ok := r[id]
	if !ok {
		return domain.Offer{}, ports.ErrOfferNotFound
	}
	return o, nil
}

func validOffer(id, owner string) domain.Offer {
	return domain.Offer{ID: id, CustomerID: owner, Premium: 1000, Currency: "COP", Mode: domain.ModeNormal, ProfileScore: 0.7, ExpiresAt: t0.Add(time.Hour)}
}

type serviceFixture struct {
	svc         *issuance.AcceptanceService
	acceptances *memory.AcceptanceStore
	clock       *fakeClock
	notified    int
}

func newServiceFixture(offers offerRepo) *serviceFixture {
	f := &serviceFixture{acceptances: memory.NewAcceptanceStore(), clock: &fakeClock{now: t0}}
	f.svc = issuance.NewAcceptanceService(offers, f.acceptances, f.clock, func() { f.notified++ })
	return f
}

var uuidV4 = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func TestAcceptPersistsTheAcceptanceBeforeAnyLaterStep(t *testing.T) {
	f := newServiceFixture(offerRepo{"o-1": validOffer("o-1", "cust-1")})

	a, created, err := f.svc.Accept(insured("cust-1"), "o-1", domain.ChannelWeb)

	require.NoError(t, err)
	assert.True(t, created)
	assert.Regexp(t, uuidV4, a.ID)
	assert.Equal(t, "o-1", a.OfferID)
	assert.Equal(t, "cust-1", a.CustomerID)
	assert.Equal(t, domain.ChannelWeb, a.Channel)
	assert.Equal(t, t0, a.AcceptedAt)
	assert.Equal(t, domain.AcceptanceRegistered, a.Status)
	assert.Nil(t, a.Decision, "underwriting runs after the acceptance is persisted")
	assert.Equal(t, t0, a.NextAttemptAt)
	assert.Equal(t, 1, f.notified)

	stored, err := f.acceptances.Get(context.Background(), a.ID)
	require.NoError(t, err)
	assert.Equal(t, a, stored)
}

func TestAcceptingTwiceReturnsTheSameAcceptance(t *testing.T) {
	f := newServiceFixture(offerRepo{"o-1": validOffer("o-1", "cust-1")})
	first, _, err := f.svc.Accept(insured("cust-1"), "o-1", domain.ChannelWeb)
	require.NoError(t, err)

	f.clock.Advance(2 * time.Hour) // the offer expired meanwhile
	again, created, err := f.svc.Accept(insured("cust-1"), "o-1", domain.ChannelMobile)

	require.NoError(t, err)
	assert.False(t, created)
	assert.Equal(t, first, again)
	assert.Equal(t, 1, f.notified)
}

func TestAcceptRejectsAnExpiredOffer(t *testing.T) {
	o := validOffer("o-1", "cust-1")
	o.ExpiresAt = t0
	f := newServiceFixture(offerRepo{"o-1": o})

	_, _, err := f.svc.Accept(insured("cust-1"), "o-1", domain.ChannelWeb)

	var expired *issuance.OfferExpiredError
	require.ErrorAs(t, err, &expired)
	assert.Equal(t, "o-1", expired.OfferID)
	assert.Equal(t, t0, expired.ExpiresAt)
	assert.Contains(t, err.Error(), "expired")
	_, err = f.acceptances.GetByOffer(context.Background(), "o-1")
	assert.ErrorIs(t, err, ports.ErrAcceptanceNotFound, "nothing is registered")
	assert.Zero(t, f.notified)
}

func TestAcceptChecksTheOfferAndItsOwner(t *testing.T) {
	f := newServiceFixture(offerRepo{"o-1": validOffer("o-1", "cust-1")})

	_, _, err := f.svc.Accept(insured("cust-1"), "missing", domain.ChannelWeb)
	assert.ErrorIs(t, err, ports.ErrOfferNotFound)

	_, _, err = f.svc.Accept(insured("cust-2"), "o-1", domain.ChannelWeb)
	assert.ErrorIs(t, err, security.ErrForbidden)

	_, _, err = f.svc.Accept(as("adv-1", security.RoleAdvisor), "o-1", domain.ChannelWeb)
	assert.ErrorIs(t, err, security.ErrForbidden, "only the owner accepts")

	_, _, err = f.svc.Accept(context.Background(), "o-1", domain.ChannelWeb)
	assert.ErrorIs(t, err, security.ErrUnauthenticated)
}

func TestReadsAreForTheOwnerAdvisorsAndOperations(t *testing.T) {
	f := newServiceFixture(offerRepo{"o-1": validOffer("o-1", "cust-1")})
	a, _, err := f.svc.Accept(insured("cust-1"), "o-1", domain.ChannelWeb)
	require.NoError(t, err)

	for _, ctx := range []context.Context{insured("cust-1"), as("adv", security.RoleAdvisor), as("ops", security.RoleOperations)} {
		got, err := f.svc.Get(ctx, a.ID)
		require.NoError(t, err)
		assert.Equal(t, a, got)
		got, err = f.svc.GetByOffer(ctx, "o-1")
		require.NoError(t, err)
		assert.Equal(t, a, got)
	}

	_, err = f.svc.Get(insured("cust-2"), a.ID)
	assert.ErrorIs(t, err, security.ErrForbidden)
	_, err = f.svc.GetByOffer(as("p", security.RolePartner), "o-1")
	assert.ErrorIs(t, err, security.ErrForbidden)
	_, err = f.svc.Get(insured("cust-1"), "missing")
	assert.ErrorIs(t, err, ports.ErrAcceptanceNotFound)
	_, err = f.svc.GetByOffer(insured("cust-1"), "missing")
	assert.ErrorIs(t, err, ports.ErrAcceptanceNotFound)
}

type failingAcceptances struct {
	*memory.AcceptanceStore
	getByOfferErr, createErr error
}

func (f failingAcceptances) GetByOffer(ctx context.Context, id string) (domain.Acceptance, error) {
	if f.getByOfferErr != nil {
		return domain.Acceptance{}, f.getByOfferErr
	}
	return f.AcceptanceStore.GetByOffer(ctx, id)
}

func (f failingAcceptances) Create(ctx context.Context, a domain.Acceptance) (domain.Acceptance, bool, error) {
	if f.createErr != nil {
		return domain.Acceptance{}, false, f.createErr
	}
	return f.AcceptanceStore.Create(ctx, a)
}

func TestAcceptPropagatesRepositoryFailures(t *testing.T) {
	boom := errors.New("db down")
	offers := offerRepo{"o-1": validOffer("o-1", "cust-1")}
	clock := &fakeClock{now: t0}

	svc := issuance.NewAcceptanceService(offers, failingAcceptances{AcceptanceStore: memory.NewAcceptanceStore(), getByOfferErr: boom}, clock, nil)
	_, _, err := svc.Accept(insured("cust-1"), "o-1", domain.ChannelWeb)
	assert.ErrorIs(t, err, boom)

	svc = issuance.NewAcceptanceService(offers, failingAcceptances{AcceptanceStore: memory.NewAcceptanceStore(), createErr: boom}, clock, nil)
	_, _, err = svc.Accept(insured("cust-1"), "o-1", domain.ChannelWeb)
	assert.ErrorIs(t, err, boom)
}
