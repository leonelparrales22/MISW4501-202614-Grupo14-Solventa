package domain

import (
	"errors"
	"strings"
	"time"
	"unicode/utf8"
)

// StatusReturned is a filing the advisor returned to the insured to complete
// (HU-W2.1-03). It keeps its filing number: the insured corrects it and sends
// it again (HU-W2.1-04), which makes it active for a new review.
const StatusReturned Status = "returned"

// Reviewable tells whether the advisor can review a claim in the status: only
// a filing waiting for review, which is what active means since HU-W2.1-03.
func Reviewable(s Status) bool { return s == StatusActive }

// ReviewOutcome is what the advisor decided on a filing.
type ReviewOutcome string

const (
	// ReviewApproved: the filing is complete and enters evaluation.
	ReviewApproved ReviewOutcome = "approved"
	// ReviewReturned: the filing goes back to the insured with what is
	// missing.
	ReviewReturned ReviewOutcome = "returned"
)

// Verdict is what the advisor says of one support of the filing (screen S8).
type Verdict string

const (
	// VerdictValid: the support is attached and serves.
	VerdictValid Verdict = "valid"
	// VerdictIllegible: the support is attached but does not serve; the
	// insured replaces it (screen A9).
	VerdictIllegible Verdict = "illegible"
	// VerdictMissing: the support is not attached: a required one, or an
	// optional one the advisor asks for.
	VerdictMissing Verdict = "missing"
)

// ReasonCode is why a filing is returned, from a closed list (screen S8).
type ReasonCode string

const (
	ReasonIllegibleOrIncompleteSupports ReasonCode = "illegible_or_incomplete_supports"
	ReasonInconsistentFacts             ReasonCode = "inconsistent_facts"
	ReasonMissingInformation            ReasonCode = "missing_information"
	ReasonOther                         ReasonCode = "other"
)

// Valid reports whether r is one of the reasons of the list.
func (r ReasonCode) Valid() bool {
	switch r {
	case ReasonIllegibleOrIncompleteSupports, ReasonInconsistentFacts, ReasonMissingInformation, ReasonOther:
		return true
	}
	return false
}

// Fact is a fact of the filing the advisor can ask the insured to correct.
type Fact string

const (
	FactOccurredAt      Fact = "occurred_at"
	FactPlace           Fact = "place"
	FactDescription     Fact = "description"
	FactEstimatedAmount Fact = "estimated_amount"
)

// Valid reports whether f is one of the facts that can be corrected.
func (f Fact) Valid() bool {
	switch f {
	case FactOccurredAt, FactPlace, FactDescription, FactEstimatedAmount:
		return true
	}
	return false
}

// Bounds of a return, in characters.
const (
	MaxReturnMessageLength = 1000
	MaxVerdictNoteLength   = 300
)

// The reasons a review is refused. They are the advisor's to correct.
var (
	// ErrNotReviewable means the claim is not waiting for the review of the
	// advisor: it was already reviewed, or it is in another stage.
	ErrNotReviewable = errors.New("claims: the claim is not waiting for review")
	// ErrSupportsIncomplete means a required support is missing: the filing
	// cannot be approved, only returned.
	ErrSupportsIncomplete = errors.New("claims: a required support of the filing is missing")
	ErrReasonCodeInvalid  = errors.New("claims: the reason of the return is not one of the list")
	ErrMessageRequired    = errors.New("claims: the message to the insured is required")
	ErrMessageTooLong     = errors.New("claims: the message to the insured is too long")
	ErrVerdictNoteTooLong = errors.New("claims: the note of a support is too long")
	// ErrNothingRequested means the return marks no support as illegible or
	// missing and asks for no fact: the insured would not know what to do.
	ErrNothingRequested = errors.New("claims: the return asks for no support and no fact")
	// ErrSupportNotApplicable means a support is not one of the claim type,
	// or appears twice, or a fact is not one that can be corrected.
	ErrSupportNotApplicable = errors.New("claims: the support is not one of the claim type")
	// ErrVerdictNotApplicable means the verdict does not fit the support: an
	// attached support is valid or illegible, one that is not attached can
	// only be missing.
	ErrVerdictNotApplicable = errors.New("claims: the verdict does not fit the support")
)

// ChecklistItem is one support of the claim type and how many files the
// filing has for it: the rows the advisor gives a verdict on (screen S8).
type ChecklistItem struct {
	RequirementCode string
	Label           string
	Required        bool
	Files           int
}

// Checklist is what the advisor reviews of a filing, in the order of the
// catalog. A filing of the phone has no supports: its evidence (4 to 6
// photographs) is the "Fotos del vehículo", the only one it requires, and the
// rest of its claim type can be asked for.
func Checklist(c ClaimRecord) []ChecklistItem {
	option, _ := CatalogFor(c.CatalogVersion).Incident(c.IncidentType)
	var out []ChecklistItem
	if c.Channel != ChannelWeb {
		out = append(out, ChecklistItem{RequirementCode: SupportVehiclePhotos, Label: "Fotos del vehículo", Required: true,
			Files: len(c.Evidence)})
		for _, r := range option.Requirements {
			if r.Code != SupportVehiclePhotos {
				out = append(out, ChecklistItem{RequirementCode: r.Code, Label: r.Label})
			}
		}
		return out
	}
	for _, r := range option.Requirements {
		item := ChecklistItem{RequirementCode: r.Code, Label: r.Label, Required: r.Required}
		for _, a := range c.Supports {
			if a.RequirementCode == r.Code {
				item.Files++
			}
		}
		out = append(out, item)
	}
	return out
}

