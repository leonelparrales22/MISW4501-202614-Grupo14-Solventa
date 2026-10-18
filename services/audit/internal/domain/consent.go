// Package domain holds the business types of the audit service. It depends on
// no adapter or infrastructure library (AGENTS.md §2).
package domain

import (
	"errors"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
)

// TermsVersion identifies a version of the consent terms shown to the customer
// (HU-W1.2-01 criterion 1: what is shown is what is recorded).
type TermsVersion string

// TermsVersionV1 is the current version of the terms.
const TermsVersionV1 TermsVersion = "v1"

// Codes of the data sources a consent covers.
const (
	SourceOpenFinance = "open_finance"
	SourceOpenData    = "open_data"
	SourceKYC         = "kyc"
)

// Validity says how long a consent for one source lasts.
type Validity string

const (
	// Validity12Months lasts 12 months from the moment the consent is granted.
	Validity12Months Validity = "12_months"
	// ValiditySingleUse is for one profiling. The record of its use is pending
	// (HU-W1.2-01 out of scope), so until then it stays active while the
	// consent is.
	ValiditySingleUse Validity = "single_use"
)

// ConsentValidityMonths is the lifetime of a consent, in months.
const ConsentValidityMonths = 12

// Channel is the way the customer granted the consent.
type Channel string

// ChannelEmbedded is the embedded-insurance journey of a partner. The type is
// open on purpose: the assisted and mobile channels add their own constant
// without changing the record, but only this one is accepted today.
const ChannelEmbedded Channel = "embedded"

// SourceTerm is what the customer is told about one data source: which data it
// reads and for how long the consent lasts.
type SourceTerm struct {
	Code           string
	DataCategories []string
	Validity       Validity
}

// Terms are the consent terms of one version: purpose, who controls the data,
// the data policy and the sources with their data.
type Terms struct {
	Version    TermsVersion
	Purpose    string
	Controller string
	PolicyRef  string
	Sources    []SourceTerm
}

// TermsV1 returns the current terms (screen E1 of the prototype). Each call
// returns a fresh copy, so no caller can change the terms of the others.
func TermsV1() Terms {
	return Terms{
		Version:    TermsVersionV1,
		Purpose:    "risk_profiling_mortgage_life_offer",
		Controller: "Solventa S.A.",
		PolicyRef:  "personal-data-policy-v1",
		Sources: []SourceTerm{
			{Code: SourceOpenFinance, DataCategories: []string{"credit_products", "balances", "payment_behavior"}, Validity: Validity12Months},
			{Code: SourceOpenData, DataCategories: []string{"public_records"}, Validity: Validity12Months},
			{Code: SourceKYC, DataCategories: []string{"identity_verification"}, Validity: ValiditySingleUse},
		},
	}
}

// SourceCodes returns the codes of the sources of the terms.
func (t Terms) SourceCodes() []string {
	codes := make([]string, len(t.Sources))
	for i, s := range t.Sources {
		codes[i] = s.Code
	}
	return codes
}

// Holder is the person the consent is about.
type Holder struct {
	DocumentID string
	FullName   string
}

// Consent is the immutable record of an explicit consent (HU-W1.2-01
// criterion 2). Scope is a copy of the sources as the customer saw them, so a
// later change of the terms never rewrites what was consented to.
type Consent struct {
	ID            string
	CustomerID    string
	Holder        Holder
	TermsVersion  TermsVersion
	Scope         []SourceTerm
	Purpose       string
	Channel       Channel
	PartnerID     string
	ApplicationID string
	UserAgent     string
	GrantedAt     time.Time
}

// ExpiresAt is when the consent stops being active: ConsentValidityMonths
// after it was granted.
func (c Consent) ExpiresAt() time.Time {
	return c.GrantedAt.AddDate(0, ConsentValidityMonths, 0)
}

// Active tells whether the consent covers the customer at now. It is active
// strictly before ExpiresAt.
func (c Consent) Active(now time.Time) bool {
	return now.Before(c.ExpiresAt())
}

// ActiveFor tells whether the consent covers the source at now. A source that
// is not in the scope is never active.
func (c Consent) ActiveFor(source string, now time.Time) bool {
	for _, s := range c.Scope {
		if s.Code != source {
			continue
		}
		// Both validities end with the consent: the single use of KYC has no
		// usage record yet (see ValiditySingleUse).
		return c.Active(now)
	}
	return false
}

// ActiveSources lists, in the order of the scope, the sources active at now.
func (c Consent) ActiveSources(now time.Time) []string {
	out := make([]string, 0, len(c.Scope))
	for _, s := range c.Scope {
		if c.ActiveFor(s.Code, now) {
			out = append(out, s.Code)
		}
	}
	return out
}

// Verification is the answer to "may this customer be profiled now?".
type Verification struct {
	Active    bool
	ConsentID string
	GrantedAt time.Time
	ExpiresAt time.Time
	Sources   []string
}

