// Package domain holds the origination business types. It has no dependency
// on adapters or infrastructure libraries.
package domain

import "time"

// RiskProfile is the individual risk profile used to price an offer.
type RiskProfile struct {
	CustomerID string
	Score      float64
	Category   string
	// CalculatedAt is when the profile was calculated from the external source.
	// It is the zero time for the base actuarial profile.
	CalculatedAt time.Time
	// Partial is true when the profile was calculated online without some of
	// the external sources, because they missed their latency budget or
	// failed (HA-L01).
	Partial bool
	// Sources is the outcome of each external source consulted to calculate
	// the profile, in priority order. It is empty when a single source answered
	// directly (HA-D01) and for cached and base profiles.
	Sources []SourceOutcome
}

// FreshSource tells which source calculated a fresh profile: Open Finance
// alone, as in HA-D01, or the parallel fan-out over several external sources.
func (p RiskProfile) FreshSource() ProfileSource {
	if len(p.Sources) == 0 || (len(p.Sources) == 1 && p.Sources[0].Source == ExternalSourceOpenFinance) {
		return SourceOpenFinance
	}
	return SourceExternal
}

// ProfileSource tells where the profile used for an offer came from.
type ProfileSource string

const (
	SourceOpenFinance   ProfileSource = "open_finance"
	SourceCache         ProfileSource = "cache"
	SourceBaseActuarial ProfileSource = "base_actuarial"
	// SourceExternal is a profile calculated online by the parallel fan-out
	// over several external sources (HA-L01).
	SourceExternal ProfileSource = "external_sources"
)

// OfferMode is "normal" when the profile is fresh and "provisional" when the
// offer was built in degraded mode.
type OfferMode string

const (
	ModeNormal      OfferMode = "normal"
	ModeProvisional OfferMode = "provisional"
)

// ExternalSourceOpenFinance names the external data source reported as
// unavailable when an offer is provisional.
const ExternalSourceOpenFinance = "open_finance"

// Names of the other external sources consulted by the parallel profiling (HA-L01).
const (
	ExternalSourceOpenData = "open_data"
	ExternalSourceKYC      = "kyc"
)

// SourceStatus is how one external source ended in the parallel profiling.
type SourceStatus string

const (
	// SourceStatusOK means the source answered inside its latency budget.
	SourceStatusOK SourceStatus = "ok"
	// SourceStatusTimeout means the source did not answer inside its budget
	// (or the whole fan-out deadline) and its call was canceled.
	SourceStatusTimeout SourceStatus = "timeout"
	// SourceStatusError means the source answered with an error.
	SourceStatusError SourceStatus = "error"
	// SourceStatusCircuitOpen means the source was isolated by its circuit
	// breaker and was not called.
	SourceStatusCircuitOpen SourceStatus = "circuit_open"
)

// SourceOutcome is the result of consulting one external source.
type SourceOutcome struct {
	Source  string
	Status  SourceStatus
	Latency time.Duration
}

// ProfileResolution is the outcome of resolving a customer's risk profile.
type ProfileResolution struct {
	Profile RiskProfile
	Mode    OfferMode
	Source  ProfileSource
	// UnavailableSource is set only in provisional mode.
	UnavailableSource string
	// Sources is the outcome of each external source consulted for this
	// resolution (HA-L01), in normal and in provisional mode.
	Sources []SourceOutcome
}

// BaseCategory is the category of the base actuarial profile.
const BaseCategory = "base_actuarial"

// BaseActuarialProfile is the conservative profile used when there is neither
// an Open Finance answer nor a valid cached profile (last degradation step).
func BaseActuarialProfile(customerID string) RiskProfile {
	return RiskProfile{CustomerID: customerID, Score: 0.5, Category: BaseCategory}
}
