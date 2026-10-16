package domain

import (
	"net/mail"
	"regexp"
	"strings"
	"time"
	"unicode"
)

// QuoteValidity is how long an assisted quote can still be formalized
// (HU-W1.1-01: 24 hours).
const QuoteValidity = 24 * time.Hour

// Limits of the mortgage life insurance the advisor quotes. They are the
// provisional business rules of the rating v1 (HU-W1.2-03 may refine them).
const (
	MinInsuredAge      = 18
	MaxInsuredAge      = 70
	MinInsuredAmount   = 10_000_000.0
	MaxInsuredAmount   = 2_000_000_000.0
	MinTermMonths      = 12
	MaxTermMonths      = 360
	maxFullNameLength  = 120
	minFullNameLength  = 3
	birthDateLayout    = "2006-01-02"
	DefaultCurrency    = "COP"
	CoverageDeath      = "death"
	CoverageDisability = "total_permanent_disability"
)

// Customer is a person the advisor quotes for. The advisor recovers an
// existing one by document id instead of typing the data again.
type Customer struct {
	ID         string
	DocumentID string
	FullName   string
	Email      string
	BirthDate  time.Time
}

// Applicant returns the customer data shown to an advisor reviewing a request.
func (c Customer) Applicant() Applicant {
	return Applicant{FullName: c.FullName, DocumentID: c.DocumentID, Email: c.Email}
}

// QuoteRequest is what the advisor types in the portal. Dates and numbers are
// validated by Validate, so a bad value yields a per-field error instead of a
// malformed request.
type QuoteRequest struct {
	DocumentID string
	FullName   string
	Email      string
	// BirthDate is YYYY-MM-DD.
	BirthDate     string
	InsuredAmount float64
	TermMonths    int
	// ConsentActive is the consent to profile the customer. The caller states
	// it while the audit service does not expose the consent query.
	ConsentActive *bool
}

// Field validation codes.
const (
	CodeRequired      = "required"
	CodeInvalidFormat = "invalid_format"
	CodeOutOfRange    = "out_of_range"
)

// FieldError tells which field failed and why. Field is the JSON name; Code is
// a stable identifier the client translates.
type FieldError struct {
	Field string
	Code  string
}

// ValidationError lists every invalid field of a request, so the advisor sees
// all the problems at once (HU-W1.1-01 criterion 3).
type ValidationError struct {
	Fields []FieldError
}

func (e *ValidationError) Error() string {
	parts := make([]string, 0, len(e.Fields))
	for _, f := range e.Fields {
		parts = append(parts, f.Field+": "+f.Code)
	}
	return "invalid quote request (" + strings.Join(parts, ", ") + ")"
}

var documentIDPattern = regexp.MustCompile(`^[A-Za-z0-9]{5,20}$`)

// ValidDocumentID tells whether s has the shape of a document id.
func ValidDocumentID(s string) bool { return documentIDPattern.MatchString(s) }

// Validate checks the request at the given time and returns the customer data
// it describes, or a *ValidationError naming every invalid field.
func (r QuoteRequest) Validate(now time.Time) (Customer, error) {
	var fields []FieldError
	bad := func(field, code string) { fields = append(fields, FieldError{Field: field, Code: code}) }

	doc := strings.TrimSpace(r.DocumentID)
	switch {
	case doc == "":
		bad("document_id", CodeRequired)
	case !ValidDocumentID(doc):
		bad("document_id", CodeInvalidFormat)
	}

	name := strings.Join(strings.Fields(r.FullName), " ")
	switch {
	case name == "":
		bad("full_name", CodeRequired)
	case len([]rune(name)) < minFullNameLength || len([]rune(name)) > maxFullNameLength || !hasLetter(name):
		bad("full_name", CodeInvalidFormat)
	}

	email := strings.TrimSpace(r.Email)
	switch {
	case email == "":
		bad("email", CodeRequired)
	case !validEmail(email):
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

	if r.ConsentActive == nil {
		bad("consent_active", CodeRequired)
	}

	if len(fields) > 0 {
		return Customer{}, &ValidationError{Fields: fields}
	}
	return Customer{DocumentID: doc, FullName: name, Email: email, BirthDate: birth}, nil
}

// parseBirthDate returns the date, or the validation code that explains why
// the value is not usable.
func parseBirthDate(s string, now time.Time) (time.Time, string) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, CodeRequired
	}
	d, err := time.Parse(birthDateLayout, s)
	if err != nil {
		return time.Time{}, CodeInvalidFormat
	}
	if age := AgeAt(d, now); d.After(now) || age < MinInsuredAge || age > MaxInsuredAge {
		return time.Time{}, CodeOutOfRange
	}
	return d, ""
}

// AgeAt returns the completed years between birth and now.
func AgeAt(birth, now time.Time) int {
	years := now.Year() - birth.Year()
	if now.Month() < birth.Month() || (now.Month() == birth.Month() && now.Day() < birth.Day()) {
		years--
	}
	return years
}

func hasLetter(s string) bool {
	for _, r := range s {
		if unicode.IsLetter(r) {
			return true
		}
	}
	return false
}

// validEmail accepts a bare address ("a@b.co"), not "Name <a@b.co>".
func validEmail(s string) bool {
	addr, err := mail.ParseAddress(s)
	return err == nil && addr.Address == s && strings.Contains(s[strings.LastIndex(s, "@"):], ".")
}

// Coverage is one coverage of a quote.
type Coverage struct {
	Code          string
	InsuredAmount float64
}

// QuoteStatus tells whether a quote can still be formalized.
type QuoteStatus string

const (
	QuoteValid   QuoteStatus = "valid"
	QuoteExpired QuoteStatus = "expired"
)

// Quote is a priced quote generated by an advisor (HU-W1.1-01). HU-W1.1-03
// formalizes it while it is valid.
type Quote struct {
	ID         string
	CustomerID string
	AdvisorID  string
	Applicant  Applicant
	BirthDate  time.Time

	InsuredAmount float64
	TermMonths    int

	// Premium is the monthly premium in Currency.
	Premium       float64
	Currency      string
	Coverages     []Coverage
	RatingVersion string

	// Mode is provisional when the quote was priced with the cached or the
	// base actuarial profile because the external sources failed (HA-D01).
	Mode              OfferMode
	ProfileSource     ProfileSource
	ProfileScore      float64
	ProfileCategory   string
	ProfileDate       time.Time
	UnavailableSource string

	CreatedAt time.Time
	ExpiresAt time.Time
}

// Status returns valid until ExpiresAt and expired from then on.
func (q Quote) Status(now time.Time) QuoteStatus {
	if now.Before(q.ExpiresAt) {
		return QuoteValid
	}
	return QuoteExpired
}

// CanFormalize tells whether the advisor may formalize the purchase now.
func (q Quote) CanFormalize(now time.Time) bool { return q.Status(now) == QuoteValid }

// Rating is the outcome of the rating engine for one quote.
type Rating struct {
	Premium   float64
	Currency  string
	Coverages []Coverage
	Version   string
}
