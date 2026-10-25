package renewal_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/security"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/renewal"
)

var (
	t0        = time.Date(2026, 9, 20, 10, 0, 0, 0, time.UTC)
	policyEnd = time.Date(2026, 10, 2, 23, 59, 0, 0, time.UTC)
)

type rig struct {
	svc      *renewal.Service
	clock    *fakeClock
	policies *fakePolicies
	renewals *fakeRenewals
	charges  *fakeCharges
	rater    *fakeRater
	notifier *fakeNotifier
	woken    int
}

type rigOption func(*renewal.Config)

func newRig(t *testing.T, opts ...rigOption) *rig {
	t.Helper()
	r := &rig{clock: &fakeClock{now: t0}, policies: newFakePolicies(), renewals: newFakeRenewals(), charges: newFakeCharges(),
		rater: &fakeRater{premium: 150_000}, notifier: &fakeNotifier{}}
	r.policies.renewals = r.renewals
	cfg := renewal.Config{
		Policies: r.policies, Renewals: r.renewals, Charges: r.charges,
		Customers: fakeCustomers{customer: domain.Customer{ID: "cust-1", BirthDate: time.Date(1985, 5, 1, 0, 0, 0, 0, time.UTC)}},
		Profiles:  fakeProfiles{}, Rater: r.rater, Notifier: r.notifier, Rules: domain.DefaultRenewalPolicy, Clock: r.clock,
		Wake: func() { r.woken++ },
	}
	for _, o := range opts {
		o(&cfg)
	}
	var err error
	r.svc, err = renewal.NewService(cfg)
	require.NoError(t, err)
	return r
}

func insured(sub string) context.Context {
	return security.WithClaims(context.Background(), security.Claims{Subject: sub, Roles: []security.Role{security.RoleInsured}})
}

func (r *rig) seed(t *testing.T, id string, end time.Time) domain.Policy {
	t.Helper()
	p := domain.Policy{ID: id, AcceptanceID: "acc-" + id, CustomerID: "sub-1",
		Holder:  domain.Applicant{FullName: "Carolina Pérez", DocumentID: "1", Email: "carolina@example.com"},
		Premium: 142_300, Currency: "COP", InsuredAmount: 68_000_000,
		Coverages:     []domain.Coverage{{Code: domain.CoverageDeath, InsuredAmount: 68_000_000}},
		CoverageStart: end.AddDate(-1, 0, 0), CoverageEnd: end}
	require.NoError(t, r.policies.Create(context.Background(), p))
	r.charges.rows[p.AcceptanceID] = domain.Charge{AcceptanceID: p.AcceptanceID, Status: domain.ChargeConfirmed,
		PaymentMethod: domain.PaymentMethod{Kind: domain.PaymentCard, Token: "tok", Brand: "visa", Last4: "1111", Installments: 1}}
	return p
}

func TestNewServiceRequiresItsDependenciesAndValidRules(t *testing.T) {
	_, err := renewal.NewService(renewal.Config{})
	assert.Error(t, err)
	r := newRig(t)
	_, err = renewal.NewService(renewal.Config{Policies: r.policies, Renewals: r.renewals, Charges: r.charges, Rater: r.rater,
		Clock: r.clock, Rules: domain.RenewalPolicy{}})
	assert.Error(t, err, "the rules must be valid")
}

func TestConditionsRepriceThePolicyWithTheDiscountOfAClaimFreeYear(t *testing.T) {
	r := newRig(t)
	p := r.seed(t, "pol-1", policyEnd)

	c, err := r.svc.Conditions(insured("sub-1"), p.ID)

	require.NoError(t, err)
	assert.True(t, c.Requoted)
	assert.InDelta(t, domain.DiscountRate, c.DiscountRate, 1e-9)
	assert.InDelta(t, 138_000, c.Renewal.Premium, 0.01, "150.000 with 8 %% off, rounded to 100")
	assert.Equal(t, 41, r.rater.got.Age, "priced with the age of today")
	assert.Equal(t, 12, r.rater.got.TermMonths)
	assert.Equal(t, policyEnd, c.Renewal.CoverageStart)
	assert.Equal(t, policyEnd.AddDate(1, 0, 0), c.Renewal.CoverageEnd)
	require.NotNil(t, c.PaymentMethod)
	assert.Empty(t, c.PaymentMethod.Token, "the token never leaves the service")
}

func TestAClaimInTheLastYearEarnsNoDiscount(t *testing.T) {
	r := newRig(t, func(c *renewal.Config) { c.Claims = fakeClaims{n: 1} })
	p := r.seed(t, "pol-1", policyEnd)

	c, err := r.svc.Conditions(insured("sub-1"), p.ID)

	require.NoError(t, err)
	assert.Zero(t, c.DiscountRate)
	assert.InDelta(t, 150_000, c.Renewal.Premium, 0.01)
}

