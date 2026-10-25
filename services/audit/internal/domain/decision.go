package domain

import (
	"errors"
	"strings"
	"time"
)

// Decision is the final outcome of an underwriting decision (HU-W4.1-01).
type Decision string

// The outcomes an analyst can read.
const (
	DecisionAccepted Decision = "accepted"
	DecisionRejected Decision = "rejected"
	DecisionDeferred Decision = "deferred"
)

// Valid reports whether d is one of the known outcomes.
func (d Decision) Valid() bool {
	return d == DecisionAccepted || d == DecisionRejected || d == DecisionDeferred
}

// SourceConsulted is one data source origination asked to build the profile,
// with what it answered. Status is "ok", "timeout", "error" or "open_circuit"
// as origination reports it; LatencyMS is how long the call took.
type SourceConsulted struct {
	Source    string
	Status    string
	LatencyMS int64
}

// RuleApplied is one rating rule, in the order it was executed, with the
// value it produced.
type RuleApplied struct {
	Order int
	Code  string
	Value float64
}

// DecisionRecord is the immutable audit record of one underwriting decision:
// the input data and the rules that set the price and the outcome.
type DecisionRecord struct {
	// OfferID identifies the underwriting request; it is the key the analyst
	// searches with and the idempotency key of the record.
	OfferID       string
	CustomerID    string
	PartnerID     string
	ApplicationID string
	Channel       string

	// RequestedAt is when the underwriting request was made, in UTC.
	RequestedAt  time.Time
	RaterVersion string

	Sources []SourceConsulted
	// ProfileScore, ProfileCategory and ProfilePartial are what the sources
	// gave once combined.
	ProfileScore    float64
	ProfileCategory string
	ProfilePartial  bool
	Rules           []RuleApplied

	Premium  float64
	Currency string
	Decision Decision
	// DecisionReason explains a deferred or rejected outcome; empty when accepted.
	DecisionReason string

	// RecordedAt is when audit stored the record (the server clock).
	RecordedAt time.Time
}

// ErrDecisionNotFound is returned when no decision matches the id searched.
var ErrDecisionNotFound = errors.New("decision not found")

// Limits of a decision record.
const (
	maxSources = 20
	maxRules   = 50
	maxReason  = 200
	// maxLatencyMS bounds the time a source took (one hour).
	maxLatencyMS = 3_600_000
	// maxRuleOrder bounds the position of a rule.
	maxRuleOrder = 1000
	// maxPremium bounds the premium to a sane amount.
	maxPremium = 1e12
)

// Validate returns a *ValidationError naming every invalid field.
func (r DecisionRecord) Validate() error {
	var fields []FieldError
	bad := func(field, code string) { fields = append(fields, FieldError{Field: field, Code: code}) }

	for field, value := range map[string]string{
		"offer_id": r.OfferID, "customer_id": r.CustomerID, "partner_id": r.PartnerID,
		"application_id": r.ApplicationID, "rater_version": r.RaterVersion,
	} {
		switch {
		case value == "":
			bad(field, "required")
		case len(value) > MaxIdentifierLength || !identifierPattern.MatchString(value):
			bad(field, "invalid")
		}
	}
	if r.Channel == "" || len(r.Channel) > MaxIdentifierLength {
		bad("channel", "invalid")
	}
	if r.RequestedAt.IsZero() {
		bad("requested_at", "required")
	}
	if len(r.Sources) > maxSources {
		bad("sources", "too_many")
	}
	for _, s := range r.Sources {
		if s.Source == "" || s.Status == "" || s.LatencyMS < 0 || s.LatencyMS > maxLatencyMS {
			bad("sources", "invalid")
			break
		}
	}
	if len(r.Rules) == 0 {
		bad("rules", "required")
	}
	if len(r.Rules) > maxRules {
		bad("rules", "too_many")
	}
	for _, rule := range r.Rules {
		if rule.Code == "" || rule.Order < 1 || rule.Order > maxRuleOrder {
			bad("rules", "invalid")
			break
		}
	}
	if r.Premium < 0 || r.Premium > maxPremium {
		bad("premium", "invalid")
	}
	if r.ProfileScore < 0 || r.ProfileScore > 1 {
		bad("profile", "invalid")
	}
	if len(r.Currency) != 3 {
		bad("currency", "invalid")
	}
	if !r.Decision.Valid() {
		bad("decision", "invalid")
	}
	if r.Decision.Valid() && r.Decision != DecisionAccepted && strings.TrimSpace(r.DecisionReason) == "" {
		bad("decision_reason", "required")
	}
	if len(r.DecisionReason) > maxReason {
		bad("decision_reason", "invalid")
	}
	if len(fields) > 0 {
		sortFields(fields)
		return &ValidationError{Fields: fields}
	}
	return nil
}

// sortFields makes the order of the reported fields deterministic (the checks
// above iterate a map).
func sortFields(fields []FieldError) {
	for i := 1; i < len(fields); i++ {
		for j := i; j > 0 && fields[j].Field < fields[j-1].Field; j-- {
			fields[j], fields[j-1] = fields[j-1], fields[j]
		}
	}
}
