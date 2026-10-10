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
}

// ProfileSource tells where the profile used for an offer came from.
type ProfileSource string

const (
	SourceOpenFinance   ProfileSource = "open_finance"
	SourceCache         ProfileSource = "cache"
	SourceBaseActuarial ProfileSource = "base_actuarial"
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

// ProfileResolution is the outcome of resolving a customer's risk profile.
type ProfileResolution struct {
	Profile RiskProfile
	Mode    OfferMode
	Source  ProfileSource
	// UnavailableSource is set only in provisional mode.
	UnavailableSource string
}

// BaseCategory is the category of the base actuarial profile.
const BaseCategory = "base_actuarial"

// BaseActuarialProfile is the conservative profile used when there is neither
// an Open Finance answer nor a valid cached profile (last degradation step).
func BaseActuarialProfile(customerID string) RiskProfile {
	return RiskProfile{CustomerID: customerID, Score: 0.5, Category: BaseCategory}
}