func TestWhenTheEngineCannotPriceTheRenewalRepeatsTheCurrentConditions(t *testing.T) {
	for name, opt := range map[string]rigOption{
		"no customer":  func(c *renewal.Config) { c.Customers = fakeCustomers{err: ports.ErrCustomerNotFound} },
		"lookup fails": func(c *renewal.Config) { c.Customers = fakeCustomers{err: errBoom} },
		"no profile":   func(c *renewal.Config) { c.Profiles = fakeProfiles{err: errBoom} },
		"rating fails": func(c *renewal.Config) { c.Rater = &fakeRater{err: errBoom} },
		"no customers": func(c *renewal.Config) { c.Customers = nil },
	} {
		t.Run(name, func(t *testing.T) {
			r := newRig(t, opt)
			p := r.seed(t, "pol-1", policyEnd)

			c, err := r.svc.Conditions(insured("sub-1"), p.ID)

			require.NoError(t, err, "the renewal is never blocked")
			assert.False(t, c.Requoted)
			assert.Zero(t, c.DiscountRate)
			assert.InDelta(t, p.Premium, c.Renewal.Premium, 0.01)
		})
	}
}

func TestOutsideTheWindowThereIsNothingToRenew(t *testing.T) {
	r := newRig(t)
	far := r.seed(t, "pol-far", policyEnd.AddDate(0, 3, 0))
	ended := r.seed(t, "pol-ended", t0.Add(-time.Hour))

	for _, p := range []domain.Policy{far, ended} {
		_, err := r.svc.Conditions(insured("sub-1"), p.ID)
		assert.ErrorIs(t, err, domain.ErrRenewalNotAvailable, p.ID)
		_, err = r.svc.Accept(insured("sub-1"), p.ID)
		assert.ErrorIs(t, err, domain.ErrRenewalNotAvailable, p.ID)
	}
	assert.Empty(t, r.charges.attached)
}

func TestOnlyTheOwnerReadsAndRenews(t *testing.T) {
	r := newRig(t)
	p := r.seed(t, "pol-1", policyEnd)
	other := security.WithClaims(context.Background(), security.Claims{Subject: "sub-2", Roles: []security.Role{security.RoleInsured}})
	staff := security.WithClaims(context.Background(), security.Claims{Subject: "ops", Roles: []security.Role{security.RoleOperations}})

	for _, ctx := range []context.Context{other, staff, context.Background()} {
		_, err := r.svc.Conditions(ctx, p.ID)
		assert.Error(t, err)
		_, err = r.svc.Accept(ctx, p.ID)
		assert.Error(t, err)
	}
	_, err := r.svc.Conditions(insured("sub-1"), "pol-unknown")
	assert.ErrorIs(t, err, ports.ErrPolicyNotFound)
}

func TestAcceptingStoresOneRenewalAndOneChargeWithTheRegisteredCard(t *testing.T) {
	r := newRig(t)
	p := r.seed(t, "pol-1", policyEnd)

	first, err := r.svc.Accept(insured("sub-1"), p.ID)
	require.NoError(t, err)
	second, err := r.svc.Accept(insured("sub-1"), p.ID)
	require.NoError(t, err)

	assert.True(t, first.Created)
	assert.False(t, second.Created, "accepting again finds the same renewal")
	assert.Equal(t, first.Renewal.ChargeID, second.Renewal.ChargeID)
	assert.Equal(t, domain.RenewalAccepted, first.Renewal.Status)
	assert.Equal(t, t0.Add(72*time.Hour), first.Renewal.RetryUntil)
	assert.Equal(t, []string{first.Renewal.ChargeID}, r.charges.attached, "the registered card is attached once")
	assert.Equal(t, 1, r.woken, "the charge worker is woken once")
	charge := r.charges.rows[first.Renewal.ChargeID]
	assert.Equal(t, domain.ChannelRenewal, charge.Channel)
	assert.Equal(t, p.ID, charge.RenewalOf)
	assert.Equal(t, policyEnd, charge.CoverageNotBefore)
}

func TestARenewalWhoseChargeWasNotStoredIsCompletedByAnotherAcceptance(t *testing.T) {
	r := newRig(t)
	p := r.seed(t, "pol-1", policyEnd)
	// A crash after the renewal was stored and before its charge.
	_, _, err := r.renewals.Create(context.Background(), domain.Renewal{
		ID: domain.RenewalID(p.ID, p.CoverageEnd), PolicyID: p.ID, CustomerID: "sub-1", PeriodEnd: p.CoverageEnd,
		Status: domain.RenewalAccepted, Premium: 138_000, Currency: "COP",
		Terms:    domain.CoverageTerms{InsuredAmount: p.InsuredAmount, TermMonths: 12, Coverages: p.Coverages},
		ChargeID: domain.RenewalID(p.ID, p.CoverageEnd), AcceptedAt: t0, RetryUntil: t0.Add(72 * time.Hour), CreatedAt: t0,
	})
	require.NoError(t, err)

	got, err := r.svc.Accept(insured("sub-1"), p.ID)

	require.NoError(t, err)
	assert.False(t, got.Created)
	assert.Contains(t, r.charges.rows, got.Renewal.ChargeID, "the charge exists now")
}

