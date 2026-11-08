package postgres_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/postgres"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
)

// expiringNow is the instant of the queries: 16 September 2026, 07:00 in Colombia.
var expiringNow = time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)

type expiringFixture struct {
	db       *sql.DB
	store    *postgres.ExpiringPolicyStore
	policies *postgres.PolicyStore
	renewals *postgres.RenewalStore
}

func newExpiringFixture(t *testing.T) *expiringFixture {
	t.Helper()
	requireOfferDocker(t)
	db := newDB(t)
	return &expiringFixture{db: db, store: postgres.NewExpiringPolicyStore(db),
		policies: postgres.NewPolicyStore(db), renewals: postgres.NewRenewalStore(db)}
}

// policy stores a policy of carolina that ends at end.
func (f *expiringFixture) policy(t *testing.T, id string, end time.Time) domain.Policy {
	t.Helper()
	p := samplePolicy(id, "sub-carolina", end)
	require.NoError(t, f.policies.Create(context.Background(), p))
	return p
}

// renewal stores the renewal of the current period of p with the given status.
func (f *expiringFixture) renewal(t *testing.T, p domain.Policy, status domain.RenewalStatus) domain.Renewal {
	t.Helper()
	r := sampleRenewal(p, status)
	if status == domain.RenewalNotRenewed {
		r.Reason = domain.NotRenewedNoResponse
	}
	if status != domain.RenewalAccepted {
		r.ClosedAt = expiringNow.Add(-time.Hour)
	}
	stored, created, err := f.renewals.Create(context.Background(), r)
	require.NoError(t, err)
	require.True(t, created)
	return stored
}

func (f *expiringFixture) list(t *testing.T, from, to string, status domain.ExpiringStatus, offset, limit int) ([]domain.ExpiringPolicy, int) {
	t.Helper()
	start, end := expiringQuery(t, from, to).Bounds()
	items, total, err := f.store.ListExpiring(context.Background(), start, end, expiringNow, status, offset, limit)
	require.NoError(t, err)
	return items, total
}

func expiringQuery(t *testing.T, from, to string) domain.ExpiringQuery {
	t.Helper()
	start, err := domain.ParseCivilDay(from)
	require.NoError(t, err)
	end, err := domain.ParseCivilDay(to)
	require.NoError(t, err)
	return domain.ExpiringQuery{From: start, To: end}
}

func idsOf(items []domain.ExpiringPolicy) []string {
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = it.Policy.ID
	}
	return out
}

func TestExpiringStoreTheRangeIsInclusiveByColombianDay(t *testing.T) {
	f := newExpiringFixture(t)
	f.policy(t, "pol-day-before", time.Date(2026, 10, 2, 4, 59, 59, 999_000_000, time.UTC)) // 1 Oct, 23:59:59.999 COT
	f.policy(t, "pol-first", time.Date(2026, 10, 2, 5, 0, 0, 0, time.UTC))                  // 2 Oct, 00:00 COT
	f.policy(t, "pol-utc-next-day", time.Date(2026, 10, 6, 2, 0, 0, 0, time.UTC))           // 5 Oct, 21:00 COT
	f.policy(t, "pol-last", time.Date(2026, 10, 6, 4, 59, 59, 999_000_000, time.UTC))       // 5 Oct, 23:59:59.999 COT
	f.policy(t, "pol-day-after", time.Date(2026, 10, 6, 5, 0, 0, 0, time.UTC))              // 6 Oct, 00:00 COT

	items, total := f.list(t, "2026-10-02", "2026-10-05", "", 0, 20)

	assert.Equal(t, []string{"pol-first", "pol-utc-next-day", "pol-last"}, idsOf(items))
	assert.Equal(t, 3, total)
}

func TestExpiringStoreListsOnlyThePoliciesInForceWithAnOpenPeriod(t *testing.T) {
	f := newExpiringFixture(t)
	f.policy(t, "pol-expired", expiringNow.Add(-24*time.Hour))
	f.policy(t, "pol-ends-now", expiringNow)
	open := f.policy(t, "pol-open", expiringNow.Add(10*24*time.Hour))
	accepted := f.policy(t, "pol-accepted", expiringNow.Add(11*24*time.Hour))
	f.renewal(t, accepted, domain.RenewalAccepted)
	renewed := f.policy(t, "pol-renewed", expiringNow.Add(12*24*time.Hour))
	f.renewal(t, renewed, domain.RenewalRenewed)
	lapsed := f.policy(t, "pol-not-renewed", expiringNow.Add(13*24*time.Hour))
	f.renewal(t, lapsed, domain.RenewalNotRenewed)

	items, total := f.list(t, "2026-09-01", "2026-10-31", "", 0, 20)

	require.Equal(t, []string{"pol-open", "pol-accepted"}, idsOf(items), "expired, renewed and not renewed periods are not listed")
	assert.Equal(t, 2, total)
	assert.Equal(t, open, items[0].Policy)
	assert.Equal(t, domain.ExpiringStatusExpiring, items[0].Status)
	assert.Nil(t, items[0].Renewal)

	assert.Equal(t, domain.ExpiringStatusRenewalInProgress, items[1].Status)
	require.NotNil(t, items[1].Renewal)
	r := items[1].Renewal
	assert.Equal(t, domain.RenewalID(accepted.ID, accepted.CoverageEnd), r.ID)
	assert.Equal(t, domain.RenewalAccepted, r.Status)
	assert.Equal(t, accepted.CoverageEnd, r.PeriodEnd)
	assert.Equal(t, r.ID, r.ChargeID)
	assert.False(t, r.AcceptedAt.IsZero())
	assert.False(t, r.RetryUntil.IsZero())
	assert.True(t, r.ClosedAt.IsZero())
}

