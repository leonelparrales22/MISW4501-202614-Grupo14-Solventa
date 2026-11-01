package domain

import (
	"errors"
	"time"
)

// PolicyLine is the line of business of a policy, as the policy core names it
// (HU-W2.1-01).
type PolicyLine string

const (
	LineAuto   PolicyLine = "auto"
	LineHome   PolicyLine = "home"
	LineTravel PolicyLine = "travel"
)

// PolicyStatus is the status the policy core gives a policy. Only active
// policies take claims; any other value is simply not active.
type PolicyStatus string

const PolicyActive PolicyStatus = "active"

// Policy is a policy of the insured as the policy core describes it. A claim
// keeps a copy of it, taken when it was filed (decision D7), so the checks of
// the filing can be rebuilt even if the policy changes later.
type Policy struct {
	ID            string
	Line          PolicyLine
	Product       string
	InsuredObject string
	Status        PolicyStatus
	// ValidFrom is the first instant the policy covers and ValidTo the first
	// one it no longer does: the validity is [ValidFrom, ValidTo).
	ValidFrom        time.Time
	ValidTo          time.Time
	CoveredIncidents []IncidentType
}

// Why a policy does not take a claim (HU-W2.1-01 criterion 3).
var (
	ErrPolicyNotActive         = errors.New("claims: the policy is not active")
	ErrIncidentNotCovered      = errors.New("claims: the policy does not cover the incident type")
	ErrOccurredOutsideValidity = errors.New("claims: the incident happened outside the validity of the policy")
)

// ActiveAt tells whether the policy is in force at the instant: its status is
// active and the instant is within its validity.
func (p Policy) ActiveAt(at time.Time) bool {
	return p.Status == PolicyActive && p.Within(at)
}

// Within tells whether the instant falls in [ValidFrom, ValidTo).
func (p Policy) Within(at time.Time) bool {
	return !at.Before(p.ValidFrom) && at.Before(p.ValidTo)
}

// Covers tells whether the policy covers the incident type: the core lists it
// and the catalog files it under the line of the policy.
func (p Policy) Covers(catalog Catalog, incident IncidentType) bool {
	option, ok := catalog.Incident(incident)
	if !ok || option.Line != p.Line {
		return false
	}
	for _, covered := range p.CoveredIncidents {
		if covered == incident {
			return true
		}
	}
	return false
}

// EligibleFor checks that a claim of the type, for an incident at occurredAt,
// can be filed now on the policy. The reasons are checked in this order: the
// policy is not active, it does not cover the type, the incident happened
// outside its validity.
func (p Policy) EligibleFor(catalog Catalog, incident IncidentType, occurredAt, now time.Time) error {
	switch {
	case !p.ActiveAt(now):
		return ErrPolicyNotActive
	case !p.Covers(catalog, incident):
		return ErrIncidentNotCovered
	case !p.Within(occurredAt):
		return ErrOccurredOutsideValidity
	}
	return nil
}

// Copy returns a policy that shares nothing with p, so the copy kept in a
// claim cannot change with the original.
func (p Policy) Copy() Policy {
	p.CoveredIncidents = append([]IncidentType(nil), p.CoveredIncidents...)
	return p
}
