package resilience

import (
	"errors"
	"fmt"
	"time"
)

// Defaults of the retry policy validated by experiment EC-D02 (HA-D02,
// decision D-4 A): the wait starts at 500 ms, doubles after each failure up
// to 60 s, and the retries stop when the 24-hour window ends.
const (
	DefaultRetryBase   = 500 * time.Millisecond
	DefaultRetryMax    = time.Minute
	DefaultRetryWindow = 24 * time.Hour
)

// RetryPolicy is the growing wait between the attempts of an operation that
// must eventually succeed, such as charging a premium (HA-D02) or paying a
// claim. The wait after the first failed attempt is Base; it doubles after
// every further failure up to Max. Window, when set, bounds the retries: no
// attempt is scheduled after the window ends, so the operation reaches a
// terminal state instead of waiting forever.
type RetryPolicy struct {
	// Base is the wait after the first failed attempt.
	Base time.Duration
	// Max caps the wait between two attempts.
	Max time.Duration
	// Window is how long the retries last, counted from the start the caller
	// passes to WindowEnd. Zero means no window: retry until it succeeds.
	Window time.Duration
}

// DefaultRetryPolicy returns the policy validated by EC-D02: 500 ms doubling
// up to 60 s within a 24-hour window.
func DefaultRetryPolicy() RetryPolicy {
	return RetryPolicy{Base: DefaultRetryBase, Max: DefaultRetryMax, Window: DefaultRetryWindow}
}

// Validate reports why the policy cannot be used.
func (p RetryPolicy) Validate() error {
	switch {
	case p.Base <= 0:
		return errors.New("retry policy: the base wait must be positive")
	case p.Max < p.Base:
		return fmt.Errorf("retry policy: the maximum wait (%s) must not be lower than the base wait (%s)", p.Max, p.Base)
	case p.Window < 0:
		return errors.New("retry policy: the window must not be negative")
	}
	return nil
}

// Backoff returns the wait after the given number of failed attempts: Base
// after the first, doubled after each further failure, capped at Max. A
// count below 1 is treated as 1.
func (p RetryPolicy) Backoff(failedAttempts int) time.Duration {
	wait := p.Base
	for i := 1; i < failedAttempts && wait < p.Max; i++ {
		if wait > p.Max/2 {
			// Doubling would reach or pass the cap (and could overflow).
			return p.Max
		}
		wait *= 2
	}
	return min(wait, p.Max)
}

// WindowEnd returns when the retry window that starts at start ends, or the
// zero time when the policy has no window.
func (p RetryPolicy) WindowEnd(start time.Time) time.Time {
	if p.Window <= 0 {
		return time.Time{}
	}
	return start.Add(p.Window)
}

// Next returns when the next attempt runs after failedAttempts failures
// observed at now, given the end of the retry window (the zero time for no
// window). The last attempt is moved to the end of the window, so the whole
// window is used. exhausted is true when the window is already over at now:
// no attempt is left and the operation must be marked as failed.
func (p RetryPolicy) Next(failedAttempts int, windowEnd, now time.Time) (next time.Time, exhausted bool) {
	next = now.Add(p.Backoff(failedAttempts))
	if windowEnd.IsZero() {
		return next, false
	}
	if !now.Before(windowEnd) {
		return time.Time{}, true
	}
	if next.After(windowEnd) {
		next = windowEnd
	}
	return next, false
}
