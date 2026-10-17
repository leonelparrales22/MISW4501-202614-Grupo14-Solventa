package domain

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
	"unicode/utf8"
)

// Acceptance states set by the advisor decision (HU-W1.1-02). Both are final.
const (
	// AcceptanceAdjusted means an advisor adjusted the premium: the customer
	// gets a new offer and accepts it through the same acceptance flow.
	AcceptanceAdjusted AcceptanceStatus = "adjusted"
	// AcceptanceRejected means an advisor did not accept the risk: the case is
	// closed and nothing is charged.
	AcceptanceRejected AcceptanceStatus = "rejected"
)

// ReviewStatus is the state of a request in the advisor inbox.
type ReviewStatus string

const (
	ReviewPending ReviewStatus = "pending"
	ReviewDecided ReviewStatus = "decided"
)

// ParseReviewStatus returns the status named by s, or false if s names none.
func ParseReviewStatus(s string) (ReviewStatus, bool) {
	switch r := ReviewStatus(s); r {
	case ReviewPending, ReviewDecided:
		return r, true
	default:
		return "", false
	}
}

// reviewReasons are the reasons underwriting routes a request to a human
// decision; the inbox filter accepts exactly these.
var reviewReasons = []string{ReviewReasonProvisionalOffer, ReviewReasonHighRiskScore}

// IsReviewReason tells whether s is one of the routing reasons of the domain.
func IsReviewReason(s string) bool {
	for _, r := range reviewReasons {
		if r == s {
			return true
		}
	}
	return false
}

// DecisionType is what the advisor decided about a routed request.
type DecisionType string

const (
	DecisionAccept DecisionType = "accept"
	DecisionAdjust DecisionType = "adjust"
	DecisionReject DecisionType = "reject"
)

// ParseDecisionType returns the decision named by s, or false if s names none.
func ParseDecisionType(s string) (DecisionType, bool) {
	switch d := DecisionType(s); d {
	case DecisionAccept, DecisionAdjust, DecisionReject:
		return d, true
	default:
		return "", false
	}
}

// Limits of a decision.
const (
	// MaxObservationsLength is the maximum length of the observations, in characters.
	MaxObservationsLength = 1000
	// MaxPremiumAdjustmentPct is the highest surcharge an advisor may apply
	// (decision D-4 a: only a surcharge, up to 100 %).
	MaxPremiumAdjustmentPct = 100
)

// Errors of a decision input.
var (
	// ErrInvalidDecision means the decision type is unknown or a field is malformed.
	ErrInvalidDecision = errors.New("invalid decision")
	// ErrObservationsRequired means an adjust or reject decision came without
	// observations (HU-W1.1-02 CA-2).
	ErrObservationsRequired = errors.New("observations are required to adjust or reject")
	// ErrInvalidPremiumAdjustment means the premium adjustment is missing when
	// adjusting, present otherwise, or out of range.
	ErrInvalidPremiumAdjustment = errors.New("invalid premium adjustment")
)

// DecisionInput is what the advisor submits to decide a routed request.
type DecisionInput struct {
	Type         DecisionType
	Observations string
	// PremiumAdjustmentPct is the surcharge in percent; set only to adjust.
	PremiumAdjustmentPct *float64
}

// Validate checks the input (CA-2): observations are required to adjust or
// reject and optional to accept, up to MaxObservationsLength characters; the
// premium adjustment is required only to adjust, greater than 0 and at most
// MaxPremiumAdjustmentPct, with up to two decimals.
func (in DecisionInput) Validate() error {
	if _, ok := ParseDecisionType(string(in.Type)); !ok {
		return fmt.Errorf("%w: type must be accept, adjust or reject", ErrInvalidDecision)
	}
	if utf8.RuneCountInString(in.Observations) > MaxObservationsLength {
		return fmt.Errorf("%w: observations must have at most %d characters", ErrInvalidDecision, MaxObservationsLength)
	}
	if in.Type != DecisionAccept && strings.TrimSpace(in.Observations) == "" {
		return ErrObservationsRequired
	}
	switch {
	case in.Type == DecisionAdjust && in.PremiumAdjustmentPct == nil:
		return fmt.Errorf("%w: premium_adjustment_pct is required to adjust", ErrInvalidPremiumAdjustment)
	case in.Type != DecisionAdjust && in.PremiumAdjustmentPct != nil:
		return fmt.Errorf("%w: premium_adjustment_pct is only allowed to adjust", ErrInvalidPremiumAdjustment)
	case in.Type == DecisionAdjust && !validAdjustment(*in.PremiumAdjustmentPct):
		return fmt.Errorf("%w: premium_adjustment_pct must be greater than 0 and at most %d, with up to two decimals",
			ErrInvalidPremiumAdjustment, MaxPremiumAdjustmentPct)
	}
	return nil
}

