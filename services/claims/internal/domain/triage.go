package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
	"time"
)

// The automatic priority of the claim (HU-W2.2-01): every claim that enters
// evaluation gets a level from the severity of its claim type and the band of
// its estimated amount, and the level is recalculated when that information
// changes. Each calculation is a record that is never changed.

// Severity is how serious the claim type is for the coverage it affects.
type Severity string

const (
	SeverityHigh   Severity = "high"
	SeverityMedium Severity = "medium"
	SeverityLow    Severity = "low"
)

// AmountBand is where the estimated amount falls against the thresholds of the
// triage policy. AmountUnknown is a claim without an estimated amount.
type AmountBand string

const (
	AmountHigh    AmountBand = "high"
	AmountMedium  AmountBand = "medium"
	AmountLow     AmountBand = "low"
	AmountUnknown AmountBand = "unknown"
)

// PriorityLevel is the triage level: 1 is attended first.
type PriorityLevel int

const (
	Triage1 PriorityLevel = 1
	Triage2 PriorityLevel = 2
	Triage3 PriorityLevel = 3
)

// Valid reports whether l is one of the three levels.
func (l PriorityLevel) Valid() bool {
	return l >= Triage1 && l <= Triage3
}

// LevelOf is the rule of the level (criterion 2, decision D2): Triage 1 when
// the severity or the amount band is high, Triage 3 when both are low, Triage
// 2 otherwise. An unknown amount never moves the level to 1 or to 3.
func LevelOf(s Severity, b AmountBand) PriorityLevel {
	switch {
	case s == SeverityHigh || b == AmountHigh:
		return Triage1
	case s == SeverityLow && b == AmountLow:
		return Triage3
	}
	return Triage2
}

// StatusInEvaluation is a claim the operations team is evaluating
// (HU-W2.2-03).
const StatusInEvaluation Status = "in_evaluation"

// triageableStatuses are the statuses whose claims get a level and are
// recalculated (assumption S5). While the review of the advisor (HU-W2.1-03)
// does not exist, a filed claim is complete and enters evaluation as it is
// filed, so active is one of them; HU-W2.1-03 removes it.
var triageableStatuses = []Status{StatusActive, StatusInEvaluation}

// TriageableStatuses returns the statuses the triage evaluates, in a new
// slice.
func TriageableStatuses() []Status {
	return append([]Status(nil), triageableStatuses...)
}

// Triageable tells whether a claim in the status gets a level and is
// recalculated. A claim that left evaluation keeps its last level.
func Triageable(s Status) bool {
	for _, t := range triageableStatuses {
		if s == t {
			return true
		}
	}
	return false
}

// TriageCause is why a level was calculated (criterion 3).
type TriageCause string

const (
	CauseEnteredEvaluation      TriageCause = "entered_evaluation"
	CauseEstimatedAmountChanged TriageCause = "estimated_amount_changed"
	CauseIncidentTypeChanged    TriageCause = "incident_type_changed"
	CauseCoverageChanged        TriageCause = "coverage_changed"
	// CauseProviderReportReceived is reserved for the report of the provider
	// (HU-W2.3-02), which will add its estimated cost to TriageInput.
	CauseProviderReportReceived TriageCause = "provider_report_received"
	CausePolicyUpdated          TriageCause = "policy_updated"
)

// TriageInput is what a level is calculated from. Two calculations with the
// same input give the same level.
type TriageInput struct {
	IncidentType IncidentType
	Line         PolicyLine
	// Coverage is the coverage of the policy the claim affects: today the one
	// of its claim type (assumption S4).
	Coverage string
	// EstimatedAmountMinor is in cents of COP; nil when the claim has none.
	EstimatedAmountMinor *int64
	PolicyVersion        string
}

// Fingerprint summarizes the input, so a calculation can tell whether
// anything changed since the previous one.
func (in TriageInput) Fingerprint() string {
	amount := "-"
	if in.EstimatedAmountMinor != nil {
		amount = strconv.FormatInt(*in.EstimatedAmountMinor, 10)
	}
	var b strings.Builder
	for _, part := range []string{string(in.IncidentType), string(in.Line), in.Coverage, amount, in.PolicyVersion} {
		b.WriteString(part + "\n")
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])
}

