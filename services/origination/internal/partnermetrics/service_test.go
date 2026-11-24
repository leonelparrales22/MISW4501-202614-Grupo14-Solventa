package partnermetrics_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/partnermetrics"
)

// HU-W3.2-01: the summary of the dashboard of a partner.

type activity struct {
	got   string
	a     domain.PartnerActivity
	err   error
	calls int
}

func (f *activity) CountPartnerActivity(_ context.Context, partnerID string, _, _ time.Time) (domain.PartnerActivity, error) {
	f.calls++
	f.got = partnerID
	return f.a, f.err
}

func rangeOf(t *testing.T, from, to string) domain.PartnerMetricsQuery {
	t.Helper()
	f, err := domain.ParseCivilDay(from)
	require.NoError(t, err)
	l, err := domain.ParseCivilDay(to)
	require.NoError(t, err)
	return domain.PartnerMetricsQuery{From: f, To: l}
}

func TestTheSummaryOfThePartner(t *testing.T) {
	store := &activity{a: domain.PartnerActivity{QuotesRequested: 9_820, PoliciesIssued: 1_610}}

	s, err := partnermetrics.NewService(store).Summary(context.Background(), "socio-a", rangeOf(t, "2026-08-18", "2026-09-16"))

	require.NoError(t, err)
	assert.Equal(t, "socio-a", store.got)
	assert.Equal(t, 16.4, s.ConversionRate)
	assert.Equal(t, 9_820, s.QuotesRequested)
}

func TestWithoutAPartnerNothingIsRead(t *testing.T) {
	store := &activity{}

	_, err := partnermetrics.NewService(store).Summary(context.Background(), "  ", rangeOf(t, "2026-09-16", "2026-09-16"))

	assert.ErrorIs(t, err, partnermetrics.ErrNoPartner)
	assert.Zero(t, store.calls, "CA-4: no partner, no data")
}

func TestAnInvertedRangeIsNotRead(t *testing.T) {
	store := &activity{}

	_, err := partnermetrics.NewService(store).Summary(context.Background(), "socio-a", rangeOf(t, "2026-09-17", "2026-09-16"))

	assert.ErrorIs(t, err, domain.ErrPartnerMetricsRangeInverted)
	assert.Zero(t, store.calls)
}

func TestAFailureOfTheStoreIsReturned(t *testing.T) {
	_, err := partnermetrics.NewService(&activity{err: errors.New("down")}).
		Summary(context.Background(), "socio-a", rangeOf(t, "2026-09-16", "2026-09-16"))

	assert.Error(t, err)
}
