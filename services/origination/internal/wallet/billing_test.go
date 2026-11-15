package wallet_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/security"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/renewal"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/wallet"
)

// HU-W1.6-02: the wallet says where the premium of each policy origination
// issued stands, and the detail of one policy (screens A1, A2 and A2b).

type fakeBilling struct {
	records map[string]ports.BillingRecord
	asked   []string
	err     error
}

func (f *fakeBilling) Billing(_ context.Context, ids []string) (map[string]ports.BillingRecord, error) {
	f.asked = ids
	return f.records, f.err
}

type fakeIssuance struct {
	charge domain.Charge
	found  bool
}

func (f fakeIssuance) IssuanceCharge(context.Context, string) (domain.Charge, bool, error) {
	return f.charge, f.found, nil
}

var rules = domain.DefaultInstallmentRules

func ownPolicies(ps ...domain.Policy) *fakeRenewals {
	views := make([]renewal.PolicyView, 0, len(ps))
	for _, p := range ps {
		views = append(views, renewal.PolicyView{Policy: p, CanRenew: true})
	}
	return &fakeRenewals{views: views}
}

func started(id string, start time.Time) domain.Policy {
	p := lifePolicy(start.AddDate(1, 0, 0))
	p.ID, p.AcceptanceID, p.CoverageStart = id, "acc-"+id, start
	return p
}

func TestTheWalletSaysWhereThePremiumOfEachPolicyStands(t *testing.T) {
	upToDate := started("pol-ok", now.AddDate(0, 0, -10))
	pending := started("pol-pending", now.AddDate(0, -1, -3))
	cancelled := started("pol-cancelled", now.AddDate(0, -2, -5))
	unpaid := domain.NewInstallment(pending, 2, rules, now)
	gone := domain.NewInstallment(cancelled, 2, rules, now)
	gone.Status, gone.Attempts = domain.InstallmentUnpaid, 3
	billing := &fakeBilling{records: map[string]ports.BillingRecord{
		"pol-pending": {Installments: []domain.Installment{unpaid}},
		"pol-cancelled": {Installments: []domain.Installment{gone}, Cancellation: &domain.PolicyCancellation{
			PolicyID: "pol-cancelled", Reason: domain.CancellationNonPayment, Installment: gone, CancelledAt: now.Add(-time.Hour)}},
	}}
	core := &fakePortfolio{policies: []domain.WalletPolicy{autoPolicy()}}
	svc, err := wallet.NewService(ownPolicies(upToDate, pending, cancelled), core, fixedClock{})
	require.NoError(t, err)
	svc = svc.WithBilling(billing, fakeIssuance{}, rules)

	got, err := svc.List(asInsured("sub-1"))

	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"pol-ok", "pol-pending", "pol-cancelled"}, billing.asked,
		"only the policies origination issued")
	byID := map[string]wallet.Item{}
	for _, it := range got.Items {
		byID[it.Policy.ID] = it
	}
	require.NotNil(t, byID["pol-ok"].Billing)
	assert.Equal(t, domain.PaymentUpToDate, byID["pol-ok"].Billing.Status)
	assert.Equal(t, rules.DueAt(upToDate, 2), byID["pol-ok"].Billing.NextDueAt)
	assert.Equal(t, domain.PaymentPending, byID["pol-pending"].Billing.Status)
	assert.Equal(t, domain.PolicyActive, byID["pol-pending"].Policy.Status, "the grace period keeps the coverage")
	assert.Equal(t, domain.PaymentCancelled, byID["pol-cancelled"].Billing.Status)
	assert.Equal(t, domain.PolicyCancelled, byID["pol-cancelled"].Policy.Status)
	assert.False(t, byID["pol-cancelled"].Renewal.CanRenew)
	assert.Nil(t, byID["AU-2025-118204"].Billing, "the policy core keeps no premiums")
}

