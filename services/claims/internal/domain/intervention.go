package domain

import (
	"errors"
	"strings"
	"time"
)

// Errors of the follow-up of the intervention and the closing of the case
// (HU-W2.3-02).
var (
	// ErrNotAssigned is a claim with no provider assigned: there is no
	// intervention to follow.
	ErrNotAssigned = errors.New("claims: the claim has no provider assigned")
	// ErrStepOutOfOrder is a step that is not the next one of the
	// intervention: recorded already, or one before it is missing.
	ErrStepOutOfOrder = errors.New("claims: the step is not the next one of the intervention")
	// ErrCaseClosed is a case closed already: its intervention takes no more
	// steps and it is not closed twice.
	ErrCaseClosed = errors.New("claims: the case is closed already")
	// ErrInterventionNotFinished is a case whose provider has not reported
	// the intervention finished (criterion 4).
	ErrInterventionNotFinished = errors.New("claims: the intervention of the provider is not finished")
)

// NotFinishedError is a closing refused because the intervention is not
// finished: Missing are the steps still to record, in order, so the analyst
// knows which condition is missing (criterion 4).
type NotFinishedError struct {
	Missing []InterventionStep
}

func (e *NotFinishedError) Error() string { return ErrInterventionNotFinished.Error() }

// Unwrap lets errors.Is find ErrInterventionNotFinished.
func (e *NotFinishedError) Unwrap() error { return ErrInterventionNotFinished }

// InterventionStep is a progress of the provider the analyst records, as the
// provider reports it through the agreed channel (criterion 1). The
// assignment of HU-W2.3-01 is where the intervention starts; it is not a step.
type InterventionStep string

const (
	StepAccepted         InterventionStep = "accepted"
	StepEnRoute          InterventionStep = "en_route"
	StepVisitDone        InterventionStep = "visit_done"
	StepRepairAuthorized InterventionStep = "repair_authorized"
	// StepRepairExecuted is the provider reporting the intervention
	// finished: only then can the case be closed.
	StepRepairExecuted InterventionStep = "repair_executed"
)

// InterventionSteps are the steps in the order they are recorded.
var InterventionSteps = []InterventionStep{StepAccepted, StepEnRoute, StepVisitDone, StepRepairAuthorized,
	StepRepairExecuted}

// KnownStep tells whether s is a step of the intervention.
func KnownStep(s InterventionStep) bool {
	for _, k := range InterventionSteps {
		if k == s {
			return true
		}
	}
	return false
}

// Limits of what the analyst writes.
const (
	MaxInterventionNote = 1000
	MinReportText       = 3
	MaxReportText       = 300
	MaxRepairDays       = 365
	MaxReportPhotos     = 50
	// stepClockSkew is how far ahead of the clock of the service an instant
	// the analyst types is still accepted: the clocks of the browser and the
	// server are never exactly the same.
	stepClockSkew = 5 * time.Minute
)

// ProviderReport is what the provider found at the visit (O8): the verified
// cause, the damage, what the repair costs and takes, and how many photos it
// sent. The photos stay with the provider; only how many is recorded.
type ProviderReport struct {
	Cause           string
	Damage          string
	RepairCostMinor int64
	RepairDays      int
	Photos          int
}

// StepRecord is a step recorded: when the provider says it happened, what it
// reported, and who recorded it and when (criteria 1 and 2).
type StepRecord struct {
	ClaimID    string
	Step       InterventionStep
	OccurredAt time.Time
	Note       string
	// Report is the report of the visit; nil for any other step.
	Report     *ProviderReport
	RecordedBy Analyst
	RecordedAt time.Time
}

// ClosingResult is how the case ended. It says what happened on site: it
// never approves, rejects nor pays the claim (criterion 5).
type ClosingResult string

const (
	ResultRepaired          ClosingResult = "repaired"
	ResultPartiallyRepaired ClosingResult = "partially_repaired"
	ResultNoRepairNeeded    ClosingResult = "no_repair_needed"
	ResultNotRepairable     ClosingResult = "not_repairable"
)

// ClosingResults are the results a case is closed with.
var ClosingResults = []ClosingResult{ResultRepaired, ResultPartiallyRepaired, ResultNoRepairNeeded,
	ResultNotRepairable}

