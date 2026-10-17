package domain

import (
	"math"
	"time"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/idempotency"
)

// ChargeKeyPrefix is the prefix of the idempotency key of a premium charge:
// the key is "cobro-" + acceptance id (ADR-01), never random, so every retry
// of the charge of one acceptance carries the same key.
const ChargeKeyPrefix = "cobro"

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
	ClaimedUntil      time.Time
	WindowStartedAt   time.Time
	WindowEndsAt      time.Time
	LastAttemptAt     time.Time
	LastFailureAt     time.Time
	FailureReason     ChargeFailureReason
	LastError         string
	ProviderReference string
	ConfirmedAt       time.Time

	// The coverage activation, set in the same write that confirms the
	// charge (CA-4) and published afterwards.
	PolicyID                string
	ActivationEventID       string
	ActivatedAt             time.Time
	CoverageStart           time.Time
	CoverageEnd             time.Time
	ActivationPublishedAt   time.Time
	ActivationAttempts      int
	ActivationNextAttemptAt time.Time
	ActivationLastError     string

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
// acceptance, the offer and the terms, due at now, with a retry window that
// ends at windowEndsAt (the zero time for none).
func NewCharge(a Acceptance, offer Offer, terms CoverageTerms, now, windowEndsAt time.Time) Charge {
	key, _ := idempotency.Key(ChargeKeyPrefix, a.ID)
	return Charge{
		AcceptanceID:    a.ID,
		IdempotencyKey:  key,
		OfferID:         offer.ID,
		CustomerID:      offer.CustomerID,
		Channel:         a.Channel,
		AcceptedAt:      a.AcceptedAt,
		Holder:          offer.Applicant,
		AmountMinor:     ToMinorUnits(offer.Premium),
		Currency:        offer.Currency,
		Terms:           copyTerms(terms),
		Status:          ChargePending,
		NextAttemptAt:   now,
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
	c.CoverageEnd = at.AddDate(0, c.Terms.TermMonths, 0)
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
