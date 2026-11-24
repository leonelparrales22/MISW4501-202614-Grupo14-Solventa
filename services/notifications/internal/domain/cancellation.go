package domain

import (
	"errors"
	"fmt"
	"time"
)

// HU-W1.6-02: the notice that a policy was cancelled for non-payment.

// KindPolicyCancelled is the notification that a policy was cancelled for
// non-payment, with no refund.
const KindPolicyCancelled = "policy_cancelled"

// GroupPolicyCancelled is the thread the device stacks these pushes in.
const GroupPolicyCancelled = "policy_cancelled"

// ReasonNonPayment is the only reason origination cancels a policy for.
const ReasonNonPayment = "non_payment"

// ErrInvalidCancellation is returned for a cancellation that misses data the
// notification must carry.
var ErrInvalidCancellation = errors.New("invalid policy cancellation")

// PolicyCancellation is the cancellation of a policy as origination reports
// it (event PolicyCancelledForNonPayment). There is never a refund.
type PolicyCancellation struct {
	PolicyID string
	// Subject is the insured: the sub of their Cognito account.
	Subject string
	Reason  string
	Line    string
	// InstallmentSeq is the month of the premium that was not paid, due at
	// DueAt; the grace period ended at GraceEndsAt.
	InstallmentSeq int
	DueAt          time.Time
	GraceEndsAt    time.Time
	CancelledAt    time.Time
	AmountMinor    int64
	Currency       string
}

// Validate reports why the cancellation cannot become a notification.
func (c PolicyCancellation) Validate() error {
	switch {
	case !validID(c.PolicyID) || !validID(c.Subject):
		return fmt.Errorf("%w: policy_id and subject are required (up to %d characters)", ErrInvalidCancellation, maxIDLength)
	case c.Reason != ReasonNonPayment:
		return fmt.Errorf("%w: unknown reason %q", ErrInvalidCancellation, c.Reason)
	case c.InstallmentSeq < 2:
		return fmt.Errorf("%w: the unpaid month is 2 or later", ErrInvalidCancellation)
	case c.DueAt.IsZero() || c.CancelledAt.IsZero() || !c.CancelledAt.After(c.DueAt):
		return fmt.Errorf("%w: the policy is cancelled after the month fell due", ErrInvalidCancellation)
	case c.AmountMinor <= 0 || c.Currency == "":
		return fmt.Errorf("%w: amount_minor and currency are required", ErrInvalidCancellation)
	case len(CancellationNotificationIDFor(c.PolicyID)) > maxIDLength:
		return fmt.Errorf("%w: policy_id is too long for the id of the notification", ErrInvalidCancellation)
	}
	return nil
}

// CancellationNotificationIDFor is the id of the notification of a
// cancellation: a policy is cancelled once, so a redelivered event finds the
// one it already created.
func CancellationNotificationIDFor(policyID string) string { return "ntf-cancelled-" + policyID }

// NewCancellationNotification builds the notification of a cancellation
// recorded at now; it occurred when the policy was cancelled.
func NewCancellationNotification(c PolicyCancellation, now time.Time) Notification {
	cancellation := c
	return Notification{
		ID: CancellationNotificationIDFor(c.PolicyID), Subject: c.Subject, Kind: KindPolicyCancelled,
		OccurredAt: c.CancelledAt.UTC(), CreatedAt: now.UTC(), Cancellation: &cancellation,
	}
}
