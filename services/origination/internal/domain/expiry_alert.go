package domain

import (
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/events"
)

// HU-M3.2-01: the alerts 30, 15 and 5 days before a policy expires.

// Contract of the PolicyExpiryApproaching event (docs/events/README.md).
const (
	EventTypePolicyExpiryApproaching    = "PolicyExpiryApproaching"
	EventVersionPolicyExpiryApproaching = 1
	// policyExpiryIDPrefix starts the deterministic id of the event: one per
	// policy, period and milestone.
	policyExpiryIDPrefix = "policy-expiry-approaching-"
)

// ExpiryMilestones are the days before the end of the coverage the insured is
// alerted at, from the closest to the farthest.
var ExpiryMilestones = []int{5, 15, 30}

// ExpiryZone is the zone the days left are counted in: the insured reads the
// expiry date where they are, as the texts of the notifications service do.
var ExpiryZone = time.FixedZone("COT", -5*60*60)

// ErrInvalidExpiryAlert is returned for an alert that misses what the
// notification must carry.
var ErrInvalidExpiryAlert = errors.New("invalid policy expiry alert")

// ExpiryDaysLeft is the number of calendar days, in Colombia, from now to the
// end of the coverage: 0 on the day it ends, negative once that day passed.
func ExpiryDaysLeft(now, end time.Time) int {
	return int(calendarDay(end).Sub(calendarDay(now)).Hours() / 24)
}

// calendarDay is the date of t in Colombia, as a UTC midnight.
func calendarDay(t time.Time) time.Time {
	y, m, d := t.In(ExpiryZone).Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// ExpiryMilestone is the last milestone crossed with daysLeft to go: the
// smallest of 30, 15 and 5 that is not below it. A policy first seen 14 days
// before its end gets only the 15-day alert, and the 30-day one is never sent
// afterwards. It answers false with more than 30 days left or once the
// coverage ended.
func ExpiryMilestone(daysLeft int) (int, bool) {
	if daysLeft < 0 {
		return 0, false
	}
	for _, m := range ExpiryMilestones {
		if daysLeft <= m {
			return m, true
		}
	}
	return 0, false
}

// PolicyExpiryApproaching is the alert that an active policy reached a
// milestone before its end (HU-M3.2-01). There is one per policy, period
// (CoverageEnd) and milestone: renewing the policy moves the end and starts
// the milestones again.
type PolicyExpiryApproaching struct {
	PolicyID string
	// Subject is the insured: the sub of their Cognito account.
	Subject string
	Source  PolicySource
	Line    PolicyLine
	// Product and InsuredObject are empty when the source does not name them
	// (origination's life policies).
	Product       string
	InsuredObject string
	CoverageEnd   time.Time
	MilestoneDays int
	DaysLeft      int
	DetectedAt    time.Time
}

// NewPolicyExpiryApproaching builds the alert of a policy of the wallet at
// now, if it is due: the policy is active, its coverage has not ended and it
// crossed a milestone. It does not know whether the milestone was already
// alerted; the store does.
func NewPolicyExpiryApproaching(p WalletPolicy, subject string, now time.Time) (PolicyExpiryApproaching, bool) {
	if p.Status != PolicyActive || !p.CoverageEnd.After(now) {
		return PolicyExpiryApproaching{}, false
	}
	days := ExpiryDaysLeft(now, p.CoverageEnd)
	milestone, ok := ExpiryMilestone(days)
	if !ok {
		return PolicyExpiryApproaching{}, false
	}
	return PolicyExpiryApproaching{
		PolicyID: p.ID, Subject: subject, Source: p.Source, Line: p.Line, Product: p.Product, InsuredObject: p.InsuredObject,
		CoverageEnd: p.CoverageEnd.UTC(), MilestoneDays: milestone, DaysLeft: days, DetectedAt: now.UTC(),
	}, true
}

// Validate reports why the alert cannot be published.
func (e PolicyExpiryApproaching) Validate() error {
	_, crossed := ExpiryMilestone(e.DaysLeft)
	switch {
	case e.PolicyID == "" || e.Subject == "":
		return fmt.Errorf("%w: policy_id and subject are required", ErrInvalidExpiryAlert)
	case e.Source != SourceOrigination && e.Source != SourcePolicyCore:
		return fmt.Errorf("%w: unknown source %q", ErrInvalidExpiryAlert, e.Source)
	case e.Line == "":
		return fmt.Errorf("%w: line is required", ErrInvalidExpiryAlert)
	case e.CoverageEnd.IsZero() || e.DetectedAt.IsZero():
		return fmt.Errorf("%w: coverage_end and detected_at are required", ErrInvalidExpiryAlert)
	case !crossed || !isMilestone(e.MilestoneDays) || e.DaysLeft > e.MilestoneDays:
		return fmt.Errorf("%w: %d days left is not within the milestone of %d days", ErrInvalidExpiryAlert,
			e.DaysLeft, e.MilestoneDays)
	}
	return nil
}

func isMilestone(days int) bool {
	for _, m := range ExpiryMilestones {
		if m == days {
			return true
		}
	}
	return false
}

// EventID is the deterministic id of the alert:
// policy-expiry-approaching-<policy_id>-<end date in Colombia>-<milestone>.
func (e PolicyExpiryApproaching) EventID() string {
	return policyExpiryIDPrefix + e.PolicyID + "-" + e.CoverageEnd.In(ExpiryZone).Format("2006-01-02") + "-" +
		strconv.Itoa(e.MilestoneDays)
}

// policyExpiryPayload is the JSON payload of the event. product and
// insured_object are null when the source does not name them.
type policyExpiryPayload struct {
	PolicyID      string    `json:"policy_id"`
	Subject       string    `json:"subject"`
	Source        string    `json:"source"`
	Line          string    `json:"line"`
	Product       *string   `json:"product"`
	InsuredObject *string   `json:"insured_object"`
	CoverageEnd   time.Time `json:"coverage_end"`
	MilestoneDays int       `json:"milestone_days"`
	DaysLeft      int       `json:"days_left"`
	DetectedAt    time.Time `json:"detected_at"`
}

// Envelope wraps the alert in the envelope of pkg/events; it occurred when it
// was detected.
func (e PolicyExpiryApproaching) Envelope() (events.Envelope, error) {
	if err := e.Validate(); err != nil {
		return events.Envelope{}, err
	}
	return events.New(e.EventID(), EventTypePolicyExpiryApproaching, EventSource, EventVersionPolicyExpiryApproaching,
		e.DetectedAt, policyExpiryPayload{
			PolicyID: e.PolicyID, Subject: e.Subject, Source: string(e.Source), Line: string(e.Line),
			Product: optional(e.Product), InsuredObject: optional(e.InsuredObject), CoverageEnd: e.CoverageEnd.UTC(),
			MilestoneDays: e.MilestoneDays, DaysLeft: e.DaysLeft, DetectedAt: e.DetectedAt.UTC(),
		})
}

func optional(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
