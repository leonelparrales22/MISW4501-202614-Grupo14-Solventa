package domain_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
)

func civilDay(t *testing.T, raw string) time.Time {
	t.Helper()
	d, err := domain.ParseCivilDay(raw)
	require.NoError(t, err)
	return d
}

func TestTheDefaultRangeIsTodayInColombiaToTheEndOfTheRenewalWindow(t *testing.T) {
	// 03:00 UTC on 3 October is still 2 October in Colombia (22:00).
	now := time.Date(2026, 10, 3, 3, 0, 0, 0, time.UTC)

	from, to := domain.DefaultExpiringRange(now, domain.DefaultRenewalPolicy)

	assert.Equal(t, "2026-10-02", from.Format(domain.CivilDayLayout))
	assert.Equal(t, "2026-11-01", to.Format(domain.CivilDayLayout), "30 days, the window of HU-W1.5-02")
	assert.Equal(t, domain.ColombiaTime, from.Location())
}

func TestTheDefaultRangeFollowsTheConfiguredWindow(t *testing.T) {
	now := time.Date(2026, 11, 8, 15, 0, 0, 0, time.UTC)
	rules := domain.DefaultRenewalPolicy
	rules.WindowDays = 365

	from, to := domain.DefaultExpiringRange(now, rules)

	assert.Equal(t, "2026-11-08", from.Format(domain.CivilDayLayout))
	assert.Equal(t, "2027-11-08", to.Format(domain.CivilDayLayout))
}

func TestWithDefaultsCompletesWhatTheAdvisorDidNotChoose(t *testing.T) {
	now := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	rules := domain.DefaultRenewalPolicy

	q := domain.ExpiringQuery{}.WithDefaults(now, rules)
	assert.Equal(t, "2026-09-16", q.From.Format(domain.CivilDayLayout))
	assert.Equal(t, "2026-10-16", q.To.Format(domain.CivilDayLayout))
	assert.Equal(t, 1, q.Page)
	assert.Equal(t, domain.DefaultExpiringPageSize, q.PageSize)

	onlyFrom := domain.ExpiringQuery{From: civilDay(t, "2026-12-01")}.WithDefaults(now, rules)
	assert.Equal(t, "2026-12-31", onlyFrom.To.Format(domain.CivilDayLayout), "the range lasts the window from its first day")

	onlyTo := domain.ExpiringQuery{To: civilDay(t, "2026-09-30"), Page: 3, PageSize: 5}.WithDefaults(now, rules)
	assert.Equal(t, "2026-09-16", onlyTo.From.Format(domain.CivilDayLayout), "the range starts today")
	assert.Equal(t, 3, onlyTo.Page)
	assert.Equal(t, 5, onlyTo.PageSize)
}

func TestTheBoundsAreTheWholeCivilDaysOfColombiaInUTC(t *testing.T) {
	q := domain.ExpiringQuery{From: civilDay(t, "2026-10-02"), To: civilDay(t, "2026-10-02")}

	start, end := q.Bounds()

	assert.Equal(t, time.Date(2026, 10, 2, 5, 0, 0, 0, time.UTC), start, "00:00 in Colombia is 05:00 UTC")
	assert.Equal(t, time.Date(2026, 10, 3, 5, 0, 0, 0, time.UTC), end, "the next day starts 24 hours later")
	assert.Equal(t, time.UTC, start.Location())
}

func TestTheRangeIncludesBothDaysWhole(t *testing.T) {
	q := domain.ExpiringQuery{From: civilDay(t, "2026-10-02"), To: civilDay(t, "2026-10-05")}

	for name, tc := range map[string]struct {
		end  time.Time
		want bool
	}{
		"00:00 of the first day in Colombia":             {time.Date(2026, 10, 2, 5, 0, 0, 0, time.UTC), true},
		"the instant before the first day":               {time.Date(2026, 10, 2, 4, 59, 59, 999_999_999, time.UTC), false},
		"23:59:59.999 of the last day in Colombia":       {time.Date(2026, 10, 6, 4, 59, 59, 999_000_000, time.UTC), true},
		"00:00 of the day after in Colombia":             {time.Date(2026, 10, 6, 5, 0, 0, 0, time.UTC), false},
		"after midnight UTC, still the last day locally": {time.Date(2026, 10, 6, 2, 0, 0, 0, time.UTC), true},
		"before 05:00 UTC, still the day before locally": {time.Date(2026, 10, 2, 3, 0, 0, 0, time.UTC), false},
	} {
		assert.Equal(t, tc.want, q.Includes(tc.end), name)
	}
}

