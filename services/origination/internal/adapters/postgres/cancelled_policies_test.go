package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/postgres"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
)

// HU-W1.6-02: what a cancellation for non-payment changes in the other reads
// of the policies: the list of the advisor, the expiry alerts, the lapse of a
// renewal and the renewal itself.

// cancel stores the cancellation of p at the given time, through the
// instalments: its second month, declined three times.
func (f *expiringFixture) cancel(t *testing.T, p domain.Policy, at time.Time) {
	t.Helper()
	ctx := context.Background()
	s := postgres.NewInstallmentStore(f.db)
	p.CoverageStart = at.AddDate(0, -2, 0)
	i := domain.NewInstallment(p, 2, rules, at.AddDate(0, -1, 0))
	_, err := s.CreateInstallment(ctx, i)
	require.NoError(t, err)
	_, err = f.db.ExecContext(ctx, `UPDATE premium_installment SET attempts = 3, next_attempt_at = NULL,
		cancels_at = $3 WHERE policy_id = $1 AND seq = $2`, p.ID, 2, at)
	require.NoError(t, err)
	i.CancelsAt, i.Attempts = at, 3
	created, err := s.Cancel(ctx, p, i, at)
	require.NoError(t, err)
	require.True(t, created)
}

func TestTheAdvisorSeesThePoliciesCancelledInTheLast30DaysAfterTheOpenOnes(t *testing.T) {
	f := newExpiringFixture(t)
	ctx := context.Background()
	open := f.policy(t, "pol-open", expiringNow.AddDate(0, 0, 10))
	recent := f.policy(t, "pol-recent", expiringNow.AddDate(0, 5, 0))
	older := f.policy(t, "pol-older", expiringNow.AddDate(0, 0, 12))
	old := f.policy(t, "pol-old", expiringNow.AddDate(0, 0, 11))
	f.cancel(t, recent, expiringNow.AddDate(0, 0, -2))
	f.cancel(t, older, expiringNow.AddDate(0, 0, -20))
	f.cancel(t, old, expiringNow.AddDate(0, 0, -31))

	all, total := f.list(t, "2026-09-16", "2026-10-16", "", 0, 20)

	assert.Equal(t, 3, total)
	require.Len(t, all, 3)
	assert.Equal(t, []string{open.ID, recent.ID, older.ID}, []string{all[0].Policy.ID, all[1].Policy.ID, all[2].Policy.ID},
		"the open periods first, then the cancellations, the latest first, whatever their range")
	assert.Equal(t, domain.ExpiringStatusExpiring, all[0].Status)
	assert.True(t, all[0].CancelledAt.IsZero())
	assert.Equal(t, domain.ExpiringStatusCancelledNonPayment, all[1].Status)
	assert.Equal(t, expiringNow.AddDate(0, 0, -2), all[1].CancelledAt)

	cancelled, total := f.list(t, "2026-09-16", "2026-10-16", domain.ExpiringStatusCancelledNonPayment, 0, 20)
	assert.Equal(t, 2, total)
	assert.Len(t, cancelled, 2)
	expiring, total := f.list(t, "2026-09-16", "2026-10-16", domain.ExpiringStatusExpiring, 0, 20)
	assert.Equal(t, 1, total, "a cancelled policy is not about to expire")
	assert.Equal(t, open.ID, expiring[0].Policy.ID)

	n, err := f.store.CountCancelledSince(ctx, expiringNow.AddDate(0, 0, -30))
	require.NoError(t, err)
	assert.Equal(t, 2, n)
	due, err := f.store.CountDueWithinWindow(ctx, expiringNow, expiringNow.AddDate(0, 0, 30))
	require.NoError(t, err)
	assert.Equal(t, 1, due, "the cancelled ones are not due to renew")
}

func TestACancelledPolicyIsNotAlertedNorLapsedNorRenewable(t *testing.T) {
	f := newExpiringFixture(t)
	ctx := context.Background()
	end := expiringNow.AddDate(0, 0, 10)
	kept := f.policy(t, "pol-kept", end)
	gone := f.policy(t, "pol-gone", end)
	f.cancel(t, gone, expiringNow.Add(-time.Hour))

	alerts, err := f.policies.ListExpiringUnrenewed(ctx, expiringNow, expiringNow.AddDate(0, 0, 31))
	require.NoError(t, err)
	require.Len(t, alerts, 1, "no expiry alert for a cancelled policy")
	assert.Equal(t, kept.ID, alerts[0].ID)

	lapsed, err := f.policies.ListLapsed(ctx, end.Add(-time.Hour), end.Add(time.Hour), 10)
	require.NoError(t, err)
	require.Len(t, lapsed, 1, "a cancelled policy is not told it was not renewed")
	assert.Equal(t, kept.ID, lapsed[0].ID)

	s := postgres.NewInstallmentStore(f.db)
	yes, err := s.IsCancelled(ctx, gone.ID)
	require.NoError(t, err)
	assert.True(t, yes)
	no, err := s.IsCancelled(ctx, kept.ID)
	require.NoError(t, err)
	assert.False(t, no)
}