func knownResult(r ClosingResult) bool {
	for _, k := range ClosingResults {
		if k == r {
			return true
		}
	}
	return false
}

// Closure is the closing of the case (criteria 3 and 5): its result, when it
// was closed, the notes and the analyst responsible.
type Closure struct {
	ClaimID    string
	Result     ClosingResult
	ClosedAt   time.Time
	Notes      string
	ClosedBy   Analyst
	RecordedAt time.Time
}

// StepRequest is a step as the analyst types it.
type StepRequest struct {
	Step       InterventionStep
	OccurredAt *time.Time
	Note       string
	Report     *ReportRequest
}

// ReportRequest is the report of the visit as the analyst types it. The
// numbers are pointers so a missing one is told from zero.
type ReportRequest struct {
	Cause           string
	Damage          string
	RepairCostMinor *int64
	RepairDays      *int
	Photos          *int
}

// CloseRequest is the closing as the analyst types it.
type CloseRequest struct {
	Result   ClosingResult
	ClosedAt *time.Time
	Notes    string
}

// NextStep is the step the intervention records next; empty when the
// provider reported it finished.
func NextStep(steps []StepRecord) InterventionStep {
	if len(steps) >= len(InterventionSteps) {
		return ""
	}
	return InterventionSteps[len(steps)]
}

// MissingSteps are the steps still to record before the case can be closed.
func MissingSteps(steps []StepRecord) []InterventionStep {
	if len(steps) >= len(InterventionSteps) {
		return []InterventionStep{}
	}
	return append([]InterventionStep(nil), InterventionSteps[len(steps):]...)
}

// lastInstant is when the intervention last moved: the last step, or the
// assignment when it has none.
func lastInstant(a Assignment, steps []StepRecord) time.Time {
	if len(steps) == 0 {
		return a.AssignedAt
	}
	return steps[len(steps)-1].OccurredAt
}

// RecordStep is the analyst recording a progress the provider reported
// (criterion 1). steps are the steps of the intervention in order. The step
// has to be the next one, at an instant not before the last one nor in the
// future; the visit carries the report of the provider.
func RecordStep(a *Assignment, steps []StepRecord, closure *Closure, in StepRequest, by Analyst,
	now time.Time) (StepRecord, error) {
	switch {
	case a == nil:
		return StepRecord{}, ErrNotAssigned
	case closure != nil:
		return StepRecord{}, ErrCaseClosed
	case !KnownStep(in.Step):
		return StepRecord{}, &ValidationError{Fields: []FieldError{{Field: "step", Code: CodeInvalidFormat}}}
	case in.Step != NextStep(steps):
		return StepRecord{}, ErrStepOutOfOrder
	}

	var fields []FieldError
	bad := func(field, code string) { fields = append(fields, FieldError{Field: field, Code: code}) }
	instant(in.OccurredAt, "occurred_at", lastInstant(*a, steps), now, bad)
	note := strings.TrimSpace(in.Note)
	if len([]rune(note)) > MaxInterventionNote {
		bad("note", CodeOutOfRange)
	}
	var report *ProviderReport
	switch {
	case in.Step == StepVisitDone && in.Report == nil:
		bad("report", CodeRequired)
	case in.Step == StepVisitDone:
		report = reportOf(*in.Report, bad)
	case in.Report != nil:
		bad("report", CodeOutOfRange) // only the visit carries a report
	}
	if len(fields) > 0 {
		return StepRecord{}, &ValidationError{Fields: fields}
	}
	return StepRecord{ClaimID: a.ClaimID, Step: in.Step, OccurredAt: in.OccurredAt.UTC(), Note: note,
		Report: report, RecordedBy: by, RecordedAt: now.UTC()}, nil
}

// instant checks an instant the analyst typed: given, not before since and
// not in the future. The screen takes minutes, not seconds, so since counts
// from the start of its minute: a step typed at 10:40 is not before an
// assignment at 10:40:35.
func instant(t *time.Time, field string, since, now time.Time, bad func(field, code string)) {
	switch {
	case t == nil || t.IsZero():
		bad(field, CodeRequired)
	case t.Before(since.Truncate(time.Minute)), t.After(now.Add(stepClockSkew)):
		bad(field, CodeOutOfRange)
	}
}