func TestARenewalIsRenewedOnceItsChargeIsConfirmed(t *testing.T) {
	r := newRig(t)
	p := r.seed(t, "pol-1", policyEnd)
	a, err := r.svc.Accept(insured("sub-1"), p.ID)
	require.NoError(t, err)

	renewal1, _, err := r.svc.Status(insured("sub-1"), p.ID)
	require.NoError(t, err)
	assert.Equal(t, domain.RenewalAccepted, renewal1.Status, "still being charged")

	r.charges.set(a.Renewal.ChargeID, func(c *domain.Charge) { c.Status, c.PolicyID = domain.ChargeConfirmed, "pol-acc-new" })
	renewal2, charge, err := r.svc.Status(insured("sub-1"), p.ID)

	require.NoError(t, err)
	assert.Equal(t, domain.RenewalRenewed, renewal2.Status)
	assert.Equal(t, "pol-acc-new", renewal2.NewPolicyID)
	assert.Equal(t, domain.ChargeConfirmed, charge.Status)
	_, err = r.svc.Accept(insured("sub-1"), p.ID)
	require.NoError(t, err, "accepting a renewed period answers the same renewal")
}

func TestAPaymentNotThroughAtTheDeadlineIsNotRenewed(t *testing.T) {
	r := newRig(t)
	p := r.seed(t, "pol-1", policyEnd)
	a, err := r.svc.Accept(insured("sub-1"), p.ID)
	require.NoError(t, err)
	r.charges.set(a.Renewal.ChargeID, func(c *domain.Charge) { c.Status, c.FailureReason = domain.ChargeFailed, domain.ChargePaymentRejected })

	got, _, err := r.svc.Status(insured("sub-1"), p.ID)
	require.NoError(t, err)
	assert.Equal(t, domain.RenewalAccepted, got.Status, "inside the deadline the insured can still fix the payment")

	r.clock.now = a.Renewal.RetryUntil
	got, _, err = r.svc.Status(insured("sub-1"), p.ID)
	require.NoError(t, err)
	assert.Equal(t, domain.RenewalNotRenewed, got.Status)
	assert.Equal(t, domain.NotRenewedPaymentFailed, got.Reason)
	_, err = r.svc.Accept(insured("sub-1"), p.ID)
	assert.ErrorIs(t, err, domain.ErrRenewalNotAvailable)
}

func TestAChargeStillBeingWorkedIsLeftToFinishPastTheDeadline(t *testing.T) {
	r := newRig(t)
	p := r.seed(t, "pol-1", policyEnd)
	a, err := r.svc.Accept(insured("sub-1"), p.ID)
	require.NoError(t, err)
	r.charges.set(a.Renewal.ChargeID, func(c *domain.Charge) { c.Status = domain.ChargeInProgress })
	r.clock.now = a.Renewal.RetryUntil.Add(time.Hour)

	got, _, err := r.svc.Status(insured("sub-1"), p.ID)

	require.NoError(t, err)
	assert.Equal(t, domain.RenewalAccepted, got.Status, "a charge in progress may still be confirmed")
}

func TestListPoliciesTellsWhichOnesCanBeRenewed(t *testing.T) {
	r := newRig(t)
	inside := r.seed(t, "pol-in", policyEnd)
	outside := r.seed(t, "pol-out", policyEnd.AddDate(1, 0, 0))
	_, err := r.svc.Accept(insured("sub-1"), inside.ID)
	require.NoError(t, err)
	require.NoError(t, r.policies.Create(context.Background(), domain.Policy{ID: "pol-else", CustomerID: "sub-2", CoverageEnd: policyEnd}))

	views, err := r.svc.ListPolicies(insured("sub-1"))

	require.NoError(t, err)
	require.Len(t, views, 2, "only the policies of the caller")
	byID := map[string]renewal.PolicyView{}
	for _, v := range views {
		byID[v.Policy.ID] = v
	}
	assert.False(t, byID[inside.ID].CanRenew, "already accepted")
	require.NotNil(t, byID[inside.ID].Renewal)
	assert.Equal(t, domain.RenewalAccepted, byID[inside.ID].Renewal.Status)
	assert.False(t, byID[outside.ID].CanRenew, "outside the window")
	assert.Equal(t, outside.CoverageEnd, byID[outside.ID].WindowClosesAt)

	_, err = r.svc.ListPolicies(context.Background())
	assert.ErrorIs(t, err, security.ErrUnauthenticated)
}

