package renewal_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/renewal"
)

// HU-W1.6-02: a policy cancelled for non-payment is not renewed; the insured
// quotes again.

type cancelledPolicies struct {
	ids map[string]bool
	err error
}

func (c cancelledPolicies) IsCancelled(_ context.Context, id string) (bool, error) {
	return c.ids[id], c.err
}

func withCancellations(c cancelledPolicies) rigOption {
	return func(cfg *renewal.Config) { cfg.Cancellations = c }
}

func TestAPolicyCancelledForNonPaymentCannotBeRenewed(t *testing.T) {
	r := newRig(t, withCancellations(cancelledPolicies{ids: map[string]bool{"pol-cancelled": true}}))
	cancelled := r.seed(t, "pol-cancelled", policyEnd)
	open := r.seed(t, "pol-open", policyEnd)

	_, err := r.svc.Conditions(insured("sub-1"), cancelled.ID)
	assert.ErrorIs(t, err, domain.ErrRenewalNotAvailable)
	_, err = r.svc.Accept(insured("sub-1"), cancelled.ID)
	assert.ErrorIs(t, err, domain.ErrRenewalNotAvailable)
	assert.Empty(t, r.charges.attached, "nothing is charged")

	views, err := r.svc.ListPolicies(insured("sub-1"))
	require.NoError(t, err)
	can := map[string]bool{}
	for _, v := range views {
		can[v.Policy.ID] = v.CanRenew
	}
	assert.False(t, can[cancelled.ID])
	assert.True(t, can[open.ID], "the other policy is still renewable")
	_, err = r.svc.Conditions(insured("sub-1"), open.ID)
	assert.NoError(t, err)
}

func TestWhenItCannotTellWhetherThePolicyWasCancelledItDoesNotRenew(t *testing.T) {
	down := errors.New("database down")
	r := newRig(t, withCancellations(cancelledPolicies{err: down}))
	p := r.seed(t, "pol-1", policyEnd)

	_, err := r.svc.Accept(insured("sub-1"), p.ID)
	assert.ErrorIs(t, err, down)
	_, err = r.svc.ListPolicies(insured("sub-1"))
	assert.ErrorIs(t, err, down)
}

func TestTheListOfTheAdvisorAsksForTheCancellationsOfTheLast30Days(t *testing.T) {
	q := &fakeExpiring{cancelled: 2, items: []domain.ExpiringPolicy{{
		Policy:      domain.Policy{ID: "pol-c", CoverageEnd: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)},
		Status:      domain.ExpiringStatusCancelledNonPayment,
		CancelledAt: t0.Add(-time.Hour),
	}}}
	svc := newExpiringService(t, q, t0)

	page, err := svc.List(advisor("sub-asesor"), domain.ExpiringQuery{Status: domain.ExpiringStatusCancelledNonPayment})

	require.NoError(t, err)
	assert.Equal(t, 2, page.CancelledNonPayment)
	assert.Equal(t, t0.AddDate(0, 0, -30), q.cancelledSince)
	assert.Equal(t, domain.ExpiringStatusCancelledNonPayment, q.status)
	require.Len(t, page.Items, 1)
	assert.False(t, page.Items[0].CanRenew, "a cancelled policy is not renewed, even inside its window")
}

func TestTheAdvisorCannotRenewAPolicyCancelledForNonPayment(t *testing.T) {
	a := newAssistedRigOver(t, newRig(t, withCancellations(cancelledPolicies{ids: map[string]bool{"pol-1": true}})))
	p := a.seedSoldIn(t, "pol-1", domain.OfferChannelAssisted)

	v, err := a.assisted.View(laura, p.ID)

	require.NoError(t, err)
	assert.Equal(t, renewal.BlockedNotAvailable, v.BlockedReason)
	assert.False(t, v.WindowOpen)
	assert.False(t, v.CanRequote)
	_, err = a.assisted.Requote(laura, p.ID, renewal.RequoteInput{ConsentAffirmed: true})
	assert.ErrorIs(t, err, domain.ErrRenewalNotAvailable)
	_, _, err = a.assisted.Formalize(laura, p.ID, renewal.FormalizeInput{RequoteID: "rq-001"})
	assert.ErrorIs(t, err, domain.ErrRenewalNotAvailable)
}
