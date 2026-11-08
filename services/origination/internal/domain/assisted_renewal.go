package domain

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// HU-W1.5-03: the assisted renewal. The advisor opens a policy about to expire
// (HU-W1.5-01), requotes it with the rating engine, records how the customer
// accepted the renewed conditions and formalizes the renewal, which charges
// and issues it with the flow of the online renewal (HU-W1.5-02).
//
// The requote is a stored, immutable snapshot that the formalization cites by
// id, so the price the customer agreed to is the price charged. Only the last
// requote of the period can be formalized, and only while it is valid.

// RenewalRequoteIDPrefix starts the id of every requote of a renewal.
const RenewalRequoteIDPrefix = "rq-"

// RenewalRequoteValidity is how long a requote can be formalized: the same 24
// hours of an assisted quote (decision D-4).
const RenewalRequoteValidity = QuoteValidity

// RequoteStatus tells whether a requote can still be formalized.
type RequoteStatus string

const (
	// RequoteValid: the requote is the last of its period and has not expired.
	RequoteValid RequoteStatus = "valid"
	// RequoteExpired: more than RenewalRequoteValidity passed since it was made.
	RequoteExpired RequoteStatus = "expired"
	// RequoteSuperseded: a newer requote of the same period replaced it.
	RequoteSuperseded RequoteStatus = "superseded"
)

// ProfileNotice tells the advisor which degraded profile priced a requote
// (decision D-7): the last cached profile or, without a recent one, the base
// actuarial profile. It is empty when the sources answered.
type ProfileNotice string

const (
	// ProfileNoticeNone: the profile came from the external sources, or no
	// profile was needed.
	ProfileNoticeNone ProfileNotice = ""
	// ProfileNoticeLastProfileInUse: the sources did not answer and the last
	// cached profile (at most 30 days old) priced the requote.
	ProfileNoticeLastProfileInUse ProfileNotice = "last_profile_in_use"
	// ProfileNoticePersonalizationUnavailable: the sources did not answer and
	// there was no recent cached profile, so the base actuarial profile priced
	// the requote.
	ProfileNoticePersonalizationUnavailable ProfileNotice = "personalization_unavailable"
)

// ProfileNoticeOf is the notice of a profile resolution.
func ProfileNoticeOf(mode OfferMode, source ProfileSource) ProfileNotice {
	if mode != ModeProvisional {
		return ProfileNoticeNone
	}
	if source == SourceBaseActuarial {
		return ProfileNoticePersonalizationUnavailable
	}
	return ProfileNoticeLastProfileInUse
}

// ConsentBasis says on what authority the external data of the customer was
// consulted for a requote (decisions D-8 and D-13 c).
type ConsentBasis string

const (
	// ConsentBasisAuditVerified: the audit service answered that the customer
	// has an active consent (policies born in the embedded channel, or whose
	// channel is unknown).
	ConsentBasisAuditVerified ConsentBasis = "audit_verified"
	// ConsentBasisAdvisorAffirmed: the advisor affirmed that the customer
	// authorized the consultation (policies born in the assisted channel,
	// whose consent audit does not keep).
	ConsentBasisAdvisorAffirmed ConsentBasis = "advisor_affirmed"
)

// ConsentAffirmedField is the field of the requote request with which the
// advisor affirms the customer's consent (decision D-13 c).
const ConsentAffirmedField = "consent_affirmed"

// RequireConsentAffirmation checks the advisor's affirmation of a policy born
// in the assisted channel: without it nothing may be consulted.
func RequireConsentAffirmation(affirmed bool) error {
	if affirmed {
		return nil
	}
	return &ValidationError{Fields: []FieldError{{Field: ConsentAffirmedField, Code: CodeRequired}}}
}

// RenewalRequote is the requote of one period of one policy made by an
// advisor: the renewed conditions, how they were priced and on what consent.
// It is immutable once stored.
type RenewalRequote struct {
	ID        string
	PolicyID  string
	PeriodEnd time.Time
	AdvisorID string

	// The renewed conditions.
	Terms        CoverageTerms
	Premium      float64
	Currency     string
	DiscountRate float64
	// Requoted is false when the conditions repeat the current ones because
	// the engine could not price the renewal (no birth date, no profile or a
	// rating error), as in HU-W1.5-02.
	Requoted      bool
	RatingVersion string
	// AppliedRules are the factors of the rating engine (rating.Explain).
	AppliedRules []AppliedRule

	// The profile that priced it (decision D-7). Score and category are kept
	// as evidence and never shown.
	Mode              OfferMode
	ProfileSource     ProfileSource
	ProfileNotice     ProfileNotice
	ProfileScore      float64
	ProfileCategory   string
	ProfileDate       time.Time
	UnavailableSource string
	Sources           []SourceOutcome

	// ProfileCustomerID is the customer the profile and the consent are keyed
	// by (decision D-6); OriginChannel is the channel of the policy's original
	// offer, empty when it is unknown.
	ProfileCustomerID string
	OriginChannel     OfferChannel
	// ConsentBasis is empty only when no external data was consulted (no
	// customer could be found for the policy).
	ConsentBasis      ConsentBasis
	ConsentID         string
	ConsentAffirmedBy string
	ConsentAffirmedAt time.Time

	CreatedAt time.Time
	ExpiresAt time.Time
}

