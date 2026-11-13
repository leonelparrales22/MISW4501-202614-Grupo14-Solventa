package domain

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
)

// HU-M2.4-01 (criterion 3): a parametric claim is resolved automatically when
// an external event crosses the threshold of its parameter. The claim is filed,
// approved and paid without anybody reviewing it, and it keeps the trigger
// (the parameter, the threshold and the observed value) so the timeline of the
// app can tell it apart from a claim evaluated by an analyst.

// ChannelParametric is a claim the parametric engine filed by itself: nobody
// filed it from the phone or the portal.
const ChannelParametric Channel = "parametric"

// IncidentFlightDelay is the delay of a flight covered by a travel policy. The
// policy core names the parameter the same way it names the covered incident.
const IncidentFlightDelay IncidentType = "flight_delay"

// ParametricEngine is who decides a claim that was resolved automatically.
const ParametricEngine = "parametric-engine"

// ParametricRulesVersion is the version of the rules this code applies.
const ParametricRulesVersion = "parametric-rules-v1"

// ParametricRule pays a fixed amount when the observed value of a parameter
// reaches the threshold.
type ParametricRule struct {
	Parameter   IncidentType
	Unit        string
	Threshold   float64
	PayoutMinor int64
	Currency    string
}

// ParametricRules are the rules of the parametric engine.
type ParametricRules struct {
	Version string
	Rules   []ParametricRule
}

// DefaultParametricRules are the rules of the prototype: a flight 180 minutes
// late or more pays $ 180.000 COP.
func DefaultParametricRules() ParametricRules {
	return ParametricRules{Version: ParametricRulesVersion, Rules: []ParametricRule{
		{Parameter: IncidentFlightDelay, Unit: "minutes", Threshold: 180, PayoutMinor: 18_000_000, Currency: "COP"},
	}}
}

// Rule returns the rule of the parameter, or false when nothing pays for it.
func (r ParametricRules) Rule(parameter IncidentType) (ParametricRule, bool) {
	for _, rule := range r.Rules {
		if rule.Parameter == parameter {
			return rule, true
		}
	}
	return ParametricRule{}, false
}

// Observation is an external event already matched to the policy it concerns:
// the delay of the flight of an insured, as the telemetry reports it.
type Observation struct {
	// ID identifies the observation; the same observation again resolves
	// nothing new.
	ID             string
	Subject        string
	PolicyID       string
	Parameter      IncidentType
	EventReference string
	Observed       float64
	Unit           string
	ObservedAt     time.Time
}

// Trigger is what made the engine pay: the parameter, its threshold and the
// value observed.
type Trigger struct {
	Parameter      IncidentType
	EventReference string
	Threshold      float64
	Observed       float64
	Unit           string
	ObservedAt     time.Time
}

// ParametricResolution is the automatic resolution of a claim.
type ParametricResolution struct {
	ObservationID string
	RuleVersion   string
	Trigger       Trigger
	ResolvedAt    time.Time
}

// Why an observation resolves nothing. ErrInvalidObservation is a malformed
// event; the others are observations that do not pay.
var (
	ErrInvalidObservation  = errors.New("claims: the observation is not valid")
	ErrParameterNotCovered = errors.New("claims: no rule or coverage pays for the parameter")
	ErrBelowThreshold      = errors.New("claims: the observed value does not reach the threshold")
)

// Validate checks that the observation has everything the engine reads.
func (o Observation) Validate() error {
	var missing []string
	for name, value := range map[string]string{
		"observation_id": o.ID, "subject": o.Subject, "policy_id": o.PolicyID,
		"parameter": string(o.Parameter), "event_reference": o.EventReference, "unit": o.Unit,
	} {
		if strings.TrimSpace(value) == "" {
			missing = append(missing, name)
		}
	}
	switch {
	case len(missing) > 0:
		return fmt.Errorf("%w: missing %s", ErrInvalidObservation, strings.Join(missing, ", "))
	case o.ObservedAt.IsZero():
		return fmt.Errorf("%w: missing observed_at", ErrInvalidObservation)
	case math.IsNaN(o.Observed) || math.IsInf(o.Observed, 0) || o.Observed < 0:
		return fmt.Errorf("%w: observed must be a non-negative number", ErrInvalidObservation)
	}
	return nil
}

