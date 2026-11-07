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

// MaxReturnReasonLength bounds the reason of a return, in characters.
const MaxReturnReasonLength = 1000

// The reasons a return is refused. They are the advisor's to correct.
var (
	ErrReturnReasonRequired = errors.New("claims: the reason of the return is required")
	ErrReturnReasonTooLong  = errors.New("claims: the reason of the return is too long")
	// ErrNothingRequested means the return names no missing support and no
	// fact to correct: the insured would not know what to do.
	ErrNothingRequested = errors.New("claims: the return asks for no support and no fact")
	// ErrSupportNotApplicable means a missing support is not one of the claim
	// type, or a fact is not one that can be corrected.
	ErrSupportNotApplicable = errors.New("claims: the support is not one of the claim type")
)

// Review is what the advisor decided on a filing, by whom and when. A claim
// sent again after a return is reviewed again, so Seq orders its reviews.
type Review struct {
	ClaimID      string
	FilingNumber string
	Seq          int
	Outcome      ReviewOutcome
	ReviewedBy   string
	ReviewedAt   time.Time
	// The return: why, and what the insured has to complete.
	Reason          string
	MissingSupports []string
	FactsToCorrect  []Fact
}

// StatusOf is the status of the claim that the review leaves it in.
func (r Review) StatusOf() Status {
	if r.Outcome == ReviewReturned {
		return StatusReturned
	}
	return StatusInEvaluation
}

// ApproveFiling is the advisor approving a filing waiting for review.
func ApproveFiling(c ClaimRecord, advisor string, at time.Time) (Review, error) {
	if !Reviewable(c.Status) {
		return Review{}, ErrNotReviewable
	}
	return Review{ClaimID: c.ID, FilingNumber: c.FilingNumber, Outcome: ReviewApproved,
		ReviewedBy: advisor, ReviewedAt: at.UTC()}, nil
}

// ReturnRequest is what the advisor asks the insured to complete.
type ReturnRequest struct {
	Reason          string
	MissingSupports []string
	FactsToCorrect  []Fact
}

// ReturnFiling is the advisor returning a filing waiting for review. The
// missing supports have to be supports of its claim type, so the insured can
// attach them; repeated ones count once.
func ReturnFiling(c ClaimRecord, advisor string, in ReturnRequest, at time.Time) (Review, error) {
	if !Reviewable(c.Status) {
		return Review{}, ErrNotReviewable
	}
	reason := strings.TrimSpace(in.Reason)
	switch {
	case reason == "":
		return Review{}, ErrReturnReasonRequired
	case utf8.RuneCountInString(reason) > MaxReturnReasonLength:
		return Review{}, ErrReturnReasonTooLong
	}
	option, _ := CatalogFor(c.CatalogVersion).Incident(c.IncidentType)
	supports := make([]string, 0, len(in.MissingSupports))
	for _, code := range in.MissingSupports {
		code = strings.TrimSpace(code)
		if _, ok := option.Requirement(code); !ok {
			return Review{}, ErrSupportNotApplicable
		}
		if !contains(supports, code) {
			supports = append(supports, code)
		}
	}
	facts := make([]Fact, 0, len(in.FactsToCorrect))
	for _, f := range in.FactsToCorrect {
		if !f.Valid() {
			return Review{}, ErrSupportNotApplicable
		}
		if !contains(facts, f) {
			facts = append(facts, f)
		}
	}
	if len(supports) == 0 && len(facts) == 0 {
		return Review{}, ErrNothingRequested
	}
	return Review{ClaimID: c.ID, FilingNumber: c.FilingNumber, Outcome: ReviewReturned,
		ReviewedBy: advisor, ReviewedAt: at.UTC(), Reason: reason,
		MissingSupports: supports, FactsToCorrect: facts}, nil
}

// ErrNotReviewable means the claim is not waiting for the review of the
// advisor: it was already reviewed, or it is in another stage.
var ErrNotReviewable = errors.New("claims: the claim is not waiting for review")

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