func TestTheSweepRecordsALapseAndNotifiesOnce(t *testing.T) {
	r := newRig(t)
	p := r.seed(t, "pol-1", policyEnd)
	r.clock.now = policyEnd.Add(time.Hour)

	n, err := r.svc.Sweep(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	n, err = r.svc.Sweep(context.Background())
	require.NoError(t, err)
	assert.Zero(t, n, "a lapse is recorded once")

	got, err := r.renewals.Get(context.Background(), p.ID, p.CoverageEnd)
	require.NoError(t, err)
	assert.Equal(t, domain.RenewalNotRenewed, got.Status)
	assert.Equal(t, domain.NotRenewedNoResponse, got.Reason)
	require.Len(t, r.notifier.notices, 1)
	assert.Equal(t, "renewal-lapsed:"+got.ID, r.notifier.notices[0].IdempotencyKey)
	assert.Zero(t, r.notifier.notices[0].GraceDays)
	assert.False(t, r.renewals.rows[got.ID].NotifiedAt.IsZero(), "the notice is recorded as sent")
}

func TestTheSweepRetriesANoticeThatFailedAndSkipsOldPolicies(t *testing.T) {
	r := newRig(t)
	r.seed(t, "pol-1", policyEnd)
	r.seed(t, "pol-old", policyEnd.AddDate(-1, 0, 0))
	r.clock.now = policyEnd.Add(time.Hour)
	r.notifier.err = errBoom

	n, err := r.svc.Sweep(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, n, "a policy that ended long ago is not told")
	assert.Empty(t, r.notifier.notices)

	r.notifier.err = nil
	_, err = r.svc.Sweep(context.Background())
	require.NoError(t, err)
	assert.Len(t, r.notifier.notices, 1, "the next sweep sends it")
}

func TestTheSweepWithoutANotifierStillRecordsTheLapse(t *testing.T) {
	r := newRig(t, func(c *renewal.Config) { c.Notifier = nil })
	r.seed(t, "pol-1", policyEnd)
	r.clock.now = policyEnd.Add(time.Hour)

	n, err := r.svc.Sweep(context.Background())

	require.NoError(t, err)
	assert.Equal(t, 1, n)
}

func TestTheSweepSettlesAnAcceptedRenewalWhoseDeadlinePassed(t *testing.T) {
	r := newRig(t)
	p := r.seed(t, "pol-1", policyEnd)
	a, err := r.svc.Accept(insured("sub-1"), p.ID)
	require.NoError(t, err)
	r.charges.set(a.Renewal.ChargeID, func(c *domain.Charge) { c.Status = domain.ChargeFailed })
	r.clock.now = a.Renewal.RetryUntil.Add(time.Minute)

	_, err = r.svc.Sweep(context.Background())
	require.NoError(t, err)

	got, err := r.renewals.Get(context.Background(), p.ID, p.CoverageEnd)
	require.NoError(t, err)
	assert.Equal(t, domain.RenewalNotRenewed, got.Status)
	assert.Equal(t, domain.NotRenewedPaymentFailed, got.Reason)
}

func TestTheProjectorStoresThePolicyOfAnActivationOnce(t *testing.T) {
	p := newFakePolicies()
	proj := renewal.NewProjector(p)
	e := domain.CoverageActivated{PolicyID: "pol-2", AcceptanceID: "ren-x", CustomerID: "sub-1", Premium: 1, Currency: "COP",
		CoverageStart: policyEnd, CoverageEnd: policyEnd.AddDate(1, 0, 0), RenewalOf: "pol-1"}

	require.NoError(t, proj.Receive(context.Background(), e))
	e.Premium = 99
	require.NoError(t, proj.Receive(context.Background(), e))

	got, err := p.Get(context.Background(), "pol-2")
	require.NoError(t, err)
	assert.Equal(t, "pol-1", got.RenewalOf)
	assert.InDelta(t, 1, got.Premium, 0.001, "a redelivery changes nothing")
}

func TestRunSweepsUntilTheContextEnds(t *testing.T) {
	r := newRig(t)
	r.seed(t, "pol-1", policyEnd)
	r.clock.now = policyEnd.Add(time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { r.svc.Run(ctx, 5*time.Millisecond); close(done) }()

	require.Eventually(t, func() bool { return len(r.notifierNotices()) == 1 }, 2*time.Second, 5*time.Millisecond)
	cancel()
	<-done
}

func (r *rig) notifierNotices() []domain.RenewalNotice {
	r.notifier.mu.Lock()
	defer r.notifier.mu.Unlock()
	return append([]domain.RenewalNotice(nil), r.notifier.notices...)
}
