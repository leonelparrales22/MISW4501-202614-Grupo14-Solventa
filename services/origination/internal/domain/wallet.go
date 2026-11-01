package domain

import "time"

// PolicySource is where a policy of the wallet comes from (HU-M1.2-01).
type PolicySource string

const (
	// SourceOrigination is a policy issued by origination: its charge
	// activated the coverage and the insured can renew it (HU-W1.5-02).
	SourceOrigination PolicySource = "origination"
	// SourcePolicyCore is a policy of the policy core, the system of record
	// of the insured's auto, home and travel policies. Origination cannot
	// renew it.
	SourcePolicyCore PolicySource = "policy_core"
)

// PolicyLine is the kind of insurance of a policy, as the policy core names
// it. LineLife is the line of every policy origination issues.
type PolicyLine string

const (
	LineLife   PolicyLine = "life"
	LineAuto   PolicyLine = "auto"
	LineHome   PolicyLine = "home"
	LineTravel PolicyLine = "travel"
)

// PolicyStatus is where a policy of the wallet stands.
type PolicyStatus string

const (
	PolicyActive    PolicyStatus = "active"
	PolicyExpired   PolicyStatus = "expired"
	PolicyCancelled PolicyStatus = "cancelled"
)

// PolicyHolder is the main insured of a policy, as the wallet shows it: who
// they are, never how to reach them.
type PolicyHolder struct {
	FullName     string
	DocumentType string
	DocumentID   string
}

// WalletPolicy is one policy of the insured as the wallet lists it, whatever
// its source (HU-M1.2-01). Premium is nil when the source does not know it:
// the policy core keeps no premiums.
type WalletPolicy struct {
	ID            string
	Source        PolicySource
	Line          PolicyLine
	Product       string
	InsuredObject string
	Status        PolicyStatus
	Holder        PolicyHolder
	Premium       *float64
	Currency      string
	InsuredAmount float64
	Coverages     []Coverage
	CoverageStart time.Time
	CoverageEnd   time.Time
	// RenewalOf is the policy this one renews; empty for new business.
	RenewalOf string
}

// StatusAt is the status of a policy issued by origination at the given
// time: active until its coverage ends, expired afterwards. Origination does
// not cancel policies yet, so it never answers cancelled.
func (p Policy) StatusAt(now time.Time) PolicyStatus {
	if now.After(p.CoverageEnd) {
		return PolicyExpired
	}
	return PolicyActive
}

// WalletPolicyOf is the wallet view of a policy issued by origination at the
// given time. Every policy origination issues is a life policy.
func WalletPolicyOf(p Policy, now time.Time) WalletPolicy {
	premium := p.Premium
	return WalletPolicy{
		ID: p.ID, Source: SourceOrigination, Line: LineLife, Status: p.StatusAt(now),
		Holder:  PolicyHolder{FullName: p.Holder.FullName, DocumentID: p.Holder.DocumentID},
		Premium: &premium, Currency: p.Currency, InsuredAmount: p.InsuredAmount,
		Coverages:     append([]Coverage(nil), p.Coverages...),
		CoverageStart: p.CoverageStart, CoverageEnd: p.CoverageEnd, RenewalOf: p.RenewalOf,
	}
}