func reportOf(in ReportRequest, bad func(field, code string)) *ProviderReport {
	out := &ProviderReport{Cause: strings.TrimSpace(in.Cause), Damage: strings.TrimSpace(in.Damage)}
	lengthOf("report.cause", out.Cause, MinReportText, MaxReportText, bad)
	lengthOf("report.damage", out.Damage, MinReportText, MaxReportText, bad)
	switch {
	case in.RepairCostMinor == nil:
		bad("report.repair_cost_minor", CodeRequired)
	case *in.RepairCostMinor < 0:
		bad("report.repair_cost_minor", CodeOutOfRange)
	default:
		out.RepairCostMinor = *in.RepairCostMinor
	}
	switch {
	case in.RepairDays == nil:
		bad("report.repair_days", CodeRequired)
	case *in.RepairDays < 0 || *in.RepairDays > MaxRepairDays:
		bad("report.repair_days", CodeOutOfRange)
	default:
		out.RepairDays = *in.RepairDays
	}
	switch {
	case in.Photos == nil:
	case *in.Photos < 0 || *in.Photos > MaxReportPhotos:
		bad("report.photos", CodeOutOfRange)
	default:
		out.Photos = *in.Photos
	}
	return out
}

// CloseCase is the analyst closing the case (criteria 3 to 5). The provider has
// to have reported the intervention finished; otherwise it is a
// *NotFinishedError naming the steps still missing. The result is mandatory
// and the closing is not before the end of the repair nor in the future.
func CloseCase(a *Assignment, steps []StepRecord, closure *Closure, in CloseRequest, by Analyst,
	now time.Time) (Closure, error) {
	switch {
	case a == nil:
		return Closure{}, ErrNotAssigned
	case closure != nil:
		return Closure{}, ErrCaseClosed
	case NextStep(steps) != "":
		return Closure{}, &NotFinishedError{Missing: MissingSteps(steps)}
	}

	var fields []FieldError
	bad := func(field, code string) { fields = append(fields, FieldError{Field: field, Code: code}) }
	switch {
	case in.Result == "":
		bad("result", CodeRequired)
	case !knownResult(in.Result):
		bad("result", CodeInvalidFormat)
	}
	instant(in.ClosedAt, "closed_at", lastInstant(*a, steps), now, bad)
	notes := strings.TrimSpace(in.Notes)
	if len([]rune(notes)) > MaxInterventionNote {
		bad("notes", CodeOutOfRange)
	}
	if len(fields) > 0 {
		return Closure{}, &ValidationError{Fields: fields}
	}
	return Closure{ClaimID: a.ClaimID, Result: in.Result, ClosedAt: in.ClosedAt.UTC(), Notes: notes, ClosedBy: by,
		RecordedAt: now.UTC()}, nil
}

// InterventionState is where the intervention of a claim stands.
type InterventionState string

const (
	// InterventionNotAssigned is a claim with no provider yet.
	InterventionNotAssigned InterventionState = "not_assigned"
	// InterventionInProgress is a provider that has not reported the
	// intervention finished.
	InterventionInProgress InterventionState = "in_progress"
	// InterventionReadyToClose is an intervention finished and a case still
	// open.
	InterventionReadyToClose InterventionState = "ready_to_close"
	// InterventionClosed is a case closed.
	InterventionClosed InterventionState = "closed"
)

// InterventionStateOf tells where the intervention stands.
func InterventionStateOf(a *Assignment, steps []StepRecord, closure *Closure) InterventionState {
	switch {
	case a == nil:
		return InterventionNotAssigned
	case closure != nil:
		return InterventionClosed
	case NextStep(steps) == "":
		return InterventionReadyToClose
	}
	return InterventionInProgress
}

// ReportOf is the report of the visit of the intervention; nil before it.
func ReportOf(steps []StepRecord) *ProviderReport {
	for _, s := range steps {
		if s.Step == StepVisitDone && s.Report != nil {
			r := *s.Report
			return &r
		}
	}
	return nil
}
