// Package domain holds the filed claim and the rules of its filing
// (HU-M2.2-01). It knows nothing about HTTP, PostgreSQL or Cognito.
package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// IncidentType is what happened, as the guided assistant offers it.
type IncidentType string

const (
	IncidentCollision        IncidentType = "collision"
	IncidentTheft            IncidentType = "theft"
	IncidentThirdPartyDamage IncidentType = "third_party_damage"
	IncidentOther            IncidentType = "other"
)

// Valid reports whether t is one of the four types.
func (t IncidentType) Valid() bool {
	switch t {
	case IncidentCollision, IncidentTheft, IncidentThirdPartyDamage, IncidentOther:
		return true
	}
	return false
}

// Status is the state of the case. A filing is born active.
type Status string

const StatusActive Status = "active"

// Known tells whether s is a status a claim can be in: active, in evaluation
// or one of the decision (HU-W2.2-03), so the insured can filter by any.
func Known(s Status) bool {
	switch s {
	case StatusActive, StatusInEvaluation, StatusApproved, StatusPaid, StatusRejected, StatusPaymentException:
		return true
	}
	return false
}

// Field validation codes. They are stable identifiers the client translates.
const (
	CodeRequired      = "required"
	CodeInvalidFormat = "invalid_format"
	CodeOutOfRange    = "out_of_range"
	// CodeNotStored is the key of an evidence that is not in the store, or
	// that belongs to somebody else (HU-M2.1-01).
	CodeNotStored = "not_stored"
)

// FieldError tells which field failed and why. Field is the JSON name.
type FieldError struct {
	Field string
	Code  string
}

// ValidationError lists every invalid field of a filing, so the assistant can
// show all of them at once instead of one per round trip.
type ValidationError struct {
	Fields []FieldError
}

func (e *ValidationError) Error() string {
	parts := make([]string, 0, len(e.Fields))
	for _, f := range e.Fields {
		parts = append(parts, f.Field+": "+f.Code)
	}
	return "invalid filing request (" + strings.Join(parts, ", ") + ")"
}

// Limits of the filing.
const (
	MinPlace          = 3
	MaxPlace          = 160
	MinDescription    = 10
	MaxDescription    = 1000
	MaxDetails        = 20
	MaxDetailValue    = 200
	MaxIncidentAgeDay = 5 * 365
)

var (
	policyIDPattern  = regexp.MustCompile(`^[A-Za-z0-9-]{3,40}$`)
	detailKeyPattern = regexp.MustCompile(`^[A-Za-z0-9_]{1,40}$`)
)

// FilingRequest is what the assistant collected across its four stages.
type FilingRequest struct {
	PolicyID     string
	IncidentType string
	// OccurredAt is RFC 3339: the instant of the incident, as the phone read it.
	OccurredAt  string
	Place       string
	Description string
	// Details are the answers to the questions that depend on the incident type.
	Details map[string]string
	// DeclarationAccepted is the declaration under oath that closes the filing.
	DeclarationAccepted bool
	// Evidence is what was captured in the place and already uploaded to the
	// evidence store (HU-M2.1-01).
	Evidence []EvidenceRequest
	// EstimatedAmountMinor is the optional estimate of the loss in cents of
	// COP as it came in the request, with the rules of the web filing
	// (HU-W2.2-01): empty when it was not sent.
	EstimatedAmountMinor string
}

// Claim is the filed case. The repository assigns FilingNumber.
type Claim struct {
	ID           string
	FilingNumber string
	// Subject is the "sub" of the token: who filed it.
	Subject string
	// FilingKey identifies the filing for that subject, so a retry does not
	// create a second case.
	FilingKey    string
	PolicyID     string
	IncidentType IncidentType
	OccurredAt   time.Time
	Place        string
	Description  string
	Details      map[string]string
	// Evidence is what documents the case, in the order it was captured.
	Evidence []Evidence
	Status   Status
	FiledAt  time.Time
	// EstimatedAmountMinor is what the insured estimated the loss at, in
	// cents of COP, declared in either channel (HU-W2.2-01 on the web,
	// HU-M2.2-01 on the phone); nil when it was not declared.
	EstimatedAmountMinor *int64
}

// FilingNumber builds the number the insured sees and quotes to support:
// SIN-<year>-<five digits>.
func FilingNumber(at time.Time, sequence int64) string {
	return fmt.Sprintf("SIN-%d-%05d", at.UTC().Year(), sequence)
}

