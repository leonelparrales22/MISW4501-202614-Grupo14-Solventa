package filing_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/security"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/filing"
)

// HU-W2.1-02: the list and the detail say when each claim last changed, the
// list carries the tally, and the insured downloads their supports.

type fakeActivity struct {
	changes  map[string]time.Time
	tally    domain.ClaimTally
	err      error
	tallyErr error
	since    time.Time
}

func (a *fakeActivity) LastChanges(_ context.Context, ids []string) (map[string]time.Time, error) {
	out := map[string]time.Time{}
	for _, id := range ids {
		if at, ok := a.changes[id]; ok {
			out[id] = at
		}
	}
	return out, a.err
}

func (a *fakeActivity) Tally(_ context.Context, _ string, since time.Time) (domain.ClaimTally, error) {
	a.since = since
	return a.tally, a.tallyErr
}

func TestTheListSaysWhenEachClaimLastChangedAndTalliesThemAll(t *testing.T) {
	f := newWebFixture()
	first, _, err := f.web.File(context.Background(), carolina, "form-1", f.collision(t))
	require.NoError(t, err)
	second, _, err := f.web.File(context.Background(), carolina, "form-2", f.collision(t))
	require.NoError(t, err)
	changed := filedAt.Add(26 * time.Hour)
	activity := &fakeActivity{changes: map[string]time.Time{first.ID: changed},
		tally: domain.ClaimTally{ByStatus: map[domain.Status]int{domain.StatusActive: 2}}}
	q := filing.NewQueryService(f.store, &receipts{}).WithActivity(activity, fixedClock{filedAt})

	page, err := q.List(context.Background(), carolina, filing.ClaimQuery{})

	require.NoError(t, err)
	byID := map[string]domain.ClaimRecord{}
	for _, c := range page.Claims {
		byID[c.ID] = c
	}
	assert.Equal(t, changed, byID[first.ID].LastChangedAt)
	assert.Equal(t, second.FiledAt, byID[second.ID].LastChangedAt, "without a recorded change, when it was filed")
	require.NotNil(t, page.Summary)
	assert.Equal(t, 2, page.Summary.InProgress())
	assert.Equal(t, time.Date(2026, 1, 1, 5, 0, 0, 0, time.UTC), activity.since, "the year of Colombia")

	got, err := q.Get(as(carolina, security.RoleInsured), first.ID)
	require.NoError(t, err)
	assert.Equal(t, changed, got.LastChangedAt)
}

func TestWithoutActivityTheListIsAsBefore(t *testing.T) {
	f := newWebFixture()
	_, _, err := f.web.File(context.Background(), carolina, "form-1", f.collision(t))
	require.NoError(t, err)

	page, err := filing.NewQueryService(f.store, &receipts{}).List(context.Background(), carolina, filing.ClaimQuery{})

	require.NoError(t, err)
	assert.Nil(t, page.Summary)
	assert.True(t, page.Claims[0].LastChangedAt.IsZero())
}

func TestAnActivityThatCannotBeReadIsAnError(t *testing.T) {
	f := newWebFixture()
	c, _, err := f.web.File(context.Background(), carolina, "form-1", f.collision(t))
	require.NoError(t, err)

	q := filing.NewQueryService(f.store, &receipts{}).WithActivity(&fakeActivity{err: errBroken}, fixedClock{filedAt})
	_, err = q.List(context.Background(), carolina, filing.ClaimQuery{})
	assert.ErrorIs(t, err, errBroken)
	_, err = q.Get(as(carolina, security.RoleInsured), c.ID)
	assert.ErrorIs(t, err, errBroken)

	q = filing.NewQueryService(f.store, &receipts{}).WithActivity(&fakeActivity{tallyErr: errBroken}, fixedClock{filedAt})
	_, err = q.List(context.Background(), carolina, filing.ClaimQuery{})
	assert.ErrorIs(t, err, errBroken)
}

func TestTheInsuredDownloadsASupportOfTheirClaim(t *testing.T) {
	f := newWebFixture()
	c, _, err := f.web.File(context.Background(), carolina, "form-1", f.collision(t))
	require.NoError(t, err)
	support := c.Supports[0].File
	q := filing.NewQueryService(f.store, &receipts{}).WithObjects(f.objects)

	link, err := q.SupportLink(as(carolina, security.RoleInsured), c.ID, support.ID)

	require.NoError(t, err)
	assert.Equal(t, support.ID, link.File.ID)
	assert.Contains(t, link.URL, support.ObjectKey())
	assert.Equal(t, filedAt.Add(filing.SupportLinkTTL), link.ExpiresAt)

	_, err = q.SupportLink(as(carolina, security.RoleInsured), c.ID, "sup-00000000000000000000000000000000")
	assert.ErrorIs(t, err, domain.ErrSupportNotFound, "not a support of the claim")
	_, err = q.SupportLink(as("sub-otro", security.RoleInsured), c.ID, support.ID)
	assert.ErrorIs(t, err, security.ErrForbidden, "only the insured who filed it")

	f.objects.signErr = errBroken
	_, err = q.SupportLink(as(carolina, security.RoleInsured), c.ID, support.ID)
	assert.Error(t, err)
	assert.False(t, errors.Is(err, filing.ErrDownloadsUnavailable))

	_, err = filing.NewQueryService(f.store, &receipts{}).SupportLink(as(carolina, security.RoleInsured), c.ID, support.ID)
	assert.ErrorIs(t, err, filing.ErrDownloadsUnavailable, "without an object store there is nothing to sign")
}