// Field validation codes.
const (
	CodeRequired      = "required"
	CodeInvalidFormat = "invalid_format"
	CodeOutOfRange    = "out_of_range"
	CodeIncomplete    = "incomplete"
	CodeMustBeTrue    = "must_be_true"
)

// Limits of the grant request.
const (
	MaxIdentifierLength = 128
	minFullNameLength   = 3
	maxFullNameLength   = 120
	maxUserAgentLength  = 512
)

// ErrTermsOutdated means the customer saw a version of the terms that is no
// longer the current one: the journey must show them again (HTTP 409).
var ErrTermsOutdated = errors.New("the consent terms version is outdated")

// FieldError tells which field failed and why. Field is the JSON name; Code is
// a stable identifier the client translates.
type FieldError struct {
	Field string
	Code  string
}

// ValidationError lists every invalid field of a request.
type ValidationError struct {
	Fields []FieldError
}

func (e *ValidationError) Error() string {
	parts := make([]string, 0, len(e.Fields))
	for _, f := range e.Fields {
		parts = append(parts, f.Field+": "+f.Code)
	}
	return "invalid consent request (" + strings.Join(parts, ", ") + ")"
}

var (
	identifierPattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
	documentPattern   = regexp.MustCompile(`^[A-Za-z0-9]{5,20}$`)
)

// GrantRequest is what the partner journey sends when the customer accepts.
type GrantRequest struct {
	CustomerID         string
	DocumentID         string
	FullName           string
	TermsVersion       string
	AcceptedSources    []string
	AcceptedDataPolicy bool
	Channel            string
	PartnerID          string
	ApplicationID      string
	UserAgent          string
}

// Validate checks the request against the current terms. It returns a
// *ValidationError naming every invalid field, or ErrTermsOutdated when the
// fields are fine but the terms version is not the current one. The
// customer must accept all the sources (all or nothing).
func (r GrantRequest) Validate(terms Terms) error {
	var fields []FieldError
	bad := func(field, code string) { fields = append(fields, FieldError{Field: field, Code: code}) }

	identifier := func(field, value string) {
		switch {
		case value == "":
			bad(field, CodeRequired)
		case len(value) > MaxIdentifierLength || !identifierPattern.MatchString(value):
			bad(field, CodeInvalidFormat)
		}
	}
	identifier("customer_id", r.CustomerID)
	identifier("partner_id", r.PartnerID)
	identifier("application_id", r.ApplicationID)

	switch doc := strings.TrimSpace(r.DocumentID); {
	case doc == "":
		bad("document_id", CodeRequired)
	case !documentPattern.MatchString(doc):
		bad("document_id", CodeInvalidFormat)
	}

	switch name := normalizeName(r.FullName); {
	case name == "":
		bad("full_name", CodeRequired)
	case len([]rune(name)) < minFullNameLength || len([]rune(name)) > maxFullNameLength || !hasLetter(name):
		bad("full_name", CodeInvalidFormat)
	}

	if r.TermsVersion == "" {
		bad("terms_version", CodeRequired)
	}

	switch {
	case len(r.AcceptedSources) == 0:
		bad("accepted_sources", CodeRequired)
	case !sameSources(r.AcceptedSources, terms.SourceCodes()):
		bad("accepted_sources", CodeIncomplete)
	}

	if !r.AcceptedDataPolicy {
		bad("accepted_data_policy", CodeMustBeTrue)
	}

	switch {
	case r.Channel == "":
		bad("channel", CodeRequired)
	case Channel(r.Channel) != ChannelEmbedded:
		bad("channel", CodeInvalidFormat)
	}

	if len(r.UserAgent) > maxUserAgentLength {
		bad("user_agent", CodeOutOfRange)
	}

	if len(fields) > 0 {
		return &ValidationError{Fields: fields}
	}
	if TermsVersion(r.TermsVersion) != terms.Version {
		return ErrTermsOutdated
	}
	return nil
}

// Holder returns the normalized holder of the request.
func (r GrantRequest) Holder() Holder {
	return Holder{DocumentID: strings.TrimSpace(r.DocumentID), FullName: normalizeName(r.FullName)}
}

// sameSources tells whether accepted holds exactly the sources of want, in any
// order and with no repeated or unknown code.
func sameSources(accepted, want []string) bool {
	if len(accepted) != len(want) {
		return false
	}
	a := append([]string(nil), accepted...)
	w := append([]string(nil), want...)
	sort.Strings(a)
	sort.Strings(w)
	for i := range a {
		if a[i] != w[i] {
			return false
		}
	}
	return true
}

func normalizeName(s string) string { return strings.Join(strings.Fields(s), " ") }

func hasLetter(s string) bool {
	for _, r := range s {
		if unicode.IsLetter(r) {
			return true
		}
	}
	return false
}