// Diff lists why the input is not the previous one, in a fixed order. It is
// empty when nothing changed.
func (in TriageInput) Diff(previous TriageInput) []TriageCause {
	var causes []TriageCause
	if !sameAmount(in.EstimatedAmountMinor, previous.EstimatedAmountMinor) {
		causes = append(causes, CauseEstimatedAmountChanged)
	}
	if in.IncidentType != previous.IncidentType {
		causes = append(causes, CauseIncidentTypeChanged)
	}
	if in.Coverage != previous.Coverage || in.Line != previous.Line {
		causes = append(causes, CauseCoverageChanged)
	}
	if in.PolicyVersion != previous.PolicyVersion {
		causes = append(causes, CausePolicyUpdated)
	}
	return causes
}

func sameAmount(a, b *int64) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// TargetKind is how the attention target of a level is expressed.
type TargetKind string

const (
	TargetImmediate    TargetKind = "immediate"
	TargetBusinessDays TargetKind = "business_days"
)

// TriageTarget is the attention target of a level (criterion 5) and the
// resolution time the insured is told (criterion 4).
type TriageTarget struct {
	Kind TargetKind
	// BusinessDays is the attention target; 0 when Kind is immediate.
	BusinessDays           int
	ResolutionBusinessDays int
}

// TriageAssessment is the level the policy gives an input, and why.
type TriageAssessment struct {
	Level      PriorityLevel
	Severity   Severity
	AmountBand AmountBand
	Input      TriageInput
	Target     TriageTarget
}

// TriageActorSystem is who calculates every level: the triage is automatic.
const TriageActorSystem = "system"

// TriageRecord is one calculation of the level of a claim. Seq numbers the
// calculations of the claim from 1, so two calculations cannot take the same
// place.
type TriageRecord struct {
	ClaimID    string
	Seq        int
	Assessment TriageAssessment
	Causes     []TriageCause
	// PreviousLevel is nil in the first calculation.
	PreviousLevel *PriorityLevel
	Actor         string
	AssessedAt    time.Time
}

// NextTriage builds the calculation that follows last (nil when the claim has
// none), or reports false when the input did not change and there is nothing
// to record. A changed input is recorded even if the level stays the same
// (assumption S7).
func NextTriage(claimID string, a TriageAssessment, last *TriageRecord, at time.Time) (TriageRecord, bool) {
	r := TriageRecord{ClaimID: claimID, Seq: 1, Assessment: a, Actor: TriageActorSystem, AssessedAt: at.UTC()}
	if last == nil {
		r.Causes = []TriageCause{CauseEnteredEvaluation}
		return r, true
	}
	causes := a.Input.Diff(last.Assessment.Input)
	if len(causes) == 0 {
		return TriageRecord{}, false
	}
	previous := last.Assessment.Level
	r.Seq, r.Causes, r.PreviousLevel = last.Seq+1, causes, &previous
	return r, true
}

// TriageState is where the triage of a claim stands.
type TriageState string

const (
	// TriagePending is a claim in evaluation that has no level yet.
	TriagePending TriageState = "pending"
	// TriageAssessed is a claim with a level. It stays assessed after the
	// claim leaves evaluation, although it is no longer recalculated.
	TriageAssessed TriageState = "assessed"
	// TriageNotApplicable is a claim that never entered evaluation.
	TriageNotApplicable TriageState = "not_applicable"
	// TriageNotFound is a claim that does not exist, in a batch read.
	TriageNotFound TriageState = "not_found"
)

// TriageStateOf tells where the triage of a claim in the status stands, given
// its last calculation (nil when it has none).
func TriageStateOf(status Status, last *TriageRecord) TriageState {
	switch {
	case last != nil:
		return TriageAssessed
	case Triageable(status):
		return TriagePending
	}
	return TriageNotApplicable
}
