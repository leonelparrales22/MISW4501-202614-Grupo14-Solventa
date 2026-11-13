package domain

import (
	"errors"
	"sort"
	"strconv"
	"strings"
	"time"
)

// HU-W2.1-04: the insured corrects a filing the advisor returned and sends it
// again. The claim keeps its id and its filing number, the facts the insured
// changes replace the ones filed, the supports the insured adds are attached
// and the ones replaced are withdrawn, and the claim is active again for a new
// review of the advisor.

// CodeNotReplaced is a support the advisor found illegible that the
// resubmission does not replace with a new file.
const CodeNotReplaced = "not_replaced"

// The reasons a resubmission is refused besides its fields.
var (
	// ErrNotReturned means the claim is not returned to the insured: it was
	// never returned, or it was already sent again.
	ErrNotReturned = errors.New("claims: the claim is not returned to the insured")
	// ErrStaleReturn means the resubmission answers a return that is not the
	// last one of the claim: the insured corrected an old version.
	ErrStaleReturn = errors.New("claims: the resubmission does not answer the last return")
)

// ResubmissionRequest is what the insured sends to correct a returned filing
// (screen A9). A nil fact keeps the value filed. A support group is the whole
// set of files the requirement has after the correction: the ids it keeps of
// the claim and the new ones uploaded; a requirement that is not sent keeps
// its files.
type ResubmissionRequest struct {
	// ReviewSeq is the review that returned the claim, the one this
	// resubmission answers.
	ReviewSeq            int
	OccurredAt           *string
	Place                *string
	Description          *string
	EstimatedAmountMinor *string
	Supports             []SupportGroup
}

// FactChange is one fact the resubmission changed, as text: RFC 3339 for the
// time of the incident, cents for the amount, empty for no value.
type FactChange struct {
	Fact   Fact
	Before string
	After  string
}

// SupportChange is what the resubmission changed of one requirement: the ids
// of the files it added and of the ones it withdrew.
type SupportChange struct {
	RequirementCode string
	Label           string
	Added           []string
	Withdrawn       []string
}

// Resubmission is the insured sending a returned filing again: who, when,
// which return it answers and what changed. Seq orders the resubmissions of a
// claim.
type Resubmission struct {
	ClaimID       string
	FilingNumber  string
	Seq           int
	ReviewSeq     int
	ResubmittedBy string
	ResubmittedAt time.Time
	Facts         []FactChange
	Supports      []SupportChange

	// Claim is the claim as it is after the resubmission, active, and Added
	// the supports to attach, for the store; they are not part of the record.
	Claim ClaimRecord
	Added []AttachedSupport
}

// Resubmit applies the correction of the insured to a returned claim and
// checks the result as the advisor will review it: every fact valid, every
// required support of the claim type with its files, every support the advisor
// found illegible replaced and every one missing uploaded. It returns a
// *ValidationError naming everything still missing at once.
//
// last is the return the claim is answering; uploaded holds the new supports
// the request names that exist, whoever owns them.
func Resubmit(c ClaimRecord, last Review, subject string, in ResubmissionRequest, uploaded map[string]SupportFile,
	now time.Time) (Resubmission, error) {
	if c.Status != StatusReturned || last.Outcome != ReviewReturned {
		return Resubmission{}, ErrNotReturned
	}
	if in.ReviewSeq != last.Seq {
		return Resubmission{}, ErrStaleReturn
	}

	var fields []FieldError
	bad := func(field, code string) { fields = append(fields, FieldError{Field: field, Code: code}) }

	after := copyClaimRecord(c)
	facts := correctFacts(&after, c, in, now, bad)
	for _, f := range last.FactsToCorrect {
		if f == FactEstimatedAmount && after.EstimatedAmountMinor == nil {
			bad("estimated_amount_minor", CodeRequired)
		}
	}

	changes, added := correctSupports(&after, c, last, subject, in.Supports, uploaded, bad)

	if len(fields) > 0 {
		return Resubmission{}, &ValidationError{Fields: fields}
	}
	after.Status = StatusActive
	return Resubmission{
		ClaimID: c.ID, FilingNumber: c.FilingNumber, ReviewSeq: last.Seq,
		// The database keeps microseconds: a retry reads the same instant.
		ResubmittedBy: subject, ResubmittedAt: now.UTC().Truncate(time.Microsecond),
		Facts: facts, Supports: changes, Claim: after, Added: added,
	}, nil
}

