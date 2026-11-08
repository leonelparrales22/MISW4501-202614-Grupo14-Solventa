package domain

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// HU-W1.5-01: the list of the policies about to expire that the advisor works
// from (screen S6), the entry point of the assisted renewal (HU-W1.5-03).
//
// It reuses the renewal window of HU-W1.5-02 (RenewalPolicy): the default
// range is the window, and a policy can be renewed from the list only inside
// it. There is no other notion of "about to expire".

// Limits of the list of policies about to expire.
const (
	// DefaultExpiringPageSize is the page size when the advisor does not ask
	// for one.
	DefaultExpiringPageSize = 20
	// MaxExpiringPageSize bounds a page.
	MaxExpiringPageSize = 50
	// maxExpiringPage bounds the page number (a 32-bit integer), so the
	// offset never overflows.
	maxExpiringPage = 1<<31 - 1
)

// ColombiaTime is the time zone of the civil days of the filters: Colombia,
// UTC-5 all year (no daylight saving time). Origination had none; claims and
// audit keep their own, and a service never imports another.
var ColombiaTime = time.FixedZone("UTC-5", -5*60*60)

// CivilDayLayout is the shape of a date of the filters: a civil day of
// Colombia (AAAA-MM-DD).
const CivilDayLayout = "2006-01-02"

// ExpiringStatus is where the current period of a policy about to expire
// stands, as the list shows it. The list only has policies whose period is
// still open (decision D-3 a): a renewed or not renewed period leaves it.
type ExpiringStatus string

const (
	// ExpiringStatusExpiring: the current period has no renewal yet ("Por
	// vencer").
	ExpiringStatusExpiring ExpiringStatus = "expiring"
	// ExpiringStatusRenewalInProgress: the renewal of the current period was
	// accepted and its premium is being charged ("Renovación en curso").
	ExpiringStatusRenewalInProgress ExpiringStatus = "renewal_in_progress"
)

// Errors of the query of the list.
var (
	// ErrExpiringRangeInverted: the last day of the range is before the first.
	ErrExpiringRangeInverted = errors.New("the end of the expiration range is before its start")
	// ErrInvalidExpiringQuery: a status, page or page size the list does not
	// understand.
	ErrInvalidExpiringQuery = errors.New("the query of the expiring policies is not valid")
)

// ParseExpiringStatus reads the status of the filter: empty is every status.
func ParseExpiringStatus(raw string) (ExpiringStatus, error) {
	switch s := ExpiringStatus(strings.TrimSpace(raw)); s {
	case "", ExpiringStatusExpiring, ExpiringStatusRenewalInProgress:
		return s, nil
	default:
		return "", fmt.Errorf("%w: status must be %s or %s", ErrInvalidExpiringQuery,
			ExpiringStatusExpiring, ExpiringStatusRenewalInProgress)
	}
}

// ExpiringStatusOf is the status of a policy whose current period has the
// given renewal (nil when it has none). It reports false when the period is
// already closed (renewed or not renewed): that policy is not listed.
func ExpiringStatusOf(r *Renewal) (ExpiringStatus, bool) {
	switch {
	case r == nil:
		return ExpiringStatusExpiring, true
	case r.Status == RenewalAccepted:
		return ExpiringStatusRenewalInProgress, true
	default:
		return "", false
	}
}

// ParseCivilDay reads a civil day of Colombia (AAAA-MM-DD) as its first
// instant.
func ParseCivilDay(raw string) (time.Time, error) {
	d, err := time.ParseInLocation(CivilDayLayout, strings.TrimSpace(raw), ColombiaTime)
	if err != nil {
		return time.Time{}, fmt.Errorf("%q is not a civil day (AAAA-MM-DD): %w", raw, err)
	}
	return d, nil
}

// CivilDayOf is the first instant of the civil day of Colombia that contains t.
func CivilDayOf(t time.Time) time.Time {
	y, m, d := t.In(ColombiaTime).Date()
	return time.Date(y, m, d, 0, 0, 0, 0, ColombiaTime)
}

// DefaultExpiringRange is the range of the list when the advisor does not
// choose one: from today to today plus the renewal window (civil days of
// Colombia), the same window in which the insured can renew (HU-W1.5-02).
func DefaultExpiringRange(now time.Time, rules RenewalPolicy) (from, to time.Time) {
	from = CivilDayOf(now)
	return from, from.AddDate(0, 0, rules.WindowDays)
}

