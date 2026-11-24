package domain

import (
	"errors"
	"math"
	"time"
)

// AssistancePolicyVersionV1 names the assistance policy of HU-W2.3-03
// (decision D1). It is not defined by the business yet: if it changes, it
// changes its version, and every dispatch keeps the version it was made with.
const AssistancePolicyVersionV1 = "assistance-policy-v1"

// AssistanceNeed is what the insured needs, as the analyst records it on the
// call (decision D1). It gives the specialty of the suggestion.
type AssistanceNeed string

const (
	NeedTowing         AssistanceNeed = "towing"
	NeedBattery        AssistanceNeed = "battery"
	NeedFlatTire       AssistanceNeed = "flat_tire"
	NeedFuel           AssistanceNeed = "fuel"
	NeedVehicleLockout AssistanceNeed = "vehicle_lockout"
	// NeedPlumbingEmergency is a burst pipe or a leak at home.
	NeedPlumbingEmergency AssistanceNeed = "plumbing_emergency"
)

// ErrNeedNotForLine is a need the line of the policy has not (assumption S4):
// a home policy does not take a tow truck, and travel has no on-site need.
var ErrNeedNotForLine = errors.New("claims: the need is not one of the line of the policy")

// CloseReason is why a request was closed without a dispatch (assumption
// S10).
type CloseReason string

const (
	CloseResolvedByPhone     CloseReason = "resolved_by_phone"
	CloseDuplicate           CloseReason = "duplicate"
	CloseCancelledByInsured  CloseReason = "cancelled_by_insured"
	CloseNotCovered          CloseReason = "not_covered"
	CloseNoProviderAvailable CloseReason = "no_provider_available"
	// CloseOther needs a note that says why.
	CloseOther CloseReason = "other"
)

// NeedRule is the specialty a need of a line asks for.
type NeedRule struct {
	Line      PolicyLine
	Need      AssistanceNeed
	Specialty Specialty
}

// AssistanceInboxPolicy is a version of the needs of each line, of how the
// arrival is estimated, of the reasons to close and of the target time of a
// dispatch. The radii of the search are those of the assignment policy
// (decision D5).
type AssistanceInboxPolicy struct {
	Version string
	Needs   []NeedRule
	// PreparationMinutes and AverageSpeedKmh estimate the arrival
	// (assumption S8).
	PreparationMinutes int
	AverageSpeedKmh    float64
	CloseReasons       []CloseReason
	// DispatchTarget is the target from the request to its dispatch.
	DispatchTarget time.Duration
}

// AssistanceInboxPolicyV1 returns the provisional policy (assumptions S4, S8
// and S10). Each call returns a fresh copy, so no caller can change the
// policy of the others.
func AssistanceInboxPolicyV1() AssistanceInboxPolicy {
	return AssistanceInboxPolicy{
		Version: AssistancePolicyVersionV1,
		// Travel has no on-site assistance: such a request is closed with
		// its reason.
		Needs: []NeedRule{
			{Line: LineAuto, Need: NeedTowing, Specialty: SpecialtyTowing},
			{Line: LineAuto, Need: NeedBattery, Specialty: SpecialtyRoadsideAssistance},
			{Line: LineAuto, Need: NeedFlatTire, Specialty: SpecialtyRoadsideAssistance},
			{Line: LineAuto, Need: NeedFuel, Specialty: SpecialtyRoadsideAssistance},
			{Line: LineAuto, Need: NeedVehicleLockout, Specialty: SpecialtyRoadsideAssistance},
			{Line: LineHome, Need: NeedPlumbingEmergency, Specialty: SpecialtyPlumbing},
		},
		PreparationMinutes: 10,
		AverageSpeedKmh:    30,
		CloseReasons: []CloseReason{CloseResolvedByPhone, CloseDuplicate, CloseCancelledByInsured, CloseNotCovered,
			CloseNoProviderAvailable, CloseOther},
		// "Objetivo: menos de 5 min" (mockup 39:4301).
		DispatchTarget: 5 * time.Minute,
	}
}

// Known reports whether the need is one of the policy, of any line.
func (p AssistanceInboxPolicy) Known(n AssistanceNeed) bool {
	for _, r := range p.Needs {
		if r.Need == n {
			return true
		}
	}
	return false
}

// SpecialtyOf returns the specialty the need of the line asks for, or
// ErrNeedNotForLine.
func (p AssistanceInboxPolicy) SpecialtyOf(line PolicyLine, n AssistanceNeed) (Specialty, error) {
	for _, r := range p.Needs {
		if r.Line == line && r.Need == n {
			return r.Specialty, nil
		}
	}
	return "", ErrNeedNotForLine
}

// Closes reports whether the reason is one of the policy.
func (p AssistanceInboxPolicy) Closes(r CloseReason) bool { return contains(p.CloseReasons, r) }

// EtaMinutes estimates the arrival of a provider at the straight-line
// distance: the preparation plus the distance at the average speed, rounded
// up to the minute (assumption S8). It is an estimate, not a route.
func (p AssistanceInboxPolicy) EtaMinutes(distanceKm float64) int {
	return p.PreparationMinutes + int(math.Ceil(distanceKm*60/p.AverageSpeedKmh))
}
