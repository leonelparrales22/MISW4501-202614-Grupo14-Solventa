package domain

import (
	"fmt"
	"time"
)

// IssuanceStatus is where an issuance is in the journey from the acceptance to
// the active policy, as operations see it (HU-W1.3-04). It is derived from the
// acceptance and its premium charge, never stored.
type IssuanceStatus string

const (
	// IssuanceAccepted: the customer accepted and no charge exists yet
	// (underwriting is pending).
	IssuanceAccepted IssuanceStatus = "accepted"
	// IssuanceCharging: the premium charge is pending, in progress or waiting
	// for a payment method.
	IssuanceCharging IssuanceStatus = "charging"
	// IssuanceActive: the charge was confirmed and the policy is active; the
	// contractual document or its notification are still on their way.
	IssuanceActive IssuanceStatus = "active"
	// IssuanceNotified: the insured has their contractual document and was
	// notified. It is the end of the journey.
	IssuanceNotified IssuanceStatus = "notified"
	// IssuanceException: the premium charge or the contractual issuance
	// failed and needs an analyst.
	IssuanceException IssuanceStatus = "exception"
)

// IssuanceStatuses lists the statuses a filter accepts.
var IssuanceStatuses = []IssuanceStatus{IssuanceAccepted, IssuanceCharging, IssuanceActive, IssuanceNotified, IssuanceException}

// IssuanceStep is the step of the journey an issuance is at, or where it
// failed when its status is exception.
type IssuanceStep string

const (
	StepUnderwriting          IssuanceStep = "underwriting"
	StepAwaitingPaymentMethod IssuanceStep = "awaiting_payment_method"
	StepPremiumCharge         IssuanceStep = IssuanceStep(IssuanceStepPremiumCharge)
	StepContractDocument      IssuanceStep = "contract_document"
	StepNotification          IssuanceStep = "notification"
	StepCompleted             IssuanceStep = "completed"
)

// IssuanceFailureReason tells why an issuance is an exception: the reasons of
// the premium charge plus those of the contractual issuance, where the
// document generator or the e-mail channel kept failing.
type IssuanceFailureReason string

const (
	ReasonRetryWindowExpired      IssuanceFailureReason = IssuanceFailureReason(ChargeRetryWindowExpired)
	ReasonPaymentRejected         IssuanceFailureReason = IssuanceFailureReason(ChargePaymentRejected)
	ReasonPaymentMethodMissing    IssuanceFailureReason = IssuanceFailureReason(ChargePaymentMethodMissing)
	ReasonDocumentGenerationFails IssuanceFailureReason = "document_generation_failed"
	ReasonNotificationFailed      IssuanceFailureReason = "notification_failed"
)

// Valid tells whether s is one of the known statuses.
func (s IssuanceStatus) Valid() bool {
	for _, k := range IssuanceStatuses {
		if s == k {
			return true
		}
	}
	return false
}

// IssuanceStatusOf maps the status of a charge to the one operations see.
func IssuanceStatusOf(c ChargeStatus) IssuanceStatus {
	switch c {
	case ChargeConfirmed:
		return IssuanceActive
	case ChargeFailed:
		return IssuanceException
	default:
		return IssuanceCharging
	}
}

// EscalationAfter is how long an exception may wait for an analyst before the
// monitor flags it as unattended (the 30 minutes of the refinement of
// HU-W1.3-04). It only flags: no alert is sent.
const EscalationAfter = 30 * time.Minute

// Issuance is one row of the monitor: an accepted offer on its way to an
// active policy.
type Issuance struct {
	AcceptanceID string
	OfferID      string
	CustomerID   string
	// CustomerName is the holder's name, empty if origination does not know it.
	CustomerName string
	Channel      Channel
	Status       IssuanceStatus
	// Step is where the issuance is, or failed.
	Step       IssuanceStep
	AcceptedAt time.Time
	UpdatedAt  time.Time

	// The following come from the premium charge; they are zero while the
	// issuance is only accepted.
	AmountMinor int64
	Currency    string
	Attempts    int
	PolicyID    string
	// FailureReason and FailedAt are set while the status is exception.
	FailureReason IssuanceFailureReason
	FailedAt      time.Time
	// NotifiedAt is when the insured was notified, for a notified issuance.
	NotifiedAt    time.Time
	ResumeCount   int
	LastResumedAt time.Time
	LastResumedBy string

	// Unattended is set by the service from the clock: see IsUnattended.
	Unattended bool
}

// IsUnattended tells whether an exception has waited for an analyst longer
// than EscalationAfter at now.
func (i Issuance) IsUnattended(now time.Time) bool {
	return i.Status == IssuanceException && !i.FailedAt.IsZero() && now.Sub(i.FailedAt) >= EscalationAfter
}

// IssuanceFilter narrows the list of the monitor. The zero value lists
// everything.
type IssuanceFilter struct {
	Status IssuanceStatus
	// Channel is where the customer accepted: web or mobile.
	Channel Channel
	// From and To bound the date of the acceptance, both inclusive; zero is
	// open.
	From  time.Time
	To    time.Time
	Limit int
}

// Validate checks the filter.
func (f IssuanceFilter) Validate() error {
	if f.Status != "" && !f.Status.Valid() {
		return fmt.Errorf("unknown issuance status %q", f.Status)
	}
	if f.Channel != "" && f.Channel != ChannelWeb && f.Channel != ChannelMobile {
		return fmt.Errorf("unknown channel %q", f.Channel)
	}
	if !f.From.IsZero() && !f.To.IsZero() && f.To.Before(f.From) {
		return fmt.Errorf("to must not be before from")
	}
	return nil
}

// IssuanceCounts summarize every issuance, whatever the filter of the list:
// the cards on top of the monitor.
type IssuanceCounts struct {
	Accepted int
	Charging int
	// ChargingWithRetries are the charging issuances whose gateway already
	// failed at least once.
	ChargingWithRetries int
	// ActiveToday are the policies whose coverage was activated since the
	// start of the day of the caller, whatever happened to their document or
	// notice afterwards; NotifiedToday those notified since.
	ActiveToday   int
	NotifiedToday int
	Exceptions    int
	// Unattended are the exceptions that waited EscalationAfter or more.
	Unattended int
}

// IssuanceActionKind is what an analyst did.
type IssuanceActionKind string

const (
	// IssuanceActionResume is the retry of a failed charge.
	IssuanceActionResume IssuanceActionKind = "resume"
	// IssuanceActionRetry is the retry of a failed contractual issuance.
	IssuanceActionRetry IssuanceActionKind = "retry"
)

// IssuanceAction is an action of an operations analyst on an issuance.
type IssuanceAction struct {
	ID           int64
	AcceptanceID string
	Action       IssuanceActionKind
	Actor        string
	Note         string
	CreatedAt    time.Time
}

// IssuanceRetry is an operations analyst retrying a failed contractual
// issuance.
type IssuanceRetry struct {
	By   string
	Note string
	At   time.Time
}
