package domain

import (
	"errors"
	"fmt"
	"strconv"
	"time"
)

// HU-M3.2-01: the alerts 30, 15 and 5 days before a policy of the insured
// expires.

// KindPolicyExpiry is the notification that a policy is about to expire.
const KindPolicyExpiry = "policy_expiry"

// GroupPolicyExpiry is the thread the device stacks these pushes in.
const GroupPolicyExpiry = "policy_expiry"

// The sources of the policies, as origination reports them.
const (
	ExpirySourceOrigination = "origination"
	ExpirySourcePolicyCore  = "policy_core"
)

// ErrInvalidExpiryAlert is returned for an expiry alert that misses data the
// notification must carry.
var ErrInvalidExpiryAlert = errors.New("invalid policy expiry alert")

// expiryZone is the zone the expiry date is read in: the date of the id of
// the notification is the one the insured sees.
var expiryZone = time.FixedZone("COT", -5*60*60)

// ExpiryAlert is the alert that a policy reached a milestone before its end,
// as origination reports it (event PolicyExpiryApproaching).
type ExpiryAlert struct {
	PolicyID string
	// Subject is the insured: the sub of their Cognito account.
	Subject string
	Source  string
	// Line is the kind of insurance (life, auto, home, travel).
	Line string
	// Product and InsuredObject are empty when the source does not name them.
	Product       string
	InsuredObject string
	// CoverageEnd is when the coverage ends: the exact date the alert shows
	// (criterion 1).
	CoverageEnd time.Time
	// MilestoneDays is the milestone reached (30, 15 or 5) and DaysLeft the
	// calendar days left when it was detected.
	MilestoneDays int
	DaysLeft      int
	DetectedAt    time.Time
}

// Validate reports why the alert cannot become a notification.
func (e ExpiryAlert) Validate() error {
	switch {
	case !validID(e.PolicyID) || !validID(e.Subject):
		return fmt.Errorf("%w: policy_id and subject are required (up to %d characters)", ErrInvalidExpiryAlert, maxIDLength)
	case e.Source != ExpirySourceOrigination && e.Source != ExpirySourcePolicyCore:
		return fmt.Errorf("%w: unknown source %q", ErrInvalidExpiryAlert, e.Source)
	case e.Line == "":
		return fmt.Errorf("%w: line is required", ErrInvalidExpiryAlert)
	case e.CoverageEnd.IsZero() || e.DetectedAt.IsZero():
		return fmt.Errorf("%w: coverage_end and detected_at are required", ErrInvalidExpiryAlert)
	case e.MilestoneDays != 30 && e.MilestoneDays != 15 && e.MilestoneDays != 5:
		return fmt.Errorf("%w: milestone_days must be 30, 15 or 5", ErrInvalidExpiryAlert)
	case e.DaysLeft < 0 || e.DaysLeft > e.MilestoneDays:
		return fmt.Errorf("%w: days_left must be between 0 and the milestone", ErrInvalidExpiryAlert)
	case len(ExpiryNotificationIDFor(e.PolicyID, e.CoverageEnd, e.MilestoneDays)) > maxIDLength:
		return fmt.Errorf("%w: policy_id is too long for the id of the notification", ErrInvalidExpiryAlert)
	}
	return nil
}

// ExpiryNotificationIDFor is the id of the notification of an alert:
// ntf-expiry-<policy_id>-<end date in Colombia, YYYYMMDD>-<milestone>. One
// per policy, period and milestone, so a redelivered event finds the one it
// already created.
func ExpiryNotificationIDFor(policyID string, coverageEnd time.Time, milestoneDays int) string {
	return "ntf-expiry-" + policyID + "-" + coverageEnd.In(expiryZone).Format("20060102") + "-" + strconv.Itoa(milestoneDays)
}

// NewExpiryNotification builds the notification of an alert recorded at now;
// it occurred when origination detected it.
func NewExpiryNotification(e ExpiryAlert, now time.Time) Notification {
	alert := e
	return Notification{
		ID: ExpiryNotificationIDFor(e.PolicyID, e.CoverageEnd, e.MilestoneDays), Subject: e.Subject, Kind: KindPolicyExpiry,
		OccurredAt: e.DetectedAt.UTC(), CreatedAt: now.UTC(), Expiry: &alert,
	}
}