func TestExpiringStoreFiltersByStatus(t *testing.T) {
	f := newExpiringFixture(t)
	f.policy(t, "pol-open", expiringNow.Add(10*24*time.Hour))
	accepted := f.policy(t, "pol-accepted", expiringNow.Add(11*24*time.Hour))
	f.renewal(t, accepted, domain.RenewalAccepted)

	items, total := f.list(t, "2026-09-16", "2026-10-16", domain.ExpiringStatusExpiring, 0, 20)
	assert.Equal(t, []string{"pol-open"}, idsOf(items))
	assert.Equal(t, 1, total)

	items, total = f.list(t, "2026-09-16", "2026-10-16", domain.ExpiringStatusRenewalInProgress, 0, 20)
	assert.Equal(t, []string{"pol-accepted"}, idsOf(items))
	assert.Equal(t, 1, total)
}

func TestExpiringStoreOrdersByEndThenIdAndPagesWithTheTotal(t *testing.T) {
	f := newExpiringFixture(t)
	sameEnd := expiringNow.Add(5 * 24 * time.Hour)
	f.policy(t, "pol-5", expiringNow.Add(9*24*time.Hour))
	f.policy(t, "pol-2b", sameEnd)
	f.policy(t, "pol-1", expiringNow.Add(2*24*time.Hour))
	f.policy(t, "pol-2a", sameEnd)
	f.policy(t, "pol-4", expiringNow.Add(8*24*time.Hour))

	all, total := f.list(t, "2026-09-16", "2026-10-16", "", 0, 20)
	assert.Equal(t, []string{"pol-1", "pol-2a", "pol-2b", "pol-4", "pol-5"}, idsOf(all), "the ones ending first, ties by id")
	assert.Equal(t, 5, total)

	page, total := f.list(t, "2026-09-16", "2026-10-16", "", 2, 2)
	assert.Equal(t, []string{"pol-2b", "pol-4"}, idsOf(page))
	assert.Equal(t, 5, total, "the total does not depend on the page")

	beyond, total := f.list(t, "2026-09-16", "2026-10-16", "", 20, 20)
	assert.NotNil(t, beyond)
	assert.Empty(t, beyond)
	assert.Equal(t, 5, total)
}

func TestExpiringStoreAnEmptyRangeIsAnEmptyListNotAnError(t *testing.T) {
	f := newExpiringFixture(t)
	f.policy(t, "pol-open", expiringNow.Add(10*24*time.Hour))

	items, total := f.list(t, "2026-12-01", "2026-12-07", "", 0, 20)

	assert.NotNil(t, items)
	assert.Empty(t, items)
	assert.Zero(t, total)
}

func TestExpiringStoreCountsTheOpenPeriodsDueWithinTheWindowWithNoOtherFilter(t *testing.T) {
	f := newExpiringFixture(t)
	until := expiringNow.AddDate(0, 0, 30)
	f.policy(t, "pol-ends-now", expiringNow)
	f.policy(t, "pol-open", expiringNow.Add(time.Hour))
	accepted := f.policy(t, "pol-accepted", expiringNow.Add(2*time.Hour))
	f.renewal(t, accepted, domain.RenewalAccepted)
	renewed := f.policy(t, "pol-renewed", expiringNow.Add(3*time.Hour))
	f.renewal(t, renewed, domain.RenewalRenewed)
	f.policy(t, "pol-at-the-end", until)
	f.policy(t, "pol-after", until.Add(time.Microsecond))

	n, err := f.store.CountDueWithinWindow(context.Background(), expiringNow, until)

	require.NoError(t, err)
	assert.Equal(t, 3, n, "open, accepted and the one ending at the end of the window")
}

func TestExpiringStoreARenewedPolicyAppearsOnlyByItsOwnEnd(t *testing.T) {
	f := newExpiringFixture(t)
	old := f.policy(t, "pol-old", expiringNow.Add(10*24*time.Hour))
	r := f.renewal(t, old, domain.RenewalRenewed)
	renewedPolicy := samplePolicy("pol-new", "sub-carolina", old.CoverageEnd.AddDate(1, 0, 0))
	renewedPolicy.CoverageStart, renewedPolicy.RenewalOf = old.CoverageEnd, old.ID
	require.NoError(t, f.policies.Create(context.Background(), renewedPolicy))
	_, err := f.db.ExecContext(context.Background(), `UPDATE renewal SET new_policy_id = $2 WHERE renewal_id = $1`, r.ID, "pol-new")
	require.NoError(t, err)

	items, _ := f.list(t, "2026-09-16", "2026-10-16", "", 0, 20)
	assert.Empty(t, items, "the old period is renewed and the new policy ends next year")

	items, _ = f.list(t, "2027-09-16", "2027-10-16", "", 0, 20)
	require.Equal(t, []string{"pol-new"}, idsOf(items))
	assert.Equal(t, "pol-old", items[0].Policy.RenewalOf)
	assert.Equal(t, domain.ExpiringStatusExpiring, items[0].Status)
}

func TestExpiringStoreReportsADatabaseThatDoesNotAnswer(t *testing.T) {
	f := newExpiringFixture(t)
	require.NoError(t, f.db.Close())

	_, _, err := f.store.ListExpiring(context.Background(), expiringNow, expiringNow.AddDate(0, 0, 30), expiringNow, "", 0, 20)
	assert.Error(t, err)
	_, err = f.store.CountDueWithinWindow(context.Background(), expiringNow, expiringNow.AddDate(0, 0, 30))
	assert.Error(t, err)
}
