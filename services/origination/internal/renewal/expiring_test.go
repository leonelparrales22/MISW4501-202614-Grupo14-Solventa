package renewal_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/security"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/renewal"
)

// fakeExpiring is the read model of the list: it records the query it got
// and answers the rows and counts of the test.
type fakeExpiring struct {
	items    []domain.ExpiringPolicy
	total    int
	due      int
	err      error
	countErr error

	start, end, now time.Time
	status          domain.ExpiringStatus
	offset, limit   int
	dueNow, dueTill time.Time
}

func (f *fakeExpiring) ListExpiring(_ context.Context, start, end, now time.Time, status domain.ExpiringStatus,
	offset, limit int) ([]domain.ExpiringPolicy, int, error) {
	f.start, f.end, f.now, f.status, f.offset, f.limit = start, end, now, status, offset, limit
	return f.items, f.total, f.err
}

func (f *fakeExpiring) CountDueWithinWindow(_ context.Context, now, until time.Time) (int, error) {
	f.dueNow, f.dueTill = now, until
	return f.due, f.countErr
}

func advisor(sub string) context.Context {
	return security.WithClaims(context.Background(), security.Claims{Subject: sub, Roles: []security.Role{security.RoleAdvisor}})
}

// expiringAt is a policy of the list that ends at end.
func expiringAt(id string, end time.Time) domain.ExpiringPolicy {
	return domain.ExpiringPolicy{Status: domain.ExpiringStatusExpiring, Policy: domain.Policy{ID: id, CustomerID: "sub-1",
		Holder:  domain.Applicant{FullName: "Carolina Pérez", DocumentID: "1020345678"},
		Premium: 142_300, Currency: "COP", CoverageStart: end.AddDate(-1, 0, 0), CoverageEnd: end}}
}

func newExpiringService(t *testing.T, q *fakeExpiring, now time.Time) *renewal.ExpiringService {
	t.Helper()
	svc, err := renewal.NewExpiringService(q, domain.DefaultRenewalPolicy, &fakeClock{now: now})
	require.NoError(t, err)
	return svc
}

func TestNewExpiringServiceRequiresItsDependenciesAndValidRules(t *testing.T) {
	_, err := renewal.NewExpiringService(nil, domain.DefaultRenewalPolicy, &fakeClock{})
	assert.Error(t, err)
	_, err = renewal.NewExpiringService(&fakeExpiring{}, domain.DefaultRenewalPolicy, nil)
	assert.Error(t, err)
	_, err = renewal.NewExpiringService(&fakeExpiring{}, domain.RenewalPolicy{}, &fakeClock{})
	assert.Error(t, err, "the rules must be valid")
}

func TestOnlyAnAdvisorListsTheExpiringPolicies(t *testing.T) {
	q := &fakeExpiring{}
	svc := newExpiringService(t, q, t0)

	_, err := svc.List(context.Background(), domain.ExpiringQuery{})
	assert.ErrorIs(t, err, security.ErrUnauthenticated)
	for _, role := range []security.Role{security.RoleInsured, security.RoleOperations, security.RolePartner} {
		ctx := security.WithClaims(context.Background(), security.Claims{Subject: "x", Roles: []security.Role{role}})
		_, err := svc.List(ctx, domain.ExpiringQuery{})
		assert.ErrorIs(t, err, security.ErrForbidden, role)
	}
	assert.True(t, q.start.IsZero(), "nothing was read for them")

	_, err = svc.List(advisor("laura"), domain.ExpiringQuery{})
	assert.NoError(t, err)
}

func TestWithoutARangeTheListIsTheRenewalWindowFromToday(t *testing.T) {
	// 20 September 2026, 05:00 in Colombia.
	q := &fakeExpiring{due: 12}
	svc := newExpiringService(t, q, t0)

	page, err := svc.List(advisor("laura"), domain.ExpiringQuery{})

	require.NoError(t, err)
	assert.Equal(t, "2026-09-20", page.From.Format(domain.CivilDayLayout))
	assert.Equal(t, "2026-10-20", page.To.Format(domain.CivilDayLayout))
	assert.Equal(t, 30, page.WindowDays)
	assert.Equal(t, time.Date(2026, 9, 20, 5, 0, 0, 0, time.UTC), q.start)
	assert.Equal(t, time.Date(2026, 10, 21, 5, 0, 0, 0, time.UTC), q.end, "the whole last day")
	assert.Equal(t, t0, q.now)
	assert.Equal(t, 0, q.offset)
	assert.Equal(t, domain.DefaultExpiringPageSize, q.limit)
	assert.Equal(t, 12, page.DueWithinWindow)
	assert.Equal(t, t0, q.dueNow)
	assert.Equal(t, t0.AddDate(0, 0, 30), q.dueTill, "the indicator counts the renewal window")
	assert.NotNil(t, page.Items, "an empty page is an empty list")
	assert.Empty(t, page.Items)
}