// RenewalRequoteExpiry is when a requote made at createdAt stops being valid.
func RenewalRequoteExpiry(createdAt time.Time) time.Time {
	return createdAt.Add(RenewalRequoteValidity)
}

// Status returns valid until ExpiresAt and expired from then on.
func (r RenewalRequote) Status(now time.Time) RequoteStatus {
	if now.Before(r.ExpiresAt) {
		return RequoteValid
	}
	return RequoteExpired
}

// StatusAmong returns superseded when latestID, the last requote of the
// period, is another one, and Status otherwise.
func (r RenewalRequote) StatusAmong(latestID string, now time.Time) RequoteStatus {
	if latestID != "" && latestID != r.ID {
		return RequoteSuperseded
	}
	return r.Status(now)
}

// Validate checks the snapshot before it is stored.
func (r RenewalRequote) Validate() error {
	switch {
	case !strings.HasPrefix(r.ID, RenewalRequoteIDPrefix):
		return fmt.Errorf("requote %q: the id must start with %s", r.ID, RenewalRequoteIDPrefix)
	case r.PolicyID == "" || r.PeriodEnd.IsZero() || r.AdvisorID == "":
		return fmt.Errorf("requote %s: policy, period and advisor are required", r.ID)
	case r.Premium <= 0 || r.Currency == "" || r.Terms.TermMonths <= 0:
		return fmt.Errorf("requote %s: premium, currency and term are required", r.ID)
	case r.CreatedAt.IsZero() || !r.ExpiresAt.After(r.CreatedAt):
		return fmt.Errorf("requote %s: it must expire after it was made", r.ID)
	}
	switch r.ConsentBasis {
	case ConsentBasisAuditVerified, "":
		return nil
	case ConsentBasisAdvisorAffirmed:
		if r.ConsentAffirmedBy == "" || r.ConsentAffirmedAt.IsZero() {
			return fmt.Errorf("requote %s: an affirmed consent needs who affirmed it and when", r.ID)
		}
		return nil
	default:
		return fmt.Errorf("requote %s: unknown consent basis %q", r.ID, r.ConsentBasis)
	}
}

// PremiumChangeRate is how much the renewed premium differs from current, as
// a fraction (0.053 = +5.3 %). It is 0 when current is not positive.
func PremiumChangeRate(current, renewed float64) float64 {
	if current <= 0 {
		return 0
	}
	return renewed/current - 1
}

// RenewalFormalization is the record of a renewal formalized by an advisor
// (CA-4): who, when, from which requote and how the customer accepted. It is
// append-only.
type RenewalFormalization struct {
	RenewalID string
	RequoteID string
	AdvisorID string
	// AdvisorEmail and AdvisorName identify the advisor to people. The name is
	// the claim name of the token, empty when the account has none.
	AdvisorEmail string
	AdvisorName  string
	// Acceptance says how and when the customer accepted (CA-2).
	Acceptance   ConsentAcceptance
	FormalizedAt time.Time
}

// AdvisorDisplayName is how the screen names the advisor: the name, or the
// e-mail when the token carried none.
func (f RenewalFormalization) AdvisorDisplayName() string {
	if strings.TrimSpace(f.AdvisorName) != "" {
		return f.AdvisorName
	}
	return f.AdvisorEmail
}

// RequoteIDField is the field of the formalization that cites the requote.
const RequoteIDField = "requote_id"

// ValidateRenewalAcceptance checks the formalization request: the requote it
// cites and the customer's acceptance, which cannot be dated before the minute
// the requote was made nor in the future. Every invalid field is reported at
// once, named as the API names it (the acceptance fields are the ones of
// HU-W1.1-03).
func ValidateRenewalAcceptance(requoteID string, a ConsentAcceptance, now, requoteCreatedAt time.Time) error {
	var fields []FieldError
	if strings.TrimSpace(requoteID) == "" {
		fields = append(fields, FieldError{Field: RequoteIDField, Code: CodeRequired})
	}
	if err := a.Validate(now, requoteCreatedAt); err != nil {
		var invalid *ValidationError
		if !errors.As(err, &invalid) {
			return err
		}
		fields = append(fields, invalid.Fields...)
	}
	if len(fields) > 0 {
		return &ValidationError{Fields: fields}
	}
	return nil
}

// Errors of the assisted renewal.
var (
	// ErrRequoteExpired: the requote is older than RenewalRequoteValidity; the
	// advisor must requote again.
	ErrRequoteExpired = errors.New("the requote expired")
	// ErrRequoteSuperseded: a newer requote of the period replaced it.
	ErrRequoteSuperseded = errors.New("the requote was replaced by a newer one")
	// ErrAssistedRenewalTaken: another advisor already formalized the renewal
	// of this period.
	ErrAssistedRenewalTaken = errors.New("another advisor already formalized the renewal of this period")
	// ErrPaymentMethodMissing: the policy has no registered payment method,
	// so the advisor cannot charge the renewal (decision D-10).
	ErrPaymentMethodMissing = errors.New("the policy has no registered payment method")
)
