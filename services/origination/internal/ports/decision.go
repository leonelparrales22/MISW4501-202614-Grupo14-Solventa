package ports

import (
	"context"
	"time"
)

// DecisionSource is one data source consulted for a decision, with the
// outcome and the time it took.
type DecisionSource struct {
	Source    string
	Status    string
	LatencyMS int64
	// ConsultedAt is when the call started; zero when unknown.
	ConsultedAt time.Time
	// Score and Category are what the source returned; nil / empty when it
	// did not answer.
	Score    *float64
	Category string
}

// DecisionRule is one rating rule, in execution order, with its output value.
type DecisionRule struct {
	Order int
	Code  string
	Value float64
}

// DecisionRecord is what origination hands the audit service for every
// underwriting decision (HU-W4.1-01): the input data and the rules that set
// the price and the outcome. It carries no document or contact data.
type DecisionRecord struct {
	OfferID       string
	CustomerID    string
	PartnerID     string
	ApplicationID string
	// ConsentID is the consent the profile was consulted under.
	ConsentID    string
	Channel      string
	RequestedAt  time.Time
	RaterVersion string

	Sources         []DecisionSource
	ProfileScore    float64
	ProfileCategory string
	ProfilePartial  bool
	Rules           []DecisionRule

	Premium  float64
	Currency string
	// Decision is "accepted", "rejected" or "deferred"; DecisionReason says
	// why when it is not accepted.
	Decision       string
	DecisionReason string
}

// DecisionRecorder sends the decision to the owner of the audit trail (the
// audit service). Recording is idempotent by offer id.
type DecisionRecorder interface {
	Record(ctx context.Context, r DecisionRecord) error
}