func TestWithoutTheInstalmentsTheWalletListsAsBefore(t *testing.T) {
	svc, err := wallet.NewService(ownPolicies(started("pol-1", now.AddDate(0, 0, -10))), nil, fixedClock{})
	require.NoError(t, err)

	got, err := svc.List(asInsured("sub-1"))

	require.NoError(t, err)
	require.Len(t, got.Items, 1)
	assert.Nil(t, got.Items[0].Billing)
}

func TestAFailureToReadTheInstalmentsFailsTheWallet(t *testing.T) {
	down := errors.New("database down")
	svc, err := wallet.NewService(ownPolicies(started("pol-1", now.AddDate(0, 0, -10))), nil, fixedClock{})
	require.NoError(t, err)
	svc = svc.WithBilling(&fakeBilling{err: down}, fakeIssuance{}, rules)

	_, err = svc.List(asInsured("sub-1"))

	assert.ErrorIs(t, err, down, "a policy without its payment status could be mistaken for one up to date")
}

func TestTheDetailOfAPolicyOriginationIssuedHasItsCardAndItsFirstMonth(t *testing.T) {
	p := started("pol-1", now.AddDate(0, 0, -10))
	card := domain.PaymentMethod{Kind: domain.PaymentCard, Token: "tok_secret", Brand: "visa", Last4: "1111"}
	issued := domain.Charge{AcceptanceID: p.AcceptanceID, Status: domain.ChargeConfirmed, AmountMinor: 6_850_000,
		Currency: "COP", ConfirmedAt: p.CoverageStart, PaymentMethod: card}
	svc, err := wallet.NewService(ownPolicies(p), &fakePortfolio{}, fixedClock{})
	require.NoError(t, err)
	svc = svc.WithBilling(&fakeBilling{records: map[string]ports.BillingRecord{}}, fakeIssuance{charge: issued, found: true}, rules)

	d, err := svc.Get(asInsured("sub-1"), "pol-1")

	require.NoError(t, err)
	require.NotNil(t, d.PaymentMethod)
	assert.Equal(t, "1111", d.PaymentMethod.Last4)
	assert.Empty(t, d.PaymentMethod.Token, "the token never leaves the service")
	require.NotNil(t, d.Issuance)
	assert.Empty(t, d.Issuance.PaymentMethod.Token)
	assert.Equal(t, int64(6_850_000), d.Issuance.AmountMinor)
	assert.Equal(t, domain.PaymentUpToDate, d.Billing.Status)
}

func TestTheDetailOfAPolicyOfTheCoreHasNoPremium(t *testing.T) {
	core := &fakePortfolio{policies: []domain.WalletPolicy{autoPolicy()}, holder: domain.PolicyHolder{FullName: "Carolina"}}
	svc, err := wallet.NewService(ownPolicies(), core, fixedClock{})
	require.NoError(t, err)

	d, err := svc.Get(asInsured("sub-1"), "AU-2025-118204")

	require.NoError(t, err)
	assert.Equal(t, domain.SourcePolicyCore, d.Policy.Source)
	assert.Equal(t, "Carolina", d.Policy.Holder.FullName)
	assert.Nil(t, d.Billing)
	assert.Nil(t, d.PaymentMethod)
}

func TestTheDetailOfAPolicyTheCallerDoesNotHaveIsNotFound(t *testing.T) {
	svc, err := wallet.NewService(ownPolicies(started("pol-1", now)), &fakePortfolio{}, fixedClock{})
	require.NoError(t, err)

	_, err = svc.Get(asInsured("sub-1"), "pol-of-someone-else")
	assert.ErrorIs(t, err, ports.ErrPolicyNotFound)
	_, err = svc.Get(context.Background(), "pol-1")
	assert.ErrorIs(t, err, security.ErrUnauthenticated)

	down := errors.New("core down")
	svc, err = wallet.NewService(nil, &fakePortfolio{policiesErr: down}, fixedClock{})
	require.NoError(t, err)
	_, err = svc.Get(asInsured("sub-1"), "AU-2025-118204")
	assert.ErrorIs(t, err, down, "the detail of one policy is never partial")
}
