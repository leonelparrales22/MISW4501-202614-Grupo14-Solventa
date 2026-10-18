package domain

import (
	"regexp"
	"strings"
	"time"
)

// OfferValidity is how long a personalized offer can still be accepted
// (HU-W1.2-03 CA-1: 24 hours).
const OfferValidity = 24 * time.Hour

// PersonalizedOfferIDPrefix starts the id of every personalized offer, so it
// can be told from the demo (demo-*) and adjusted (adj-*) offers of other
// stories.
const PersonalizedOfferIDPrefix = "off-"

// Prefixes of the ids of the offers other stories serve from the OfferStub:
// the adjusted offers of an advisor decision (HU-W1.1-02, AdjustedOfferID) and
// the demonstration offers (HU-W1.3-01).
const (
	AdjustedOfferIDPrefix = "adj-"
	DemoOfferIDPrefix     = "demo-"
)

// OfferChannel is the channel that originated a personalized offer
// (HU-W1.2-03, decision D8). Only the embedded one is implemented; the
// assisted one is declared so the model does not change when the advisor quote
// (HU-W1.1-01) is unified with the offer.
type OfferChannel string

const (
	OfferChannelEmbedded OfferChannel = "embedded"
	OfferChannelAssisted OfferChannel = "assisted"
)

// OfferStatus is the state of a personalized offer, derived from the clock
// and from the offers that replaced it.
type OfferStatus string

const (
	OfferActive     OfferStatus = "active"
	OfferExpired    OfferStatus = "expired"
	OfferSuperseded OfferStatus = "superseded"
)

// Reasons why an offer is provisional (HU-W1.2-03 CA-3).
const (
	// ProvisionalCachedProfile: the offer was priced with the last profile of
	// the customer (ProfileDate says when it was calculated).
	ProvisionalCachedProfile = "cached_profile"
	// ProvisionalBaseProfile: there was no valid cached profile, so the offer
	// was priced with the base actuarial profile.
	ProvisionalBaseProfile = "base_actuarial_profile"
)

// ProfileSnapshot is the copy of the risk profile an offer was priced with. It
// is kept with the offer so the price can be explained later, even though the
// profile of the customer changes (HU-W1.2-03 CA-2).
type ProfileSnapshot struct {
	Score        float64
	Category     string
	CalculatedAt time.Time
	Partial      bool
	Sources      []SourceOutcome
}

// AppliedRule is one factor of the rating engine and the value it took for
// this offer.
type AppliedRule struct {
	Code  string
	Value float64
}

// PersonalizedOffer is the priced offer of the embedded journey (HU-W1.2-03).
// It is immutable once stored: a requote or a renewal creates a new offer that
// points to the previous one. OwnerID, SupersededBy, SupersededAt and
// RenewedBy are derived from other records when the offer is read.
type PersonalizedOffer struct {
	ID string
	// CustomerID is the id the partner uses for the customer; the consent and
	// the risk profile are keyed by it. It is not the owner of the offer.
	CustomerID string
	// OwnerID is the Cognito subject that claimed the offer; empty until
	// someone opens it with a session (decision D4).
	OwnerID       string
	Channel       OfferChannel
	QuoteID       string
	ConsentID     string
	PartnerID     string
	ApplicationID string

	Applicant  Applicant
	BirthDate  time.Time
	AgeAtQuote int

	Terms      CoverageTerms
	Premium    float64
	Currency   string
	Conditions []string

	Mode OfferMode
	// ProfileSource tells where the profile came from.
	ProfileSource ProfileSource
	// ProvisionalReason is set only for a provisional offer.
	ProvisionalReason string
	// ProfileDate is when the cached profile was calculated; zero otherwise.
	ProfileDate       time.Time
	UnavailableSource string
	Profile           ProfileSnapshot

	Rules        []AppliedRule
	RaterVersion string

	CreatedAt time.Time
	ExpiresAt time.Time
	// Supersedes is the provisional offer this one replaced (requote).
	Supersedes string
	// Renews is the expired offer this one renews.
	Renews string

	SupersededBy string
	SupersededAt time.Time
	RenewedBy    string
}

// Status returns superseded once a newer offer replaced this one, expired from
// ExpiresAt on and active otherwise.
func (o PersonalizedOffer) Status(now time.Time) OfferStatus {
	switch {
	case o.SupersededBy != "":
		return OfferSuperseded
	case !now.Before(o.ExpiresAt):
		return OfferExpired
	default:
		return OfferActive
	}
}

// CanAccept tells whether the customer can accept the offer now.
func (o PersonalizedOffer) CanAccept(now time.Time) bool { return o.Status(now) == OfferActive }

// CanRenew tells whether the customer can ask for a new offer: the validity is
// over, nobody replaced the offer and it was not renewed yet (a renewed offer
// points to its renewal instead).
func (o PersonalizedOffer) CanRenew(now time.Time) bool {
	return o.Status(now) == OfferExpired && o.RenewedBy == ""
}

