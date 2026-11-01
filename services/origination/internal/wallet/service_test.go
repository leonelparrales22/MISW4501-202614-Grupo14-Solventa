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

var now = time.Date(2026, 9, 16, 14, 30, 0, 0, time.UTC)

type fixedClock struct{}

func (fixedClock) Now() time.Time { return now }

type fakeRenewals struct {
	views []renewal.PolicyView
	err   error
}

func (f *fakeRenewals) ListPolicies(context.Context) ([]renewal.PolicyView, error) { return f.views, f.err }

type fakePortfolio struct {
	policies    []domain.WalletPolicy
	policiesErr error
	holder      domain.PolicyHolder
	holderErr   error
	holderCalls int
	gotHolderID string
}

func (f *fakePortfolio) HolderPolicies(_ context.Context, id string) ([]domain.WalletPolicy, error) {
	f.gotHolderID = id
	return f.policies, f.policiesErr
}

func (f *fakePortfolio) Holder(context.Context, string) (domain.PolicyHolder, error) {
	f.holderCalls++
	return f.holder, f.holderErr
}

func asInsured(subject string) context.Context {
	return security.WithClaims(context.Background(), security.Claims{Subject: subject, Roles: []security.Role{security.RoleInsured}})
}

func lifePolicy(end time.Time) domain.Policy {
	return domain.Policy{ID: "pol-1", CustomerID: "sub-1",
		Holder:  domain.Applicant{FullName: "Carolina Pérez", DocumentID: "1032456789", Email: "carolina@example.com"},
		Premium: 68_500, Currency: "COP", InsuredAmount: 250_000_000,
		Coverages:     []domain.Coverage{{Code: domain.CoverageDeath, InsuredAmount: 250_000_000}},
		CoverageStart: end.AddDate(-1, 0, 0), CoverageEnd: end}
}

func autoPolicy() domain.WalletPolicy {
	return domain.WalletPolicy{ID: "AU-2025-118204", Source: domain.SourcePolicyCore, Line: domain.LineAuto,
		Status: domain.PolicyActive, Currency: "COP", InsuredAmount: 72_500_000,
		CoverageStart: now.AddDate(0, -11, 0), CoverageEnd: now.AddDate(0, 1, 0)}
}

func TestNewServiceNeedsAClockAndASource(t *testing.T) {
	_, err := wallet.NewService(&fakeRenewals{}, nil, nil)
	require.Error(t, err)
	_, err = wallet.NewService(nil, nil, fixedClock{})
	require.Error(t, err)
	_, err = wallet.NewService(nil, &fakePortfolio{}, fixedClock{})
	require.NoError(t, err)
}

func TestListJoinsBothSourcesWithTheHolderAsMainInsured(t *testing.T) {
	view := renewal.PolicyView{Policy: lifePolicy(now.AddDate(20, 0, 0)), CanRenew: false}
	portfolio := &fakePortfolio{policies: []domain.WalletPolicy{autoPolicy()},
		holder: domain.PolicyHolder{FullName: "Carolina Pérez Ruiz", DocumentType: "CC", DocumentID: "1032456789"}}
	svc, err := wallet.NewService(&fakeRenewals{views: []renewal.PolicyView{view}}, portfolio, fixedClock{})
	require.NoError(t, err)

	got, err := svc.List(asInsured("sub-1"))

	require.NoError(t, err)
	assert.False(t, got.Partial)
	require.Len(t, got.Items, 2)
	life, auto := got.Items[0], got.Items[1]
	assert.Equal(t, domain.SourceOrigination, life.Policy.Source)
	assert.Equal(t, domain.LineLife, life.Policy.Line)
	assert.Equal(t, domain.PolicyActive, life.Policy.Status)
	require.NotNil(t, life.Policy.Premium)
	assert.Equal(t, 68_500.0, *life.Policy.Premium)
	assert.Equal(t, "Carolina Pérez", life.Policy.Holder.FullName)
	require.NotNil(t, life.Renewal, "a policy origination issued keeps its renewal state")
	assert.Equal(t, "AU-2025-118204", auto.Policy.ID)
	assert.Nil(t, auto.Renewal)
	assert.Equal(t, "Carolina Pérez Ruiz", auto.Policy.Holder.FullName)
	assert.Equal(t, "sub-1", portfolio.gotHolderID, "the holder of the core is the subject of the token")
}

func TestListWithoutTheCoreListsOnlyOriginationsPolicies(t *testing.T) {
	expired := renewal.PolicyView{Policy: lifePolicy(now.AddDate(0, 0, -1))}
	svc, err := wallet.NewService(&fakeRenewals{views: []renewal.PolicyView{expired}}, nil, fixedClock{})
	require.NoError(t, err)

	got, err := svc.List(asInsured("sub-1"))

	require.NoError(t, err)
	require.Len(t, got.Items, 1)
	assert.Equal(t, domain.PolicyExpired, got.Items[0].Policy.Status)
}

func TestListIsPartialWhenTheCoreDoesNotAnswer(t *testing.T) {
	view := renewal.PolicyView{Policy: lifePolicy(now.AddDate(1, 0, 0))}
	portfolio := &fakePortfolio{policiesErr: ports.ErrPolicyPortfolioUnavailable}
	svc, err := wallet.NewService(&fakeRenewals{views: []renewal.PolicyView{view}}, portfolio, fixedClock{})
	require.NoError(t, err)

	got, err := svc.List(asInsured("sub-1"))

	require.NoError(t, err)
	assert.True(t, got.Partial)
	require.Len(t, got.Items, 1, "origination's policies are still listed")
}

func TestListIsPartialWhenTheHolderProfileDoesNotAnswer(t *testing.T) {
	portfolio := &fakePortfolio{policies: []domain.WalletPolicy{autoPolicy()}, holderErr: ports.ErrPolicyPortfolioUnavailable}
	svc, err := wallet.NewService(nil, portfolio, fixedClock{})
	require.NoError(t, err)

	got, err := svc.List(asInsured("sub-1"))

	require.NoError(t, err)
	assert.True(t, got.Partial)
	assert.Empty(t, got.Items, "a policy without its main insured is not listed")
	assert.NotNil(t, got.Items, "an empty wallet is a list, never nil")
}

func TestListDoesNotAskTheProfileOfAHolderWithoutPolicies(t *testing.T) {
	portfolio := &fakePortfolio{policies: []domain.WalletPolicy{}}
	svc, err := wallet.NewService(nil, portfolio, fixedClock{})
	require.NoError(t, err)

	got, err := svc.List(asInsured("sub-1"))

	require.NoError(t, err)
	assert.False(t, got.Partial)
	assert.Empty(t, got.Items)
	assert.Zero(t, portfolio.holderCalls)
}

func TestListFailsWhenOriginationsStoreFails(t *testing.T) {
	boom := errors.New("database down")
	svc, err := wallet.NewService(&fakeRenewals{err: boom}, &fakePortfolio{}, fixedClock{})
	require.NoError(t, err)

	_, err = svc.List(asInsured("sub-1"))

	require.ErrorIs(t, err, boom)
}

func TestListNeedsTheCaller(t *testing.T) {
	svc, err := wallet.NewService(nil, &fakePortfolio{}, fixedClock{})
	require.NoError(t, err)

	_, err = svc.List(context.Background())

	require.ErrorIs(t, err, security.ErrUnauthenticated)
}