func TestParseCivilDayReadsOnlyAAAAMMDD(t *testing.T) {
	d := civilDay(t, " 2026-10-02 ")
	assert.Equal(t, time.Date(2026, 10, 2, 0, 0, 0, 0, domain.ColombiaTime), d)

	for _, raw := range []string{"", "02/10/2026", "2026-10-32", "2026-2-1", "2026-10-02T00:00:00Z", "mañana"} {
		_, err := domain.ParseCivilDay(raw)
		assert.Error(t, err, raw)
	}
}

func TestCivilDayOfUsesColombia(t *testing.T) {
	assert.Equal(t, time.Date(2026, 10, 2, 0, 0, 0, 0, domain.ColombiaTime),
		domain.CivilDayOf(time.Date(2026, 10, 3, 4, 59, 0, 0, time.UTC)))
	assert.Equal(t, time.Date(2026, 10, 3, 0, 0, 0, 0, domain.ColombiaTime),
		domain.CivilDayOf(time.Date(2026, 10, 3, 5, 0, 0, 0, time.UTC)))
}

func TestValidateRefusesWhatTheListCannotAnswer(t *testing.T) {
	from, to := civilDay(t, "2026-10-02"), civilDay(t, "2026-10-05")
	ok := domain.ExpiringQuery{From: from, To: to, Page: 1, PageSize: 20}
	require.NoError(t, ok.Validate())

	sameDay := ok
	sameDay.To = from
	assert.NoError(t, sameDay.Validate(), "a range of a single day")

	inverted := ok
	inverted.From, inverted.To = to, from
	assert.ErrorIs(t, inverted.Validate(), domain.ErrExpiringRangeInverted)

	for name, q := range map[string]domain.ExpiringQuery{
		"no range":       {Page: 1, PageSize: 20},
		"page 0":         {From: from, To: to, Page: 0, PageSize: 20},
		"page too big":   {From: from, To: to, Page: 1 << 31, PageSize: 20},
		"page size 0":    {From: from, To: to, Page: 1, PageSize: 0},
		"page size 51":   {From: from, To: to, Page: 1, PageSize: 51},
		"unknown status": {From: from, To: to, Page: 1, PageSize: 20, Status: "cancelled_non_payment"},
	} {
		assert.ErrorIs(t, q.Validate(), domain.ErrInvalidExpiringQuery, name)
	}
}

func TestTheOffsetSkipsThePreviousPages(t *testing.T) {
	assert.Equal(t, 0, domain.ExpiringQuery{Page: 1, PageSize: 20}.Offset())
	assert.Equal(t, 40, domain.ExpiringQuery{Page: 3, PageSize: 20}.Offset())
}

func TestParseExpiringStatus(t *testing.T) {
	for raw, want := range map[string]domain.ExpiringStatus{
		"": "", "expiring": domain.ExpiringStatusExpiring, " renewal_in_progress ": domain.ExpiringStatusRenewalInProgress,
	} {
		got, err := domain.ParseExpiringStatus(raw)
		require.NoError(t, err, raw)
		assert.Equal(t, want, got, raw)
	}
	for _, raw := range []string{"renewed", "not_renewed", "EXPIRING", "cancelled_non_payment"} {
		_, err := domain.ParseExpiringStatus(raw)
		assert.ErrorIs(t, err, domain.ErrInvalidExpiringQuery, raw)
	}
}

func TestTheStatusComesFromTheRenewalOfTheCurrentPeriod(t *testing.T) {
	got, listed := domain.ExpiringStatusOf(nil)
	assert.True(t, listed)
	assert.Equal(t, domain.ExpiringStatusExpiring, got, "no renewal yet: Por vencer")

	got, listed = domain.ExpiringStatusOf(&domain.Renewal{Status: domain.RenewalAccepted})
	assert.True(t, listed)
	assert.Equal(t, domain.ExpiringStatusRenewalInProgress, got, "accepted and being charged: Renovación en curso")

	for _, closed := range []domain.RenewalStatus{domain.RenewalRenewed, domain.RenewalNotRenewed} {
		_, listed = domain.ExpiringStatusOf(&domain.Renewal{Status: closed})
		assert.False(t, listed, "a closed period leaves the list: %s", closed)
	}
}
