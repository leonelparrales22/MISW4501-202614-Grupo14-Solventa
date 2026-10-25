package domain

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// Policy is an active policy as the insured sees it in the portal (HU-W1.5-02).
// It is a projection of the CoverageActivated event, so it exists from the
// moment the premium is charged.
type Policy struct {
	ID           string
	AcceptanceID string
	// CustomerID is the insured who owns the policy: the subject of their token.
	CustomerID    string
	Holder        Applicant
	Premium       float64
	Currency      string
	InsuredAmount float64
	Coverages     []Coverage
	CoverageStart time.Time
	CoverageEnd   time.Time
	// RenewalOf is the policy this one renews; empty for new business.
	RenewalOf string
}

// PolicyFrom builds the projection of an activation.
func PolicyFrom(e CoverageActivated) Policy {
	return Policy{
		ID: e.PolicyID, AcceptanceID: e.AcceptanceID, CustomerID: e.CustomerID, Holder: e.Holder,
		Premium: e.Premium, Currency: e.Currency, InsuredAmount: e.InsuredAmount,
		Coverages: append([]Coverage(nil), e.Coverages...), CoverageStart: e.CoverageStart, CoverageEnd: e.CoverageEnd,
		RenewalOf: e.RenewalOf,
	}
}

// RenewalPolicy is the business rule of the renewal window and its deadlines
// (HU-W1.5-02): when the insured can renew, how long they have to fix a
// payment that fails, and whether a lapsed policy gets a grace period.
type RenewalPolicy struct {
	// WindowDays is how many days before the end of the coverage the renewal
	// opens. The window closes when the coverage ends.
	WindowDays int
	// TermMonths is the term of a renewed coverage.
	TermMonths int
	// RetryWithin is how long the insured has, from accepting, to get a
	// payment through; after it the policy is not renewed.
	RetryWithin time.Duration
	// GraceDays is the grace period after a lapse (0 = none), told to the
	// insured in the notice.
	GraceDays int
}

// DefaultRenewalPolicy is the window the story asks for when nothing is
// configured: 30 days, a 12 months term, 72 hours to fix a payment, no grace.
var DefaultRenewalPolicy = RenewalPolicy{WindowDays: 30, TermMonths: 12, RetryWithin: 72 * time.Hour}

// Validate checks the configuration.
func (p RenewalPolicy) Validate() error {
	switch {
	case p.WindowDays <= 0 || p.WindowDays > 365:
		return errors.New("the renewal window must be between 1 and 365 days")
	case p.TermMonths <= 0 || p.TermMonths > 600:
		return errors.New("the renewal term must be between 1 and 600 months")
	case p.RetryWithin <= 0:
		return errors.New("the payment retry period must be positive")
	case p.GraceDays < 0 || p.GraceDays > 90:
		return errors.New("the grace period must be between 0 and 90 days")
	}
	return nil
}

// WindowOpensAt is when the policy can start to be renewed.
func (p RenewalPolicy) WindowOpensAt(policy Policy) time.Time {
	return policy.CoverageEnd.AddDate(0, 0, -p.WindowDays)
}

// InWindow tells whether the policy can be renewed at now: the window is open
// from WindowDays before the end of the coverage until it ends.
func (p RenewalPolicy) InWindow(policy Policy, now time.Time) bool {
	return !now.Before(p.WindowOpensAt(policy)) && now.Before(policy.CoverageEnd)
}

// RenewalStatus is where the renewal of one period of a policy is.
type RenewalStatus string

const (
	// RenewalAccepted: the insured accepted the conditions and the premium is
	// being charged; they can still hand over another payment method.
	RenewalAccepted RenewalStatus = "accepted"
	// RenewalRenewed: the premium was charged and the new policy is active.
	RenewalRenewed RenewalStatus = "renewed"
	// RenewalNotRenewed: the window closed without a renewal, or the payment
	// was not fixed in time.
	RenewalNotRenewed RenewalStatus = "not_renewed"
)

// NotRenewedReason tells why a renewal ended as not renewed.
type NotRenewedReason string

const (
	// NotRenewedNoResponse: the insured did not answer before the window closed.
	NotRenewedNoResponse NotRenewedReason = "no_response"
	// NotRenewedPaymentFailed: the insured accepted but the payment did not go
	// through within the retry period.
	NotRenewedPaymentFailed NotRenewedReason = "payment_failed"
)

