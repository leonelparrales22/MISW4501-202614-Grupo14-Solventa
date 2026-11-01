// Package domain holds the model of the notifications service: what the
// insured is told, where they are reached and whether each push arrived
// (HU-M3.1-01).
package domain

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// KindParametricPayout is the notification of an automatic parametric payment.
const KindParametricPayout = "parametric_payout"

// GroupParametricPayouts is the thread the device stacks these pushes in:
// several payments the same day each keep their own entry (criterion 6).
const GroupParametricPayouts = "parametric_payouts"

// maxIDLength bounds every identifier the service stores.
const maxIDLength = 128

// ErrInvalidPayout is returned for a payment event that misses data the
// notification must carry.
var ErrInvalidPayout = errors.New("invalid parametric payout")

// ErrInvalidDevice is returned for a device registration without its id,
// platform or a valid locale.
var ErrInvalidDevice = errors.New("invalid device")

// Trigger is the external event that crossed the threshold of the policy:
// for a flight delay, the flight, the minutes it was late and the minutes
// that make the policy pay.
type Trigger struct {
	// Parameter is what the policy measures (flight_delay, rainfall…).
	Parameter string
	// EventReference names the event briefly (the flight AV-8421).
	EventReference string
	Threshold      float64
	Observed       float64
	// Unit of Threshold and Observed (minutes, millimeters…).
	Unit       string
	ObservedAt time.Time
}

// PaymentMethod is where the payment went, without its number.
type PaymentMethod struct {
	Brand string
	Last4 string
}

// ParametricPayout is the automatic payment of a parametric claim, as the
// claims service reports it (event ParametricPayoutCompleted).
type ParametricPayout struct {
	PayoutID string
	ClaimID  string
	PolicyID string
	// Subject is the insured: the sub of their Cognito account.
	Subject     string
	AmountMinor int64
	Currency    string
	Trigger     Trigger
	Method      PaymentMethod
	// IdempotencyKey is the key the payment was made with (HA-D02).
	IdempotencyKey string
	// PaidAt is when the payment was confirmed: the 60 s of criterion 1
	// count from here.
	PaidAt time.Time
}

// Validate reports why the payout cannot become a notification.
func (p ParametricPayout) Validate() error {
	switch {
	case !validID(p.PayoutID) || !validID(p.Subject):
		return fmt.Errorf("%w: payout_id and subject are required (up to %d characters)", ErrInvalidPayout, maxIDLength)
	case p.AmountMinor <= 0 || len(p.Currency) != 3:
		return fmt.Errorf("%w: a positive amount and a three-letter currency are required", ErrInvalidPayout)
	case p.Trigger.Parameter == "" || p.Trigger.EventReference == "":
		return fmt.Errorf("%w: the trigger needs its parameter and event reference", ErrInvalidPayout)
	case p.PaidAt.IsZero():
		return fmt.Errorf("%w: paid_at is required", ErrInvalidPayout)
	}
	return nil
}

// Notification is one entry of the notification center of an insured. It is
// recorded when the event arrives, whether or not a push ever reaches a
// device (criteria 5 and 7), and it is never overwritten by a later one
// (criterion 6).
type Notification struct {
	ID      string
	Subject string
	Kind    string
	// OccurredAt is when the fact happened (the payment confirmation).
	OccurredAt time.Time
	// CreatedAt is when the service recorded it.
	CreatedAt time.Time
	// ReadAt is nil while the insured has not read it.
	ReadAt *time.Time
	Payout *ParametricPayout
}

// NotificationIDFor is the id of the notification of a payout: one per
// payout, so a redelivered event finds the one it already created.
func NotificationIDFor(payoutID string) string { return "ntf-" + payoutID }

// NewPayoutNotification builds the notification of a payout recorded at now.
func NewPayoutNotification(p ParametricPayout, now time.Time) Notification {
	payout := p
	return Notification{
		ID: NotificationIDFor(p.PayoutID), Subject: p.Subject, Kind: KindParametricPayout,
		OccurredAt: p.PaidAt.UTC(), CreatedAt: now.UTC(), Payout: &payout,
	}
}

// Device is one installation of the app of an insured. PushEnabled mirrors
// the notification permission of the operating system: without it the
// service sends no push and the notification waits in the center
// (criterion 5).
type Device struct {
	InstallationID string
	Subject        string
	Platform       string
	// Token addresses the device at the push provider.
	Token       string
	PushEnabled bool
	// Locale is the language of the app (es or en): the push is written in it.
	Locale    string
	UpdatedAt time.Time
}

// Locales the push templates are written in.
const (
	LocaleSpanish = "es"
	LocaleEnglish = "en"
)

// Reachable tells whether a push can be sent to the device.
func (d Device) Reachable() bool { return d.PushEnabled && d.Token != "" }

// Validate reports why the device cannot be registered.
func (d Device) Validate() error {
	switch {
	case !validID(d.InstallationID) || !validID(d.Subject):
		return fmt.Errorf("%w: installation_id is required (up to %d characters)", ErrInvalidDevice, maxIDLength)
	case d.Platform != "android":
		return fmt.Errorf("%w: platform must be android", ErrInvalidDevice)
	case d.Locale != LocaleSpanish && d.Locale != LocaleEnglish:
		return fmt.Errorf("%w: locale must be es or en", ErrInvalidDevice)
	case len(d.Token) > 4096:
		return fmt.Errorf("%w: the token is too long", ErrInvalidDevice)
	case d.PushEnabled && d.Token == "":
		return fmt.Errorf("%w: a token is required when push is enabled", ErrInvalidDevice)
	}
	return nil
}

func validID(s string) bool {
	return s != "" && len(s) <= maxIDLength && strings.TrimSpace(s) == s
}
