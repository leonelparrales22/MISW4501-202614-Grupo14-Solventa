package domain

import (
	"math"
	"strconv"
	"time"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/idempotency"
)

// ChargeKeyPrefix is the prefix of the idempotency key of a premium charge:
// the key is "cobro-" + acceptance id (ADR-01), never random, so every retry
// of the charge of one acceptance carries the same key.
const ChargeKeyPrefix = "cobro"

// PaymentMethodKind is how the customer pays the premium (HU-W1.3-02).
type PaymentMethodKind string

const (
	PaymentCard PaymentMethodKind = "card"
	PaymentPSE  PaymentMethodKind = "pse"
)

// MaxInstallments bounds the instalments a card payment can be split into.
const MaxInstallments = 36

// PaymentMethod is what the customer handed over in screen E4.
//
// The card number is never here and never reaches this service: the browser
// tokenises it against the gateway and only the token, the brand and the last
// four digits travel, which keeps the platform out of PCI-DSS scope.
type PaymentMethod struct {
	Kind PaymentMethodKind
	// Token identifies the card at the gateway. Empty for PSE.
	Token string
	// Brand and Last4 only exist to show "Visa ...1111".
	Brand        string
	Last4        string
	Installments int
	// BankCode is the bank chosen for PSE. Empty for a card.
	BankCode string
	// PayerType is whether the payer is a natural or a legal person, which
	// PSE asks for before sending the customer to their bank. Empty for a
	// card.
	PayerType  PayerType
	CapturedAt time.Time
}

// PayerType is what PSE needs to know about who pays (HU-W1.3-02).
type PayerType string

const (
	PayerNatural PayerType = "natural"
	PayerLegal   PayerType = "legal"
)

// ValidPayerType tells whether a payer type is one of the two PSE accepts.
func ValidPayerType(t PayerType) bool { return t == PayerNatural || t == PayerLegal }

// Captured tells whether the customer has handed over a payment method.
func (p PaymentMethod) Captured() bool { return p.Kind != "" }

// ChargeStatus is the state of the premium charge of an acceptance (HA-D02).
type ChargeStatus string

const (
	ChargePending    ChargeStatus = ChargeStatus(idempotency.StatusPending)
	ChargeInProgress ChargeStatus = ChargeStatus(idempotency.StatusInProgress)
	ChargeConfirmed  ChargeStatus = ChargeStatus(idempotency.StatusConfirmed)
	ChargeFailed     ChargeStatus = ChargeStatus(idempotency.StatusFailed)
)

// ChargeFailureReason tells why a charge ended in an issuance exception.
type ChargeFailureReason string

const (
	// ChargeRetryWindowExpired: the retry window ended without a successful
	// charge (CA-3).
	ChargeRetryWindowExpired ChargeFailureReason = "retry_window_expired"
	// ChargePaymentRejected: the gateway declined the charge.
	ChargePaymentRejected ChargeFailureReason = "payment_rejected"
	// ChargePaymentMethodMissing: the retry window ended before the customer
	// handed over a payment method, so nothing was ever charged
	// (HU-W1.3-02). It is not retry_window_expired because there was no
	// retry: saying so in the operations tray would be misleading.
	ChargePaymentMethodMissing ChargeFailureReason = "payment_method_missing"
)

// ChargeFailureReasonFor maps the reason of the shared mechanism to the one of
// the charge.
func ChargeFailureReasonFor(r idempotency.FailureReason) ChargeFailureReason {
	if r == idempotency.ReasonRejected {
		return ChargePaymentRejected
	}
	return ChargeRetryWindowExpired
}

// IssuanceStepPremiumCharge is the issuance step a charge exception belongs to.
const IssuanceStepPremiumCharge = "premium_charge"

// CoverageTerms are the conditions of the coverage that a confirmed charge
// activates: what the contractual document shows besides the premium.
type CoverageTerms struct {
	InsuredAmount float64
	TermMonths    int
	Coverages     []Coverage
}