// ExpiringQuery is the query of the list of policies about to expire.
type ExpiringQuery struct {
	// From and To are the first and the last civil day of the range of
	// expiration (both included), as their first instant in ColombiaTime.
	// Zero means the default (WithDefaults).
	From time.Time
	To   time.Time
	// Status is the status asked for; empty for both.
	Status ExpiringStatus
	// Page starts at 1; 0 means the first page.
	Page int
	// PageSize is 1 to MaxExpiringPageSize; 0 means DefaultExpiringPageSize.
	PageSize int
}

// WithDefaults fills what the advisor did not choose: the range is today to
// today plus the renewal window; with only the first day, the range lasts the
// renewal window from it; with only the last day, it starts today. The first
// page and DefaultExpiringPageSize complete it.
func (q ExpiringQuery) WithDefaults(now time.Time, rules RenewalPolicy) ExpiringQuery {
	today, _ := DefaultExpiringRange(now, rules)
	switch {
	case q.From.IsZero() && q.To.IsZero():
		q.From, q.To = DefaultExpiringRange(now, rules)
	case q.From.IsZero():
		q.From = today
	case q.To.IsZero():
		q.To = CivilDayOf(q.From).AddDate(0, 0, rules.WindowDays)
	}
	if q.Page == 0 {
		q.Page = 1
	}
	if q.PageSize == 0 {
		q.PageSize = DefaultExpiringPageSize
	}
	return q
}

// Validate checks a query with its defaults applied: a range whose last day
// is not before its first (ErrExpiringRangeInverted), a known status, a page
// of 1 or more and a page size of 1 to MaxExpiringPageSize
// (ErrInvalidExpiringQuery).
func (q ExpiringQuery) Validate() error {
	switch {
	case q.From.IsZero() || q.To.IsZero():
		return fmt.Errorf("%w: the range needs its first and last day", ErrInvalidExpiringQuery)
	case CivilDayOf(q.To).Before(CivilDayOf(q.From)):
		return ErrExpiringRangeInverted
	case q.Page < 1 || q.Page > maxExpiringPage:
		return fmt.Errorf("%w: page must be 1 or more", ErrInvalidExpiringQuery)
	case q.PageSize < 1 || q.PageSize > MaxExpiringPageSize:
		return fmt.Errorf("%w: page_size must be between 1 and %d", ErrInvalidExpiringQuery, MaxExpiringPageSize)
	}
	_, err := ParseExpiringStatus(string(q.Status))
	return err
}

// Bounds is the range of the query as instants in UTC: from the first instant
// of the first day (included) to the first instant after the last day
// (excluded). Colombia has no daylight saving time, so a day is always 24
// hours.
func (q ExpiringQuery) Bounds() (start, end time.Time) {
	return CivilDayOf(q.From).UTC(), CivilDayOf(q.To).AddDate(0, 0, 1).UTC()
}

// Includes tells whether a coverage that ends at end falls inside the range of
// the query. It is the reference in Go of the condition of the SQL query.
func (q ExpiringQuery) Includes(end time.Time) bool {
	start, stop := q.Bounds()
	return !end.Before(start) && end.Before(stop)
}

// Offset is the number of matching policies before the page.
func (q ExpiringQuery) Offset() int { return (q.Page - 1) * q.PageSize }

// ExpiringPolicy is one row of the list: a policy about to expire, the renewal
// of its current period (nil when it has none) and its status. CanRenew and
// the window are what the advisor can do with it now (decision D-5 a: only
// inside the renewal window of HU-W1.5-02).
type ExpiringPolicy struct {
	Policy         Policy
	Renewal        *Renewal
	Status         ExpiringStatus
	CanRenew       bool
	WindowOpensAt  time.Time
	WindowClosesAt time.Time
}

// ExpiringPage is a page of the list with what the screen shows around it:
// the total that match the query, how many policies with an open period end
// within the renewal window (the indicator, whatever the filter), the range
// that was applied and the window.
type ExpiringPage struct {
	Items           []ExpiringPolicy
	Total           int
	DueWithinWindow int
	From            time.Time
	To              time.Time
	WindowDays      int
	Page            int
	PageSize        int
}
