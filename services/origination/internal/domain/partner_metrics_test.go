package domain_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
)

// HU-W3.2-01: the rules of the summary of the dashboard of a partner.

func day(t *testing.T, raw string) time.Time {
	t.Helper()
	d, err := domain.ParseCivilDay(raw)
	require.NoError(t, err)
	return d
}

func TestTheConversionIsPoliciesOverQuotesWithOneDecimal(t *testing.T) {
	assert.Equal(t, 15.0, domain.ConversionRate(1_872, 12_480))
	assert.Equal(t, 16.4, domain.ConversionRate(1_610, 9_820))
	assert.Equal(t, 33.3, domain.ConversionRate(1, 3))
	assert.Equal(t, 100.0, domain.ConversionRate(4, 4))
}

func TestWithoutQuotesTheConversionIsZero(t *testing.T) {
	assert.Equal(t, 0.0, domain.ConversionRate(0, 0), "CA-1: 0 % without quotes, no division by zero")
	assert.Equal(t, 0.0, domain.ConversionRate(3, 0))
	assert.Equal(t, 0.0, domain.ConversionRate(0, 10))
}

func TestTheRangeIncludesItsFirstAndLastDay(t *testing.T) {
	q := domain.PartnerMetricsQuery{From: day(t, "2026-09-10"), To: day(t, "2026-09-16")}

	start, end := q.Interval()

	require.NoError(t, q.Validate())
	assert.Equal(t, 7, q.Days())
	assert.Equal(t, time.Date(2026, 9, 10, 5, 0, 0, 0, time.UTC), start.UTC())
	assert.Equal(t, time.Date(2026, 9, 17, 5, 0, 0, 0, time.UTC), end.UTC())
}

func TestASingleDayIsAValidRange(t *testing.T) {
	q := domain.PartnerMetricsQuery{From: day(t, "2026-09-16"), To: day(t, "2026-09-16")}

	assert.NoError(t, q.Validate())
	assert.Equal(t, 1, q.Days())
}

func TestTheRangeRules(t *testing.T) {
	assert.ErrorIs(t, domain.PartnerMetricsQuery{From: day(t, "2026-09-17"), To: day(t, "2026-09-16")}.Validate(),
		domain.ErrPartnerMetricsRangeInverted, "CA-2: the first day cannot be after the last")
	assert.ErrorIs(t, domain.PartnerMetricsQuery{To: day(t, "2026-09-16")}.Validate(), domain.ErrPartnerMetricsRangeMissing)
	assert.ErrorIs(t, domain.PartnerMetricsQuery{From: day(t, "2025-09-15"), To: day(t, "2026-09-16")}.Validate(),
		domain.ErrPartnerMetricsRangeTooLong)
	assert.NoError(t, domain.PartnerMetricsQuery{From: day(t, "2025-09-16"), To: day(t, "2026-09-16")}.Validate(),
		"366 days is the longest range")
}

func TestTheSummaryCarriesTheRangeAndTheConversion(t *testing.T) {
	q := domain.PartnerMetricsQuery{From: day(t, "2026-08-18"), To: day(t, "2026-09-16")}

	s := domain.NewPartnerSummary("socio-a", q, domain.PartnerActivity{QuotesRequested: 560, PoliciesIssued: 22})

	assert.Equal(t, "socio-a", s.PartnerID)
	assert.Equal(t, "2026-08-18", s.From.Format(domain.CivilDayLayout))
	assert.Equal(t, "2026-09-16", s.To.Format(domain.CivilDayLayout))
	assert.Equal(t, 3.9, s.ConversionRate)
}
