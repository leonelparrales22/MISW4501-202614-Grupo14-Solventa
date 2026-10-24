package domain

import "time"

// Offer is the priced offer a customer can accept. HU-W1.2-03 will generate
// and persist it with the rating engine; HU-W1.3-01 only reads it.
type Offer struct {
	ID         string
	CustomerID string
	Applicant  Applicant
	Premium    float64
	Currency   string
	// Mode is provisional when the offer was priced in degraded mode (HA-D01).
	Mode         OfferMode
	ProfileScore float64
	ExpiresAt    time.Time
	// Terms are the coverage terms the offer activates once it is paid. Only
	// a personalized offer carries them (HU-W1.2-03, priced by the rating
	// engine); the demo and adjusted offers leave them empty and their charge
	// activates the demonstration terms (HA-D02, decision D-16).
	Terms CoverageTerms
}

// IsExpired tells whether the offer validity is over at the given time: an
// expired offer can no longer be accepted (HU-W1.3-01 CA-4).
func (o Offer) IsExpired(now time.Time) bool {
	return !now.Before(o.ExpiresAt)
}

// Applicant is the customer data the advisor needs to review a request
// routed to human decision (HU-W1.3-01 CA-3).
type Applicant struct {
	FullName   string
	DocumentID string
	Email      string
}

// Channel is where the customer accepted the offer.
type Channel string

const (
	ChannelWeb    Channel = "web"
	ChannelMobile Channel = "mobile"
)

// ParseChannel returns the channel named by s, or false if s names none.
func ParseChannel(s string) (Channel, bool) {
	switch c := Channel(s); c {
	case ChannelWeb, ChannelMobile:
		return c, true
	default:
		return "", false
	}
}

// AcceptanceStatus is the state of an offer acceptance (HU-W1.3-01).
type AcceptanceStatus string

const (
	// AcceptanceRegistered means the acceptance is persisted and underwriting
	// has not finished yet; it is retried until it does (CA-1, CA-5).
	AcceptanceRegistered AcceptanceStatus = "registered"
	// AcceptanceIssuancePending means automatic underwriting accepted the risk
	// and the issuance flow started (CA-2).
	AcceptanceIssuancePending AcceptanceStatus = "issuance_pending"
	// AcceptanceInReview means underwriting routed the request to a human
	// decision in the advisor inbox (CA-3).
	AcceptanceInReview AcceptanceStatus = "in_review"
)

// UnderwritingOutcome is the decision of the automatic underwriting.
type UnderwritingOutcome string

const (
	UnderwritingApproved    UnderwritingOutcome = "approved"
	UnderwritingHumanReview UnderwritingOutcome = "human_review"
)

// Reasons why underwriting routes a request to a human decision.
const (
	ReviewReasonProvisionalOffer = "provisional_offer"
	ReviewReasonHighRiskScore    = "high_risk_score"
)

// UnderwritingDecision is the outcome of the automatic underwriting for one
// acceptance. Reason is set only for UnderwritingHumanReview.
type UnderwritingDecision struct {
	Outcome   UnderwritingOutcome
	Reason    string
	DecidedAt time.Time
}

// Acceptance is the customer's acceptance of an offer: the no-loss point of
// the origination journey. It is persisted before any later step runs.
type Acceptance struct {
	ID         string
	OfferID    string
	CustomerID string
	Channel    Channel
	AcceptedAt time.Time
	Status     AcceptanceStatus
	// Decision is set once underwriting decided, so a resumed acceptance
	// never runs underwriting twice.
	Decision *UnderwritingDecision
	// Review is set once an advisor decided a request routed to a human
	// decision (HU-W1.1-02); nil otherwise.
	Review *AcceptanceReview
	// Attempts counts the failed processing attempts.
	Attempts      int
	LastFailureAt time.Time
	// NextAttemptAt is when the acceptance is processed next. It is the zero
	// time once the acceptance left AcceptanceRegistered.
	NextAttemptAt time.Time
	UpdatedAt     time.Time
}

// ReviewRequest is what the advisor inbox (HU-W1.1-02) receives when
// underwriting routes an acceptance to a human decision (CA-3).
//
// It keeps a snapshot of the routed offer (decision D-7 a of the fix C5): its
// premium and currency, its mode, profile score, validity and coverage terms,
// taken by the acceptance processor from the offer it already read. The inbox
// opens and decides a case from the snapshot and never reads the routed offer
// again, so a case is complete by itself: it is opened and decided the same
// way after a restart, whatever kind of offer was routed. The snapshot is
// exact, because an offer with an acceptance is never replaced and offers are
// immutable.
type ReviewRequest struct {
	AcceptanceID string
	OfferID      string
	CustomerID   string
	Applicant    Applicant
	Premium      float64
	Currency     string
	Reason       string
	ReceivedAt   time.Time

	// OfferMode, ProfileScore, OfferExpiresAt and Terms are the snapshot of
	// the routed offer. Terms are empty when the routed offer carries none.
	OfferMode      OfferMode
	ProfileScore   float64
	OfferExpiresAt time.Time
	Terms          CoverageTerms
}