// ParametricFilingKey is the filing key of the claim of an observation: the
// same observation finds the same claim instead of filing a second one.
func ParametricFilingKey(observationID string) string { return "parametric-" + observationID }

// ResolveParametric files the claim of an observation that crosses the
// threshold of its rule and approves it for the fixed amount of the rule (at
// most the insured value of the coverage, with no deductible). The claim is
// born in evaluation and the approval moves it on; the payout is due at once.
// id is the id of the new claim; terms and holder are the policy and the
// insured as the policy core describes them now.
func ResolveParametric(id string, o Observation, rules ParametricRules, terms PolicyTerms, holder Holder,
	now time.Time) (ClaimRecord, ParametricResolution, Decision, error) {
	if err := o.Validate(); err != nil {
		return ClaimRecord{}, ParametricResolution{}, Decision{}, err
	}
	rule, ok := rules.Rule(o.Parameter)
	if !ok || rule.Unit != o.Unit {
		return ClaimRecord{}, ParametricResolution{}, Decision{}, ErrParameterNotCovered
	}
	policy := terms.Policy
	if !policy.ActiveAt(o.ObservedAt) {
		return ClaimRecord{}, ParametricResolution{}, Decision{}, ErrPolicyNotActive
	}
	cov, ok := terms.CoverageOf(o.Parameter)
	if !ok {
		return ClaimRecord{}, ParametricResolution{}, Decision{}, ErrParameterNotCovered
	}
	if o.Observed < rule.Threshold {
		return ClaimRecord{}, ParametricResolution{}, Decision{}, ErrBelowThreshold
	}
	now = now.UTC()
	c := ClaimRecord{
		Claim: Claim{
			ID: id, Subject: o.Subject, FilingKey: ParametricFilingKey(o.ID), PolicyID: o.PolicyID,
			IncidentType: o.Parameter, OccurredAt: o.ObservedAt.UTC(), Place: o.EventReference,
			Description: fmt.Sprintf("Resolución automática: %s %s, %s %s (umbral %s %s).",
				parameterLabel(o.Parameter), o.EventReference, number(o.Observed), o.Unit, number(rule.Threshold), o.Unit),
			Details: map[string]string{"event_reference": o.EventReference},
			Status:  StatusInEvaluation, FiledAt: now,
		},
		Channel: ChannelParametric, Policy: &policy,
	}
	resolution := ParametricResolution{
		ObservationID: o.ID, RuleVersion: rules.Version, ResolvedAt: now,
		Trigger: Trigger{Parameter: o.Parameter, EventReference: o.EventReference, Threshold: rule.Threshold,
			Observed: o.Observed, Unit: o.Unit, ObservedAt: o.ObservedAt.UTC()},
	}
	// The rule pays a fixed amount, with no deductible, up to the insured
	// value of the coverage.
	amount := min(rule.PayoutMinor, cov.InsuredValueMinor)
	cov.DeductibleBasisPoints = 0
	d, err := Approve(c, cov, holder, amount, ParametricEngine, now)
	if err != nil {
		return ClaimRecord{}, ParametricResolution{}, Decision{}, err
	}
	return c, resolution, d, nil
}

// parameterLabel is how the description of the claim names the parameter.
func parameterLabel(p IncidentType) string {
	if p == IncidentFlightDelay {
		return "retraso del vuelo"
	}
	return string(p)
}

// number writes a value without decimals when it has none: 215, not 215.000000.
func number(v float64) string {
	if v == math.Trunc(v) {
		return fmt.Sprintf("%.0f", v)
	}
	return fmt.Sprintf("%g", v)
}