func TestTheQueryOfTheAdvisorReachesTheStore(t *testing.T) {
	q := &fakeExpiring{total: 45}
	svc := newExpiringService(t, q, t0)
	from, err := domain.ParseCivilDay("2026-10-01")
	require.NoError(t, err)
	to, err := domain.ParseCivilDay("2026-12-31")
	require.NoError(t, err)

	page, err := svc.List(advisor("laura"), domain.ExpiringQuery{From: from, To: to,
		Status: domain.ExpiringStatusRenewalInProgress, Page: 3, PageSize: 10})

	require.NoError(t, err)
	assert.Equal(t, domain.ExpiringStatusRenewalInProgress, q.status)
	assert.Equal(t, 20, q.offset)
	assert.Equal(t, 10, q.limit)
	assert.Equal(t, 45, page.Total)
	assert.Equal(t, 3, page.Page)
	assert.Equal(t, 10, page.PageSize)
	assert.Equal(t, "2026-10-01", page.From.Format(domain.CivilDayLayout))
	assert.Equal(t, "2026-12-31", page.To.Format(domain.CivilDayLayout))
}

func TestEachRowSaysWhetherItCanBeRenewedNowAndWhenItsWindowOpens(t *testing.T) {
	inWindow := expiringAt("pol-in", t0.Add(10*24*time.Hour))
	notYet := expiringAt("pol-later", t0.Add(60*24*time.Hour))
	inProgress := expiringAt("pol-accepted", t0.Add(5*24*time.Hour))
	inProgress.Status = domain.ExpiringStatusRenewalInProgress
	inProgress.Renewal = &domain.Renewal{ID: "ren-pol-accepted", Status: domain.RenewalAccepted}
	q := &fakeExpiring{items: []domain.ExpiringPolicy{inProgress, inWindow, notYet}, total: 3}
	svc := newExpiringService(t, q, t0)

	page, err := svc.List(advisor("laura"), domain.ExpiringQuery{})

	require.NoError(t, err)
	require.Len(t, page.Items, 3)
	assert.False(t, page.Items[0].CanRenew, "a renewal in progress is seen, not renewed again")
	assert.True(t, page.Items[1].CanRenew, "inside the window of HU-W1.5-02")
	assert.False(t, page.Items[2].CanRenew, "outside the window it is listed with no Renew (D-5 a)")
	assert.Equal(t, notYet.Policy.CoverageEnd.AddDate(0, 0, -30), page.Items[2].WindowOpensAt)
	assert.Equal(t, notYet.Policy.CoverageEnd, page.Items[2].WindowClosesAt)
}

func TestAnInvalidQueryIsRefusedBeforeReading(t *testing.T) {
	q := &fakeExpiring{}
	svc := newExpiringService(t, q, t0)
	from, _ := domain.ParseCivilDay("2026-10-10")
	to, _ := domain.ParseCivilDay("2026-10-01")

	_, err := svc.List(advisor("laura"), domain.ExpiringQuery{From: from, To: to})
	assert.ErrorIs(t, err, domain.ErrExpiringRangeInverted)
	_, err = svc.List(advisor("laura"), domain.ExpiringQuery{PageSize: 500})
	assert.ErrorIs(t, err, domain.ErrInvalidExpiringQuery)
	assert.True(t, q.start.IsZero(), "the store was not called")
}

func TestAStoreThatFailsIsAnError(t *testing.T) {
	boom := errors.New("database down")
	svc := newExpiringService(t, &fakeExpiring{err: boom}, t0)
	_, err := svc.List(advisor("laura"), domain.ExpiringQuery{})
	assert.ErrorIs(t, err, boom)

	svc = newExpiringService(t, &fakeExpiring{countErr: boom}, t0)
	_, err = svc.List(advisor("laura"), domain.ExpiringQuery{})
	assert.ErrorIs(t, err, boom)
}