func validAdjustment(p float64) bool {
	if math.IsNaN(p) || math.IsInf(p, 0) || p <= 0 || p > MaxPremiumAdjustmentPct {
		return false
	}
	cents := p * 100
	return math.Abs(cents-math.Round(cents)) < 1e-6
}

// AdjustPremium applies a surcharge of pct percent to the premium, rounded to
// two decimals.
func AdjustPremium(premium, pct float64) float64 {
	return math.Round(premium*(1+pct/100)*100) / 100
}

// AdjustedOfferID is the id of the offer an adjust decision creates for an
// acceptance. It is deterministic, so retrying the decision never creates a
// second offer.
func AdjustedOfferID(acceptanceID string) string { return "adj-" + acceptanceID }

// Advisor is the advisor who decided, from the claims of their token.
type Advisor struct {
	// ID is the subject of the token ("sub").
	ID    string
	Email string
}

// AdjustedOffer is the new offer of an adjust decision. Its customer, applicant,
// currency, mode and score are those of the routed offer.
type AdjustedOffer struct {
	ID        string
	Premium   float64
	Currency  string
	ExpiresAt time.Time
}

// HumanDecision is the append-only record of an advisor decision (CA-4): who
// decided, when, what and why. It is never modified nor deleted.
type HumanDecision struct {
	ID           string
	AcceptanceID string
	Type         DecisionType
	Advisor      Advisor
	DecidedAt    time.Time
	Observations string
	// PremiumAdjustmentPct and AdjustedOffer are set only for DecisionAdjust.
	PremiumAdjustmentPct *float64
	AdjustedOffer        *AdjustedOffer
}

// ReviewNotification is the delivery state of the notice that tells the
// customer their request was adjusted or rejected (decision D-3 c). It reuses
// the statuses of the issuance notification (HU-W1.3-03).
type ReviewNotification struct {
	Channel string
	Status  NotificationStatus
	// Attempts counts the failed sends.
	Attempts int
	// NextAttemptAt is when the notice is sent next; the zero time once it is
	// sent or failed.
	NextAttemptAt time.Time
	SentAt        time.Time
	LastFailureAt time.Time
	LastFailure   string
}

// ReviewCase is a request in the advisor inbox: what underwriting routed, its
// state and, once decided, the decision and the customer notice.
type ReviewCase struct {
	Request  ReviewRequest
	Status   ReviewStatus
	Decision *HumanDecision
	// Notification is nil when no notice is due: an accept decision, or the
	// e-mail channel is not configured.
	Notification *ReviewNotification
}

// ReviewFilter selects the cases of the inbox (CA-5). An empty Reason means
// every reason; Descending lists the latest arrival first.
type ReviewFilter struct {
	Status     ReviewStatus
	Reason     string
	Descending bool
}

// ReviewCounts are the cases of the inbox per status, for the selected reason.
type ReviewCounts struct {
	Pending int
	Decided int
}

// AcceptanceReview is the advisor decision as the customer sees it in their
// acceptance: the outcome and, for an adjust, the new offer to accept.
type AcceptanceReview struct {
	Outcome       DecisionType
	DecidedAt     time.Time
	AdjustedOffer *AdjustedOffer
}

// ReviewOutcomeNotice tells the customer that their request was adjusted
// (with the new offer) or rejected. It carries no advisor observations: they
// are an internal record (CA-4).
type ReviewOutcomeNotice struct {
	To           string
	CustomerName string
	AcceptanceID string
	Outcome      DecisionType
	// AdjustedOffer is set only for DecisionAdjust.
	AdjustedOffer *AdjustedOffer
	DecidedAt     time.Time
	// IdempotencyKey lets the channel drop a notice sent twice.
	IdempotencyKey string
}

// ReviewOutcomeIdempotencyKey is the idempotency key of the notice of an
// acceptance: a case has a single decision, so it has a single notice.
func ReviewOutcomeIdempotencyKey(acceptanceID string) string { return "review-outcome:" + acceptanceID }