// SupportAssessment is the verdict of the advisor on one support.
type SupportAssessment struct {
	RequirementCode string
	Label           string
	Verdict         Verdict
	Note            string
}

// Reviewer is the advisor who reviews: who they are in the user pool and the
// name the insured reads (screen A9).
type Reviewer struct {
	ID   string
	Name string
}

// Review is what the advisor decided on a filing, by whom and when. A claim
// sent again after a return is reviewed again, so Seq orders its reviews.
type Review struct {
	ClaimID      string
	FilingNumber string
	Seq          int
	Outcome      ReviewOutcome
	ReviewedBy   string
	ReviewerName string
	ReviewedAt   time.Time
	// Supports is the verdict on every support of the checklist that is
	// attached, or missing and required or asked for.
	Supports []SupportAssessment
	// The return: why, what the insured reads and the facts to correct.
	ReasonCode     ReasonCode
	Message        string
	FactsToCorrect []Fact
}

// StatusOf is the status of the claim that the review leaves it in.
func (r Review) StatusOf() Status {
	if r.Outcome == ReviewReturned {
		return StatusReturned
	}
	return StatusInEvaluation
}

// ApproveFiling is the advisor approving a filing waiting for review: every
// attached support is valid. A filing with a required support missing is not
// approved, it is returned.
func ApproveFiling(c ClaimRecord, by Reviewer, at time.Time) (Review, error) {
	if !Reviewable(c.Status) {
		return Review{}, ErrNotReviewable
	}
	r := Review{ClaimID: c.ID, FilingNumber: c.FilingNumber, Outcome: ReviewApproved,
		ReviewedBy: by.ID, ReviewerName: by.Name, ReviewedAt: at.UTC()}
	for _, item := range Checklist(c) {
		switch {
		case item.Files > 0:
			r.Supports = append(r.Supports, SupportAssessment{RequirementCode: item.RequirementCode, Label: item.Label,
				Verdict: VerdictValid})
		case item.Required:
			return Review{}, ErrSupportsIncomplete
		}
	}
	return r, nil
}

// SupportVerdict is the verdict the advisor gives one support.
type SupportVerdict struct {
	RequirementCode string
	Verdict         Verdict
	Note            string
}

// ReturnRequest is why the advisor returns a filing and what the insured has
// to complete.
type ReturnRequest struct {
	ReasonCode     ReasonCode
	Message        string
	Supports       []SupportVerdict
	FactsToCorrect []Fact
}

// ReturnFiling is the advisor returning a filing waiting for review. The
// advisor gives a verdict on the supports that do not serve; an attached
// support the advisor does not mention is valid, and a required one that is
// not attached is missing.
func ReturnFiling(c ClaimRecord, by Reviewer, in ReturnRequest, at time.Time) (Review, error) {
	if !Reviewable(c.Status) {
		return Review{}, ErrNotReviewable
	}
	if !in.ReasonCode.Valid() {
		return Review{}, ErrReasonCodeInvalid
	}
	message := strings.TrimSpace(in.Message)
	switch {
	case message == "":
		return Review{}, ErrMessageRequired
	case utf8.RuneCountInString(message) > MaxReturnMessageLength:
		return Review{}, ErrMessageTooLong
	}

	checklist := Checklist(c)
	given := make(map[string]SupportVerdict, len(in.Supports))
	for _, v := range in.Supports {
		item, ok := find(checklist, v.RequirementCode)
		if _, repeated := given[v.RequirementCode]; !ok || repeated {
			return Review{}, ErrSupportNotApplicable
		}
		attached := item.Files > 0
		if (attached && v.Verdict != VerdictValid && v.Verdict != VerdictIllegible) ||
			(!attached && v.Verdict != VerdictMissing) {
			return Review{}, ErrVerdictNotApplicable
		}
		if utf8.RuneCountInString(strings.TrimSpace(v.Note)) > MaxVerdictNoteLength {
			return Review{}, ErrVerdictNoteTooLong
		}
		given[v.RequirementCode] = v
	}
	facts := make([]Fact, 0, len(in.FactsToCorrect))
	for _, f := range in.FactsToCorrect {
		if !f.Valid() || contains(facts, f) {
			return Review{}, ErrSupportNotApplicable
		}
		facts = append(facts, f)
	}

	r := Review{ClaimID: c.ID, FilingNumber: c.FilingNumber, Outcome: ReviewReturned,
		ReviewedBy: by.ID, ReviewerName: by.Name, ReviewedAt: at.UTC(),
		ReasonCode: in.ReasonCode, Message: message, FactsToCorrect: facts}
	asked := len(facts) > 0
	for _, item := range checklist {
		a := SupportAssessment{RequirementCode: item.RequirementCode, Label: item.Label}
		v, ok := given[item.RequirementCode]
		switch {
		case ok:
			a.Verdict, a.Note = v.Verdict, strings.TrimSpace(v.Note)
		case item.Files > 0:
			a.Verdict = VerdictValid
		case item.Required:
			a.Verdict = VerdictMissing
		default:
			continue
		}
		asked = asked || a.Verdict != VerdictValid
		r.Supports = append(r.Supports, a)
	}
	if !asked {
		return Review{}, ErrNothingRequested
	}
	return r, nil
}

func find(list []ChecklistItem, code string) (ChecklistItem, bool) {
	for _, item := range list {
		if item.RequirementCode == code {
			return item, true
		}
	}
	return ChecklistItem{}, false
}

func contains[T comparable](list []T, v T) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// StatusChange is one change of status of a claim, as the database recorded
// it. PreviousStatus is empty on the filing.
type StatusChange struct {
	ID             int64
	ClaimID        string
	Status         Status
	PreviousStatus Status
	ChangedAt      time.Time
}