// AsOffer is the view of the offer that the acceptance, the advisor inbox and
// the charge consume. The owner takes the place of CustomerID, because that is
// the subject those flows check; an unclaimed offer has none, so nobody can
// accept it. A superseded offer stops being acceptable at the moment it was
// replaced.
func (o PersonalizedOffer) AsOffer() Offer {
	expires := o.ExpiresAt
	if !o.SupersededAt.IsZero() && o.SupersededAt.Before(expires) {
		expires = o.SupersededAt
	}
	return Offer{
		ID:           o.ID,
		CustomerID:   o.OwnerID,
		Applicant:    o.Applicant,
		Premium:      o.Premium,
		Currency:     o.Currency,
		Mode:         o.Mode,
		ProfileScore: o.Profile.Score,
		ExpiresAt:    expires,
	}
}

// RequoteNotice is what the customer is told when a provisional offer is
// replaced by a normal one because the external source came back
// (HU-W1.2-03 CA-4).
type RequoteNotice struct {
	To           string
	CustomerName string
	OldOfferID   string
	NewOfferID   string
	OldPremium   float64
	NewPremium   float64
	Currency     string
	ExpiresAt    time.Time
	// IdempotencyKey is "offer-requoted:<old offer id>": a retry never
	// duplicates the message.
	IdempotencyKey string
}

// RequoteIdempotencyKey returns the key of the notice about the replacement of
// the offer.
func RequoteIdempotencyKey(oldOfferID string) string { return "offer-requoted:" + oldOfferID }

// EmbeddedOfferRequest is what the partner sends to generate the offer of the
// embedded journey (HU-W1.2-03). The identity of the holder and the data of
// the credit come from the partner (decision D3).
type EmbeddedOfferRequest struct {
	CustomerID    string
	ConsentID     string
	PartnerID     string
	ApplicationID string
	DocumentID    string
	FullName      string
	Email         string
	// BirthDate is YYYY-MM-DD.
	BirthDate     string
	InsuredAmount float64
	TermMonths    int
}

const maxIdentifierLength = 128

var identifierPattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// applicant returns the holder data of the request, normalized.
func (r EmbeddedOfferRequest) applicant() Applicant {
	return Applicant{
		FullName:   strings.Join(strings.Fields(r.FullName), " "),
		DocumentID: strings.TrimSpace(r.DocumentID),
		Email:      strings.TrimSpace(r.Email),
	}
}

// Validate checks the request at the given time and returns the holder and
// the birth date it describes, or a *ValidationError naming every invalid
// field, so the partner fixes them all at once.
func (r EmbeddedOfferRequest) Validate(now time.Time) (Applicant, time.Time, error) {
	var fields []FieldError
	bad := func(field, code string) { fields = append(fields, FieldError{Field: field, Code: code}) }
	identifier := func(field, v string) {
		switch {
		case v == "":
			bad(field, CodeRequired)
		case len(v) > maxIdentifierLength || !identifierPattern.MatchString(v):
			bad(field, CodeInvalidFormat)
		}
	}
	identifier("customer_id", r.CustomerID)
	identifier("consent_id", r.ConsentID)
	identifier("partner_id", r.PartnerID)
	identifier("application_id", r.ApplicationID)

	holder := r.applicant()
	switch {
	case holder.DocumentID == "":
		bad("document_id", CodeRequired)
	case !ValidDocumentID(holder.DocumentID):
		bad("document_id", CodeInvalidFormat)
	}
	switch {
	case holder.FullName == "":
		bad("full_name", CodeRequired)
	case len([]rune(holder.FullName)) < minFullNameLength || len([]rune(holder.FullName)) > maxFullNameLength || !hasLetter(holder.FullName):
		bad("full_name", CodeInvalidFormat)
	}
	switch {
	case holder.Email == "":
		bad("email", CodeRequired)
	case !validEmail(holder.Email):
		bad("email", CodeInvalidFormat)
	}
	birth, birthErr := parseBirthDate(r.BirthDate, now)
	if birthErr != "" {
		bad("birth_date", birthErr)
	}
	switch {
	case r.InsuredAmount == 0:
		bad("insured_amount", CodeRequired)
	case r.InsuredAmount < MinInsuredAmount || r.InsuredAmount > MaxInsuredAmount:
		bad("insured_amount", CodeOutOfRange)
	}
	switch {
	case r.TermMonths == 0:
		bad("term_months", CodeRequired)
	case r.TermMonths < MinTermMonths || r.TermMonths > MaxTermMonths:
		bad("term_months", CodeOutOfRange)
	}

	if len(fields) > 0 {
		return Applicant{}, time.Time{}, &ValidationError{Fields: fields}
	}
	return holder, birth, nil
}

// ValidateBirthDate tells whether a stored birth date is still insurable at
// now (renewal: the customer may have turned 71 since the first offer). It
// returns a *ValidationError on birth_date otherwise.
func ValidateBirthDate(birth, now time.Time) error {
	if age := AgeAt(birth, now); age < MinInsuredAge || age > MaxInsuredAge {
		return &ValidationError{Fields: []FieldError{{Field: "birth_date", Code: CodeOutOfRange}}}
	}
	return nil
}