// correctFacts applies the facts the request sends with the rules of the
// filing. The time of the incident is checked against the filing: it cannot
// be after it nor older than the filing allows, and it must fall in the
// validity of the policy copied at the filing, when there is one.
func correctFacts(after *ClaimRecord, c ClaimRecord, in ResubmissionRequest, now time.Time,
	bad func(field, code string)) []FactChange {
	var out []FactChange
	if in.OccurredAt != nil {
		if at, ok := correctedOccurrence(strings.TrimSpace(*in.OccurredAt), c, now, bad); ok && !at.Equal(c.OccurredAt) {
			after.OccurredAt = at
			out = append(out, FactChange{Fact: FactOccurredAt, Before: c.OccurredAt.UTC().Format(time.RFC3339),
				After: at.Format(time.RFC3339)})
		}
	}
	if in.Place != nil {
		place := strings.TrimSpace(*in.Place)
		switch n := len([]rune(place)); {
		case place == "":
			bad("place", CodeRequired)
		case n < MinPlace, n > MaxPlace:
			bad("place", CodeOutOfRange)
		case place != c.Place:
			after.Place = place
			out = append(out, FactChange{Fact: FactPlace, Before: c.Place, After: place})
		}
	}
	if in.Description != nil {
		description := strings.TrimSpace(*in.Description)
		switch n := len([]rune(description)); {
		case description == "":
			bad("description", CodeRequired)
		case n < MinDescription, n > MaxDescription:
			bad("description", CodeOutOfRange)
		case description != c.Description:
			after.Description = description
			out = append(out, FactChange{Fact: FactDescription, Before: c.Description, After: description})
		}
	}
	if in.EstimatedAmountMinor != nil {
		value := strings.TrimSpace(*in.EstimatedAmountMinor)
		if value == "" {
			bad("estimated_amount_minor", CodeRequired)
		} else if amount := estimatedAmount(value, bad); amount != nil && !sameAmount(amount, c.EstimatedAmountMinor) {
			after.EstimatedAmountMinor = amount
			out = append(out, FactChange{Fact: FactEstimatedAmount, Before: amountText(c.EstimatedAmountMinor),
				After: amountText(amount)})
		}
	}
	return out
}

func correctedOccurrence(value string, c ClaimRecord, now time.Time, bad func(field, code string)) (time.Time, bool) {
	if value == "" {
		bad("occurred_at", CodeRequired)
		return time.Time{}, false
	}
	at, err := time.Parse(time.RFC3339, value)
	if err != nil {
		bad("occurred_at", CodeInvalidFormat)
		return time.Time{}, false
	}
	at = at.UTC()
	filed := c.FiledAt
	if filed.IsZero() || filed.After(now) {
		filed = now
	}
	if at.After(filed) || at.Before(filed.AddDate(0, 0, -MaxIncidentAgeDay)) ||
		(c.Policy != nil && !c.Policy.Within(at)) {
		bad("occurred_at", CodeOutOfRange)
		return time.Time{}, false
	}
	return at, true
}

// correctSupports applies the support groups of the request: a group is the
// whole set of files the requirement keeps. It checks the new files as the
// filing does, then the result as the advisor will review it.
func correctSupports(after *ClaimRecord, c ClaimRecord, last Review, subject string, groups []SupportGroup,
	uploaded map[string]SupportFile, bad func(field, code string)) ([]SupportChange, []AttachedSupport) {
	requirements := RequirementsOf(c)
	current := map[string][]AttachedSupport{}
	position := 0
	for _, a := range c.Supports {
		current[a.RequirementCode] = append(current[a.RequirementCode], a)
		if a.Position >= position {
			position = a.Position + 1
		}
	}

	if countIDs(groups) > MaxSupportsPerClaim {
		bad("supports", CodeTooMany)
		return nil, nil
	}

	seen := map[string]bool{}
	sent := map[string]bool{}
	// failed are the requirements already reported: one code each.
	failed := map[string]bool{}
	var changes []SupportChange
	var added []AttachedSupport
	for _, g := range groups {
		code := strings.TrimSpace(g.RequirementCode)
		field := "supports." + code
		req, asked := requirementIn(requirements, code)
		switch {
		case !requirementCodePattern.MatchString(code):
			bad("supports", CodeNotApplicable)
			continue
		case !asked || sent[code]:
			bad(field, CodeNotApplicable)
			continue
		}
		sent[code] = true

		kept := map[string]bool{}
		var newFiles []SupportFile
		problem := ""
		for _, id := range g.SupportIDs {
			id = strings.TrimSpace(id)
			if seen[id] {
				problem = CodeNotFound
				continue
			}
			seen[id] = true
			if isCurrent(current[code], id) {
				kept[id] = true
				continue
			}
			file, ok := uploaded[id]
			switch {
			case !ValidSupportID(id) || !ok || file.OwnerSubject != subject || !file.Confirmed():
				problem = CodeNotFound
			case !req.Accepts(file.Format) && problem == "":
				problem = CodeInvalidFormat
			}
			newFiles = append(newFiles, file)
		}
		if problem != "" {
			bad(field, problem)
			failed[code] = true
			continue
		}

		change := SupportChange{RequirementCode: code, Label: req.Label}
		var remaining []AttachedSupport
		for _, a := range current[code] {
			if kept[a.File.ID] {
				remaining = append(remaining, a)
			} else {
				change.Withdrawn = append(change.Withdrawn, a.File.ID)
			}
		}
		for _, f := range newFiles {
			a := AttachedSupport{RequirementCode: code, Position: position, File: f}
			position++
			remaining = append(remaining, a)
			added = append(added, a)
			change.Added = append(change.Added, f.ID)
		}
		current[code] = remaining
		if len(change.Added) > 0 || len(change.Withdrawn) > 0 {
			changes = append(changes, change)
		}
	}

	after.Supports = nil
	for _, req := range requirements {
		after.Supports = append(after.Supports, current[req.Code]...)
	}
	sort.SliceStable(after.Supports, func(i, j int) bool { return after.Supports[i].Position < after.Supports[j].Position })

	checkCorrectedSupports(*after, requirements, last, changes, sent, failed, bad)
	return changes, added
}

