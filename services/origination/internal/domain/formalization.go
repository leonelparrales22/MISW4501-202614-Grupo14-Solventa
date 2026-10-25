package domain

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"regexp"
	"strings"
	"time"
)

// Bounds of the confirmation code an advisor needs to formalize a purchase on
// behalf of the customer (HU-W1.1-03 CA-5).
const (
	// ConsentCodeLength is the number of digits of the code.
	ConsentCodeLength = 6
	// ConsentCodeTTL is how long a code can be used after it was sent.
	ConsentCodeTTL = 10 * time.Minute
	// MaxConsentCodeSends bounds the codes sent for one quote.
	MaxConsentCodeSends = 3
	// MaxConsentCodeAttempts bounds the codes typed for one quote, right or
	// wrong, so the code cannot be guessed.
	MaxConsentCodeAttempts = 5
)

// AdvisorPartnerID is the partner id of the offers an advisor formalizes: they
// do not come from a partner's embedded journey.
const AdvisorPartnerID = "solventa-advisor"

// ConsentMethod is how the customer's acceptance of the purchase was
// recorded.
type ConsentMethod string

const (
	// ConsentRecordedCall: the customer accepted on a recorded call, identified
	// by the reference of the recording.
	ConsentRecordedCall ConsentMethod = "recorded_call"
	// ConsentRemoteSignature: the customer signed remotely, identified by the
	// reference of the signature.
	ConsentRemoteSignature ConsentMethod = "remote_signature"
	// ConsentConfirmationCode: the customer received a code and gave it to the
	// advisor, who typed it.
	ConsentConfirmationCode ConsentMethod = "confirmation_code"
)

// ConsentMethods lists the methods the advisor can choose from.
var ConsentMethods = []ConsentMethod{ConsentRecordedCall, ConsentRemoteSignature, ConsentConfirmationCode}

// Valid tells whether m is a known method.
func (m ConsentMethod) Valid() bool {
	for _, k := range ConsentMethods {
		if m == k {
			return true
		}
	}
	return false
}

const (
	// MaxConsentReferenceLength bounds the reference of a recording or a signature.
	MaxConsentReferenceLength = 64
	// consentClockSkew is how far into the future the acceptance may be dated.
	consentClockSkew = 5 * time.Minute
)

var consentReferencePattern = regexp.MustCompile(`^[A-Za-z0-9._:/#-]+$`)

// ConsentAcceptance is what the advisor records about the customer's
// acceptance (CA-5): how it happened, when and the reference that identifies
// it. With ConsentConfirmationCode the reference is optional, because the
// code itself is the proof.
type ConsentAcceptance struct {
	Method     ConsentMethod
	AcceptedAt time.Time
	Reference  string
}

// Validate checks the acceptance against the clock and the quote it belongs
// to: the customer cannot accept in the future, nor before the minute the quote
// existed (the screen captures the time to the minute).
// It reports every invalid field, named as the API names it.
func (a ConsentAcceptance) Validate(now, quoteCreatedAt time.Time) error {
	var fields []FieldError
	bad := func(field, code string) { fields = append(fields, FieldError{Field: field, Code: code}) }

	switch {
	case a.Method == "":
		bad("acceptance.method", CodeRequired)
	case !a.Method.Valid():
		bad("acceptance.method", CodeInvalidFormat)
	}
	switch {
	case a.AcceptedAt.IsZero():
		bad("acceptance.accepted_at", CodeRequired)
	case a.AcceptedAt.After(now.Add(consentClockSkew)), a.AcceptedAt.Before(quoteCreatedAt.Truncate(time.Minute)):
		bad("acceptance.accepted_at", CodeOutOfRange)
	}
	ref := strings.TrimSpace(a.Reference)
	switch {
	case ref == "" && a.Method != ConsentConfirmationCode:
		bad("acceptance.reference", CodeRequired)
	case ref != "" && (len(ref) > MaxConsentReferenceLength || !consentReferencePattern.MatchString(ref)):
		bad("acceptance.reference", CodeInvalidFormat)
	}
	if len(fields) > 0 {
		return &ValidationError{Fields: fields}
	}
	return nil
}

// ConsentChallenge is the code sent to the customer for one quote. Only the
// hash of the code is kept.
type ConsentChallenge struct {
	QuoteID   string
	AdvisorID string
	CodeHash  string
	// Sends and Attempts count the codes sent and typed for the quote; they
	// survive a new code.
	Sends     int
	Attempts  int
	CreatedAt time.Time
	ExpiresAt time.Time
}

// IsExpired tells whether the code can no longer be used at now.
func (c ConsentChallenge) IsExpired(now time.Time) bool { return !now.Before(c.ExpiresAt) }

// Matches tells whether code is the one the challenge was issued with.
func (c ConsentChallenge) Matches(code string) bool {
	return subtle.ConstantTimeCompare([]byte(HashConsentCode(c.QuoteID, code)), []byte(c.CodeHash)) == 1
}

// HashConsentCode is how a code is stored: bound to its quote, so a hash
// copied from one quote means nothing on another.
func HashConsentCode(quoteID, code string) string {
	sum := sha256.Sum256([]byte(quoteID + ":" + code))
	return hex.EncodeToString(sum[:])
}

// ValidConsentCode tells whether s has the shape of a confirmation code.
func ValidConsentCode(s string) bool {
	if len(s) != ConsentCodeLength {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// ConsentCodeNotice is the message that carries the code to the customer.
type ConsentCodeNotice struct {
	To           string
	CustomerName string
	Code         string
	ExpiresAt    time.Time
	// IdempotencyKey is stable for a quote and its n-th code, so a retried
	// send does not repeat the message.
	IdempotencyKey string
}

// Formalization is a purchase formalized by an advisor on behalf of the
// customer (HU-W1.1-03): which quote, which advisor, when and how the customer
// consented. It is append-only.
type Formalization struct {
	ID         string
	QuoteID    string
	OfferID    string
	CustomerID string
	AdvisorID  string
	// AdvisorEmail identifies the advisor to people: the token carries no name.
	AdvisorEmail string
	// Consent says how and when the customer accepted (CA-5).
	Consent ConsentAcceptance
	// FormalizedAt is when the advisor formalized the purchase.
	FormalizedAt time.Time
}

// FormalizationOfferID is the id of the offer built from a quote. It is
// derived from the quote, so formalizing a quote twice names the same offer.
func FormalizationOfferID(quoteID string) string { return PersonalizedOfferIDPrefix + "q-" + quoteID }

// FormalizationID is the id of the formalization of a quote.
func FormalizationID(quoteID string) string { return "frm-" + quoteID }