// Charge is the premium charge of one acceptance (Intento_Cobro): one row per
// acceptance with its transitions, keyed by the deterministic idempotency key
// (ADR-01). It keeps a snapshot of the acceptance, the offer and the coverage
// terms, so the coverage activation it publishes is always the same event,
// even though the offers live in memory (decision D-2).
type Charge struct {
	AcceptanceID   string
	IdempotencyKey string
	OfferID        string
	CustomerID     string
	Channel        Channel
	AcceptedAt     time.Time

	// OfferExpiresAt is the validity of the offer behind the acceptance,
	// snapshotted here because the offers live in memory (decision D-2).
	// Screen E4c shows it so the customer knows how long the retry with
	// another payment method is worth.
	OfferExpiresAt time.Time

	// Snapshot used to charge and to build the activation event.
	Holder Applicant
	// AmountMinor is the premium in minor units of Currency (cents).
	AmountMinor int64
	Currency    string
	Terms       CoverageTerms

	Status ChargeStatus
	// Attempts counts the failed attempts in the current retry window.
	Attempts int
	// NextAttemptAt is when a pending charge is due; zero once it finished.
	NextAttemptAt time.Time
	// ClaimedUntil is when the lease of the worker that holds the charge (or
	// the publication of its activation) expires.
	ClaimedUntil    time.Time
	WindowStartedAt time.Time
	WindowEndsAt    time.Time
	LastAttemptAt   time.Time
	LastFailureAt   time.Time
	FailureReason   ChargeFailureReason
	LastError       string
	// DeclineCode is the issuer's code when the gateway declined the charge
	// (51 for insufficient funds, for instance). Unlike LastError, which is
	// a diagnostic message for operations, this one is shown to the customer
	// in screen E4c, so it stays a stable code the client translates.
	DeclineCode string
	// ProviderReference is the gateway's id for this charge, set whether it
	// was confirmed or declined: it is what the customer quotes to the
	// issuer.
	ProviderReference string
	ConfirmedAt       time.Time

	// The coverage activation, set in the same write that confirms the
	// charge (CA-4) and published afterwards.
	PolicyID          string
	ActivationEventID string
	ActivatedAt       time.Time
	CoverageStart     time.Time
	CoverageEnd       time.Time
	// RenewalOf and CoverageNotBefore are set on the charge of a policy
	// renewal (HU-W1.5-02): the policy it renews and the earliest start of
	// its coverage, which is when that policy ends.
	RenewalOf               string
	CoverageNotBefore       time.Time
	ActivationPublishedAt   time.Time
	ActivationAttempts      int
	ActivationNextAttemptAt time.Time
	ActivationLastError     string

	// The payment method of screen E4 and how many the customer has handed
	// over (HU-W1.3-02). PaymentAttempt is 0 until the first one; it is not
	// Attempts, which counts the failed gateway attempts of the window.
	PaymentMethod  PaymentMethod
	PaymentAttempt int

	// Resumptions by an operations analyst after an exception (CA-3).
	ResumeCount   int
	LastResumedBy string
	LastResumedAt time.Time
	ResumeNote    string

	CreatedAt time.Time
	UpdatedAt time.Time
}

// ChargeResumption is an operations analyst resuming a failed charge.
type ChargeResumption struct {
	By   string
	Note string
	At   time.Time
	// WindowEndsAt is the end of the new retry window.
	WindowEndsAt time.Time
}

// MaxResumeNoteLength bounds the note of a resumption.
const MaxResumeNoteLength = 500

// PolicyIDFor is the demonstration policy id of an acceptance, as fixed by
// HU-W1.3-03 ("pol-" + acceptance id) until HU-W1.3-02 defines its own.
func PolicyIDFor(acceptanceID string) string { return "pol-" + acceptanceID }

// ActivationEventIDFor is the id of the CoverageActivated event of an
// acceptance: deterministic, so a redelivery is recognized.
func ActivationEventIDFor(acceptanceID string) string {
	return "evt-coverage-activated-" + acceptanceID
}

