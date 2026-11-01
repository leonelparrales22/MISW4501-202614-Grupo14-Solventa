package domain

// TriagePolicyVersionV1 names the triage policy of HU-W2.2-01 (decision D5).
// It is not defined by the business yet: if it changes, it changes its
// version, every calculation keeps the version it was made with and the
// claims in evaluation are recalculated with the cause policy_updated.
const TriagePolicyVersionV1 = "triage-policy-v1"

// TriageCurrency is the currency of the amounts of the triage.
const TriageCurrency = "COP"

// SeverityRule is the severity of a claim type of a line.
type SeverityRule struct {
	Line     PolicyLine
	Incident IncidentType
	Severity Severity
}

// LevelTarget is the target of a level.
type LevelTarget struct {
	Level  PriorityLevel
	Target TriageTarget
}

// TriagePolicy is a version of the severity table, the amount thresholds and
// the target of each level (criteria 2 and 5).
type TriagePolicy struct {
	Version  string
	Severity []SeverityRule
	// LowBelowMinor and HighAboveMinor are in cents of COP: an amount below
	// the first is low, above the second is high, and medium in between, both
	// thresholds included.
	LowBelowMinor  int64
	HighAboveMinor int64
	Targets        []LevelTarget
}

// TriagePolicyV1 returns the provisional policy (assumptions S1 to S3). Each
// call returns a fresh copy, so no caller can change the policy of the others.
func TriagePolicyV1() TriagePolicy {
	return TriagePolicy{
		Version: TriagePolicyVersionV1,
		// Theft (total loss) and damage to third parties (civil liability,
		// possibly injured people) are the most urgent; glass, the least.
		Severity: []SeverityRule{
			{Line: LineAuto, Incident: IncidentCollision, Severity: SeverityMedium},
			{Line: LineAuto, Incident: IncidentTheft, Severity: SeverityHigh},
			{Line: LineAuto, Incident: IncidentThirdPartyDamage, Severity: SeverityHigh},
			{Line: LineAuto, Incident: IncidentGlass, Severity: SeverityLow},
			{Line: LineAuto, Incident: IncidentOther, Severity: SeverityMedium},
			{Line: LineHome, Incident: IncidentWaterDamage, Severity: SeverityMedium},
		},
		// COP 5,000,000 and COP 30,000,000: the mockup labels 4.8 million as a
		// low amount and puts 48 million in Triage 1.
		LowBelowMinor:  5_000_000_00,
		HighAboveMinor: 30_000_000_00,
		Targets: []LevelTarget{
			{Level: Triage1, Target: TriageTarget{Kind: TargetImmediate, ResolutionBusinessDays: 2}},
			{Level: Triage2, Target: TriageTarget{Kind: TargetBusinessDays, BusinessDays: 5, ResolutionBusinessDays: 5}},
			{Level: Triage3, Target: TriageTarget{Kind: TargetBusinessDays, BusinessDays: 10, ResolutionBusinessDays: 10}},
		},
	}
}

// SeverityOf returns the severity of the claim type of the line. A type the
// table does not know is medium, and known is false so the caller can log it.
func (p TriagePolicy) SeverityOf(line PolicyLine, incident IncidentType) (s Severity, known bool) {
	for _, r := range p.Severity {
		if r.Line == line && r.Incident == incident {
			return r.Severity, true
		}
	}
	return SeverityMedium, false
}

// BandOf places the amount, in cents, against the thresholds.
func (p TriagePolicy) BandOf(amountMinor *int64) AmountBand {
	switch {
	case amountMinor == nil:
		return AmountUnknown
	case *amountMinor < p.LowBelowMinor:
		return AmountLow
	case *amountMinor > p.HighAboveMinor:
		return AmountHigh
	}
	return AmountMedium
}

// TargetOf returns the target of the level, or the zero target for a level
// the policy does not define.
func (p TriagePolicy) TargetOf(l PriorityLevel) TriageTarget {
	for _, t := range p.Targets {
		if t.Level == l {
			return t.Target
		}
	}
	return TriageTarget{}
}

// InputOf takes from the claim what its level is calculated from. The line
// comes from the catalog the claim was filed with and the coverage is the one
// of its claim type (assumption S4); the policy core is never asked.
func (p TriagePolicy) InputOf(c ClaimRecord) TriageInput {
	in := TriageInput{
		IncidentType:         c.IncidentType,
		Coverage:             string(c.IncidentType),
		EstimatedAmountMinor: c.EstimatedAmountMinor,
		PolicyVersion:        p.Version,
	}
	if option, ok := CatalogFor(c.CatalogVersion).Incident(c.IncidentType); ok {
		in.Line = option.Line
	}
	if in.EstimatedAmountMinor != nil {
		amount := *in.EstimatedAmountMinor
		in.EstimatedAmountMinor = &amount
	}
	return in
}

// Assess calculates the level of the input under this policy. known is false
// when the claim type is not in the severity table (it is assessed as medium).
func (p TriagePolicy) Assess(in TriageInput) (a TriageAssessment, known bool) {
	in.PolicyVersion = p.Version
	severity, known := p.SeverityOf(in.Line, in.IncidentType)
	band := p.BandOf(in.EstimatedAmountMinor)
	level := LevelOf(severity, band)
	return TriageAssessment{Level: level, Severity: severity, AmountBand: band, Input: in, Target: p.TargetOf(level)}, known
}
