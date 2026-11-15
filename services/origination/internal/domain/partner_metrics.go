package domain

import (
	"errors"
	"math"
	"time"
)

// HU-W3.2-01: the summary of the integration of a commercial partner on its
// dashboard. The partner chooses a range of civil days of Colombia and sees
// how many quotes it requested through the embedded channel, how many of them
// became policies and the conversion between both.

// MaxPartnerMetricsDays bounds the range of the summary: one year (a leap one
// included) is enough for a business decision and keeps the count cheap.
const MaxPartnerMetricsDays = 366

var (
	// ErrPartnerMetricsRangeInverted: the last day of the range is before the
	// first one (CA-2).
	ErrPartnerMetricsRangeInverted = errors.New("the end of the range is before its start")
	// ErrPartnerMetricsRangeTooLong: the range covers more than
	// MaxPartnerMetricsDays days.
	ErrPartnerMetricsRangeTooLong = errors.New("the range of the summary is too long")
	// ErrPartnerMetricsRangeMissing: the range has no first or last day.
	ErrPartnerMetricsRangeMissing = errors.New("the range of the summary needs its first and last day")
)

// PartnerMetricsQuery is the range the partner asks for: its first and last
// civil day of Colombia, both included (CA-2). The partner never sends who it
// is: the id comes from the verified token (CA-4).
type PartnerMetricsQuery struct {
	From time.Time
	To   time.Time
}

// Validate checks the range: both days, the last not before the first and at
// most MaxPartnerMetricsDays days.
func (q PartnerMetricsQuery) Validate() error {
	if q.From.IsZero() || q.To.IsZero() {
		return ErrPartnerMetricsRangeMissing
	}
	if q.To.Before(q.From) {
		return ErrPartnerMetricsRangeInverted
	}
	if q.Days() > MaxPartnerMetricsDays {
		return ErrPartnerMetricsRangeTooLong
	}
	return nil
}

// Days is how many civil days the range covers, both ends included.
func (q PartnerMetricsQuery) Days() int {
	return int(math.Round(CivilDayOf(q.To).Sub(CivilDayOf(q.From)).Hours()/24)) + 1
}

// Interval is the range as instants, [start, end): from the first instant of
// its first day to the first instant of the day after its last one, so the
// last day counts whole.
func (q PartnerMetricsQuery) Interval() (start, end time.Time) {
	return CivilDayOf(q.From), CivilDayOf(q.To).AddDate(0, 0, 1)
}

// PartnerActivity is what the partner did in a range: the quotes it requested
// and the policies issued from them.
type PartnerActivity struct {
	QuotesRequested int
	PoliciesIssued  int
}

// PartnerSummary is the summary of the dashboard (CA-1).
type PartnerSummary struct {
	PartnerID string
	From      time.Time
	To        time.Time
	PartnerActivity
	// ConversionRate is PoliciesIssued / QuotesRequested × 100 with one
	// decimal, and 0 when there are no quotes (CA-1, CA-3).
	ConversionRate float64
}

// NewPartnerSummary builds the summary of a range from its activity.
func NewPartnerSummary(partnerID string, q PartnerMetricsQuery, a PartnerActivity) PartnerSummary {
	return PartnerSummary{
		PartnerID: partnerID, From: CivilDayOf(q.From), To: CivilDayOf(q.To), PartnerActivity: a,
		ConversionRate: ConversionRate(a.PoliciesIssued, a.QuotesRequested),
	}
}

// ConversionRate is issued / quotes × 100 rounded to one decimal; 0 without
// quotes, never a division by zero (CA-1).
func ConversionRate(issued, quotes int) float64 {
	if quotes <= 0 || issued <= 0 {
		return 0
	}
	return math.Round(float64(issued)/float64(quotes)*1000) / 10
}