// ToMinorUnits converts an amount to minor units (two decimals).
func ToMinorUnits(amount float64) int64 { return int64(math.Round(amount * 100)) }

// FromMinorUnits converts minor units (two decimals) back to an amount.
func FromMinorUnits(minor int64) float64 { return float64(minor) / 100 }

// NewCharge builds the pending charge of an accepted risk: the snapshot of the
// acceptance, the offer and the terms, with a retry window that ends at
// windowEndsAt (the zero time for none).
//
// It is **not due yet**: NextAttemptAt stays zero until the customer hands
// over a payment method in screen E4 (HU-W1.3-02), and ListDue skips a charge
// with no due date. The charge is still persisted the moment the acceptance
// is approved, so the no-loss point of HA-D02 does not move; what waits is
// the first attempt, not the record.
func NewCharge(a Acceptance, offer Offer, terms CoverageTerms, now, windowEndsAt time.Time) Charge {
	key, _ := idempotency.Key(ChargeKeyPrefix, a.ID)
	return Charge{
		AcceptanceID:    a.ID,
		IdempotencyKey:  key,
		OfferID:         offer.ID,
		OfferExpiresAt:  offer.ExpiresAt,
		CustomerID:      offer.CustomerID,
		Channel:         a.Channel,
		AcceptedAt:      a.AcceptedAt,
		Holder:          offer.Applicant,
		AmountMinor:     ToMinorUnits(offer.Premium),
		Currency:        offer.Currency,
		Terms:           copyTerms(terms),
		Status:          ChargePending,
		WindowStartedAt: now,
		WindowEndsAt:    windowEndsAt,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
}

// Confirmed returns the charge confirmed at the given time, with the coverage
// activation it produces: the policy, the event id and the coverage period
// (from the confirmation, for the term in months). The publication of the
// activation is due at once.
func (c Charge) Confirmed(at time.Time, providerReference string) Charge {
	c.Status = ChargeConfirmed
	c.ConfirmedAt = at
	c.ProviderReference = providerReference
	c.NextAttemptAt = time.Time{}
	c.FailureReason = ""
	c.PolicyID = PolicyIDFor(c.AcceptanceID)
	c.ActivationEventID = ActivationEventIDFor(c.AcceptanceID)
	c.ActivatedAt = at
	c.CoverageStart = at
	if c.CoverageNotBefore.After(at) {
		// A renewal paid before the policy ends starts when it ends: no
		// gap and no overlap (HU-W1.5-02).
		c.CoverageStart = c.CoverageNotBefore
	}
	c.CoverageEnd = c.CoverageStart.AddDate(0, c.Terms.TermMonths, 0)
	c.ActivationNextAttemptAt = at
	c.UpdatedAt = at
	return c
}

// ActivationEvent is the CoverageActivated event of a confirmed charge. It is
// built only from what the charge stores, so every publication of the same
// charge is exactly the same event.
func (c Charge) ActivationEvent() CoverageActivated {
	return CoverageActivated{
		EventID:       c.ActivationEventID,
		PolicyID:      c.PolicyID,
		AcceptanceID:  c.AcceptanceID,
		CustomerID:    c.CustomerID,
		Holder:        c.Holder,
		Premium:       FromMinorUnits(c.AmountMinor),
		Currency:      c.Currency,
		InsuredAmount: c.Terms.InsuredAmount,
		Coverages:     append([]Coverage(nil), c.Terms.Coverages...),
		ActivatedAt:   c.ActivatedAt,
		CoverageStart: c.CoverageStart,
		CoverageEnd:   c.CoverageEnd,
		RenewalOf:     c.RenewalOf,
	}
}

// ValidateActivation reports why a confirmed charge of this snapshot could
// not activate the coverage (decision D-17: nothing is charged that cannot be
// activated).
func (c Charge) ValidateActivation(at time.Time) error {
	return c.Confirmed(at, "").ActivationEvent().Validate()
}

// ActivationPublished tells whether the coverage activation was handed to its
// consumers.
func (c Charge) ActivationPublished() bool { return !c.ActivationPublishedAt.IsZero() }

// AttemptState is the view the shared idempotent mechanism needs.
func (c Charge) AttemptState() idempotency.Attempt {
	return idempotency.Attempt{
		ID:             c.AcceptanceID,
		Key:            c.IdempotencyKey,
		Status:         idempotency.Status(c.Status),
		FailedAttempts: c.Attempts,
		WindowEndsAt:   c.WindowEndsAt,
		NextAttemptAt:  c.NextAttemptAt,
		ClaimedUntil:   c.ClaimedUntil,
	}
}

func copyTerms(t CoverageTerms) CoverageTerms {
	t.Coverages = append([]Coverage(nil), t.Coverages...)
	return t
}

// --- Payment method (HU-W1.3-02) -------------------------------------------

// TakesPaymentMethod tells whether the customer can hand over a payment
// method for this charge.
//
// The rule is about what the gateway may already have done, not about what
// the customer would like:
//
//   - Pending with no attempt behind it: nothing has been sent, so the method
//     can be set or replaced freely.
//   - Pending with attempts: a request already reached the gateway and its
//     answer was lost. Charging another card would need another key, and
//     another key on an ambiguous result is how a customer gets charged
//     twice. Only the same card may keep retrying.
//   - Failed because the gateway **declined**: it answered, so it is certain
//     nothing was charged. Another card is safe and gets a new key. That is
//     screen E4c, and it spares the customer from waiting on an operations
//     analyst for something they can fix themselves.
//   - Failed because the window ended: ambiguous, like the pending case.
//     Only operations may resume it.
func (c Charge) TakesPaymentMethod() bool {
	switch c.Status {
	case ChargePending:
		return c.Attempts == 0
	case ChargeFailed:
		return c.FailureReason == ChargePaymentRejected
	default:
		return false
	}
}

// WithPaymentMethod returns the charge with the method attached and its
// attempt due at now, ready for the worker of HA-D02 to pick it up.
//
// After a decline it also gets a **new idempotency key**. The gateway stores
// the answer of every key and replays it, so charging another card with the
// old key would either repeat the decline or be refused as a key reused with
// a different charge. The new key is only ever issued after a definitive
// decline, never after a timeout or an ambiguous result: that is what keeps
// the no-duplicate guarantee of HA-D02 intact.
func (c Charge) WithPaymentMethod(m PaymentMethod, now time.Time) Charge {
	retryingAfterDecline := c.Status == ChargeFailed

	m.CapturedAt = now
	c.PaymentMethod = m
	c.PaymentAttempt++
	c.Status = ChargePending
	c.NextAttemptAt = now
	c.UpdatedAt = now

	if retryingAfterDecline {
		c.IdempotencyKey = ChargeKeyFor(c.AcceptanceID, c.PaymentAttempt)
		c.FailureReason = ""
		c.LastError = ""
		// The window is not reopened: it has been running since the
		// acceptance, and a decline does not buy more time.
		c.Attempts = 0
	}
	return c
}

// ChargeKeyFor is the idempotency key of the nth payment attempt of an
// acceptance. The first one keeps the key HA-D02 already uses, so nothing
// that exists changes; the rest add their ordinal, which keeps the key
// readable when someone has to trace a charge by hand.
func ChargeKeyFor(acceptanceID string, paymentAttempt int) string {
	key, _ := idempotency.Key(ChargeKeyPrefix, acceptanceID)
	if paymentAttempt <= 1 {
		return key
	}
	return key + "-" + strconv.Itoa(paymentAttempt)
}

// FailureReasonForWindow tells why a charge that ran out of window failed:
// with no payment method nothing was ever charged, and saying the retries
// expired would send operations looking for a gateway problem that never
// happened.
func (c Charge) FailureReasonForWindow() ChargeFailureReason {
	if !c.PaymentMethod.Captured() {
		return ChargePaymentMethodMissing
	}
	return ChargeRetryWindowExpired
}