// Renewal is the renewal of one period of one policy. There is at most one per
// policy and period: it is what makes accepting twice, or accepting while the
// sweep closes the window, produce a single outcome.
type Renewal struct {
	ID         string
	PolicyID   string
	CustomerID string
	// PeriodEnd is the end of the coverage being renewed.
	PeriodEnd time.Time
	Status    RenewalStatus
	Reason    NotRenewedReason

	// The conditions the insured accepted, empty for a lapse.
	Terms        CoverageTerms
	Premium      float64
	Currency     string
	DiscountRate float64
	// Requoted is false when the conditions repeat the current ones because the
	// engine could not price the renewal.
	Requoted bool

	// ChargeID is the acceptance id of the charge that renews the policy.
	ChargeID string
	// NewPolicyID is the policy that the renewal created, once renewed.
	NewPolicyID string

	AcceptedAt time.Time
	// RetryUntil is the deadline to get the payment through.
	RetryUntil time.Time
	ClosedAt   time.Time

	// NotifiedAt is when the insured was told the policy was not renewed.
	NotifiedAt         time.Time
	NotificationFailed bool
	CreatedAt          time.Time
}

// RenewalID is the id of the renewal of a policy for the period that ends at
// periodEnd. It is deterministic, so it is also the idempotency anchor.
func RenewalID(policyID string, periodEnd time.Time) string {
	return "ren-" + policyID + "-" + periodEnd.UTC().Format("20060102")
}

// RenewalChargeID is the id of the charge of a renewal; it plays the role of
// the acceptance id in the charge, the monitor and the payment-method route.
func RenewalChargeID(renewalID string) string { return renewalID }

// IsRenewalCharge tells whether a charge id belongs to a renewal.
func IsRenewalCharge(id string) bool { return strings.HasPrefix(id, "ren-") }

// DiscountRate is the good-experience discount of the renewal (decision of
// HU-W1.5-02: simulated, no claims history is available to origination).
const DiscountRate = 0.08

// MaxNoClaimsYear is the number of claims in the last year that still earns
// the discount: none.
const MaxNoClaimsYear = 0

// RenewalConditions is what the insured sees before accepting: the current
// conditions next to the renewed ones (acceptance criterion 1).
type RenewalConditions struct {
	Policy         Policy
	Current        RenewalTerms
	Renewal        RenewalTerms
	WindowOpensAt  time.Time
	WindowClosesAt time.Time
	// Requoted is true when the engine re-priced the policy with the current
	// profile; false when the conditions repeat the current ones.
	Requoted bool
	// DiscountRate is the discount applied to the renewal, 0 when none.
	DiscountRate float64
}

// RenewalTerms is one side of the comparison.
type RenewalTerms struct {
	Premium       float64
	Currency      string
	InsuredAmount float64
	Coverages     []Coverage
	CoverageStart time.Time
	CoverageEnd   time.Time
}

// Renewal domain errors.
var (
	// ErrRenewalNotAvailable: the policy is outside the renewal window.
	ErrRenewalNotAvailable = errors.New("the policy is not in its renewal window")
	// ErrRenewalClosed: the period of the policy already ended as renewed or
	// not renewed, and the answer does not change.
	ErrRenewalClosed = errors.New("the renewal of this period is already closed")
)

// Validate checks the renewal request of the insured.
func (r Renewal) Validate() error {
	if r.PolicyID == "" || r.CustomerID == "" || r.PeriodEnd.IsZero() {
		return fmt.Errorf("renewal %s: policy, customer and period are required", r.ID)
	}
	return nil
}

// RenewalNotice is the message that tells the insured the policy was not renewed.
type RenewalNotice struct {
	To         string
	HolderName string
	PolicyID   string
	// CoverageEnded is when the coverage ended.
	CoverageEnded time.Time
	Reason        NotRenewedReason
	// GraceDays is the grace period that applies, 0 when none.
	GraceDays int
	// IdempotencyKey is stable for a policy and period, so a retried send does
	// not repeat the message.
	IdempotencyKey string
}

// NewRenewalCharge builds the pending charge of a renewal: the premium the
// insured accepted, for the term of the renewal, in a retry window that ends
// at the deadline to fix the payment. Its coverage starts when the renewed
// policy ends. It is not due until a payment method is attached.
func NewRenewalCharge(p Policy, r Renewal, now time.Time) Charge {
	key := ChargeKeyFor(r.ChargeID, 1)
	return Charge{
		AcceptanceID:    r.ChargeID,
		IdempotencyKey:  key,
		OfferID:         r.ID,
		CustomerID:      p.CustomerID,
		Channel:         ChannelRenewal,
		AcceptedAt:      r.AcceptedAt,
		Holder:          p.Holder,
		AmountMinor:     ToMinorUnits(r.Premium),
		Currency:        r.Currency,
		Terms:           copyTerms(r.Terms),
		Status:          ChargePending,
		WindowStartedAt: r.AcceptedAt,
		WindowEndsAt:    r.RetryUntil,
		RenewalOf:       p.ID,
		// The renewed coverage continues the old one: no gap and no overlap.
		CoverageNotBefore: p.CoverageEnd,
		CreatedAt:         now,
		UpdatedAt:         now,
	}
}