// Fingerprint summarizes what was filed. Two filings with the same key are
// the same filing only when their fingerprints match. The estimated amount
// counts only when it was declared, so the claims filed before it keep their
// fingerprint and their retries still find them.
func (c Claim) Fingerprint() string {
	if c.EstimatedAmountMinor == nil {
		return c.factsFingerprint()
	}
	sum := sha256.Sum256([]byte(c.factsFingerprint() + "\nestimated_amount=" +
		strconv.FormatInt(*c.EstimatedAmountMinor, 10)))
	return hex.EncodeToString(sum[:])
}

// factsFingerprint summarizes the facts of the filing without the estimated
// amount, which the web fingerprint adds on its own.
func (c Claim) factsFingerprint() string {
	keys := make([]string, 0, len(c.Details))
	for k := range c.Details {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var b strings.Builder
	b.WriteString(c.PolicyID + "\n")
	b.WriteString(string(c.IncidentType) + "\n")
	b.WriteString(c.OccurredAt.UTC().Format(time.RFC3339Nano) + "\n")
	b.WriteString(c.Place + "\n")
	b.WriteString(c.Description + "\n")
	for _, k := range keys {
		b.WriteString(k + "=" + c.Details[k] + "\n")
	}
	// The evidence is part of what was filed: the same key with other
	// photographs is another filing, not a retry of this one.
	for _, e := range c.Evidence {
		b.WriteString(e.ObjectKey + "\n")
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])
}

// Validate checks the request at the given time and returns the claim it
// describes, or a *ValidationError naming every invalid field.
func (r FilingRequest) Validate(now time.Time) (Claim, error) {
	var fields []FieldError
	bad := func(field, code string) { fields = append(fields, FieldError{Field: field, Code: code}) }

	policyID := strings.TrimSpace(r.PolicyID)
	switch {
	case policyID == "":
		bad("policy_id", CodeRequired)
	case !policyIDPattern.MatchString(policyID):
		bad("policy_id", CodeInvalidFormat)
	}

	incident := IncidentType(strings.TrimSpace(r.IncidentType))
	switch {
	case incident == "":
		bad("incident_type", CodeRequired)
	case !incident.Valid():
		bad("incident_type", CodeInvalidFormat)
	}

	occurredAt := occurred(strings.TrimSpace(r.OccurredAt), now, bad)

	place := strings.TrimSpace(r.Place)
	switch {
	case place == "":
		bad("place", CodeRequired)
	case len([]rune(place)) < MinPlace, len([]rune(place)) > MaxPlace:
		bad("place", CodeOutOfRange)
	}

	description := strings.TrimSpace(r.Description)
	switch {
	case description == "":
		bad("description", CodeRequired)
	case len([]rune(description)) < MinDescription, len([]rune(description)) > MaxDescription:
		bad("description", CodeOutOfRange)
	}

	details := normalizeDetails(r.Details, bad)
	evidence := validateEvidence(r.Evidence, occurredAt, now, bad)
	amount := estimatedAmount(strings.TrimSpace(r.EstimatedAmountMinor), bad)

	// The declaration under oath is a requirement of the filing, not a
	// preference: without it there is nothing to file.
	if !r.DeclarationAccepted {
		bad("declaration_accepted", CodeRequired)
	}

	if len(fields) > 0 {
		return Claim{}, &ValidationError{Fields: fields}
	}
	return Claim{
		PolicyID:     policyID,
		IncidentType: incident,
		OccurredAt:   occurredAt,
		Place:        place,
		Description:  description,
		Details:      details,
		Evidence:     evidence,
		Status:       StatusActive,

		EstimatedAmountMinor: amount,
	}, nil
}

// occurred parses the instant of the incident. It cannot be in the future,
// and five years is the oldest filing the journey accepts.
func occurred(value string, now time.Time, bad func(field, code string)) time.Time {
	if value == "" {
		bad("occurred_at", CodeRequired)
		return time.Time{}
	}
	at, err := time.Parse(time.RFC3339, value)
	if err != nil {
		bad("occurred_at", CodeInvalidFormat)
		return time.Time{}
	}
	at = at.UTC()
	if at.After(now) || at.Before(now.AddDate(0, 0, -MaxIncidentAgeDay)) {
		bad("occurred_at", CodeOutOfRange)
	}
	return at
}

func normalizeDetails(in map[string]string, bad func(field, code string)) map[string]string {
	out := make(map[string]string, len(in))
	if len(in) > MaxDetails {
		bad("details", CodeOutOfRange)
		return out
	}
	for k, v := range in {
		key := strings.TrimSpace(k)
		value := strings.TrimSpace(v)
		switch {
		case !detailKeyPattern.MatchString(key):
			bad("details", CodeInvalidFormat)
		case len([]rune(value)) > MaxDetailValue:
			bad("details", CodeOutOfRange)
		default:
			out[key] = value
		}
	}
	return out
}