// checkCorrectedSupports checks the supports as the advisor will review them:
// the number of files of every requirement, the required ones of the
// checklist and what the last return asked for.
func checkCorrectedSupports(after ClaimRecord, requirements []Requirement, last Review, changes []SupportChange,
	sent, failed map[string]bool, bad func(field, code string)) {
	files := map[string]int{}
	for _, item := range Checklist(after) {
		files[item.RequirementCode] = item.Files
	}
	addedTo := map[string]bool{}
	for _, ch := range changes {
		addedTo[ch.RequirementCode] = len(ch.Added) > 0
	}
	verdicts := map[string]Verdict{}
	for _, a := range last.Supports {
		verdicts[a.RequirementCode] = a.Verdict
	}

	for _, item := range Checklist(after) {
		req, _ := requirementIn(requirements, item.RequirementCode)
		field := "supports." + item.RequirementCode
		n := files[item.RequirementCode]
		switch {
		case failed[item.RequirementCode]:
		case verdicts[item.RequirementCode] == VerdictIllegible && !addedTo[item.RequirementCode]:
			bad(field, CodeNotReplaced)
		case n == 0 && (item.Required || verdicts[item.RequirementCode] == VerdictMissing):
			bad(field, CodeMissing)
		case !sent[item.RequirementCode]:
			// Untouched files were checked when they were filed.
		case n > 0 && n < req.MinFiles:
			bad(field, CodeTooFew)
		case req.MaxFiles > 0 && n > req.MaxFiles:
			bad(field, CodeTooMany)
		}
	}
}

// RequirementsOf are the supports a claim can have, in the order of the
// catalog. A filing of the phone has its photographs of the vehicle as the
// one required support, whatever its claim type asks for (see Checklist).
func RequirementsOf(c ClaimRecord) []Requirement {
	// A parametric claim is resolved without supports (HU-M2.4-01).
	if c.Channel == ChannelParametric {
		return nil
	}
	option, _ := CatalogFor(c.CatalogVersion).Incident(c.IncidentType)
	if c.Channel == ChannelWeb {
		return append([]Requirement(nil), option.Requirements...)
	}
	out := []Requirement{{Code: SupportVehiclePhotos, Label: "Fotos del vehículo", Required: true,
		MinFiles: MinPhotos, MaxFiles: 10, Formats: []FileFormat{FormatJPEG, FormatPNG}}}
	for _, r := range option.Requirements {
		if r.Code != SupportVehiclePhotos {
			r.Required, r.MinFiles = false, 0
			out = append(out, r)
		}
	}
	return out
}

func requirementIn(list []Requirement, code string) (Requirement, bool) {
	for _, r := range list {
		if r.Code == code {
			return r, true
		}
	}
	return Requirement{}, false
}

func isCurrent(list []AttachedSupport, id string) bool {
	for _, a := range list {
		if a.File.ID == id {
			return true
		}
	}
	return false
}

func amountText(v *int64) string {
	if v == nil {
		return ""
	}
	return strconv.FormatInt(*v, 10)
}

// copyClaimRecord returns a record that shares no slice or map with c.
func copyClaimRecord(c ClaimRecord) ClaimRecord {
	details := make(map[string]string, len(c.Details))
	for k, v := range c.Details {
		details[k] = v
	}
	c.Details = details
	c.Evidence = append([]Evidence(nil), c.Evidence...)
	c.Supports = append([]AttachedSupport(nil), c.Supports...)
	if c.EstimatedAmountMinor != nil {
		amount := *c.EstimatedAmountMinor
		c.EstimatedAmountMinor = &amount
	}
	return c
}
