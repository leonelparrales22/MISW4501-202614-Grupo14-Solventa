package ports

import (
	"context"
	"errors"
	"time"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
)

// ErrReviewRequestNotFound is returned by a ReviewRepository that has no
// request for the requested acceptance.
var ErrReviewRequestNotFound = errors.New("review request not found")

// ErrReviewAlreadyDecided is returned when a request that is not pending is
// decided again (or two advisors decide it at once and the other one won).
var ErrReviewAlreadyDecided = errors.New("review request already decided")

// ReviewRepository is the advisor inbox of HU-W1.1-02: the requests that
// underwriting routed to a human decision (ReviewQueue) and the append-only
// record of the advisor decisions. No operation modifies or deletes a
// recorded decision.
type ReviewRepository interface {
	ReviewQueue

	// List returns the cases that match the filter ordered by arrival, and
	// the counts per status for the filter reason (the status is ignored).
	List(ctx context.Context, f domain.ReviewFilter) ([]domain.ReviewCase, domain.ReviewCounts, error)
	// Get returns ErrReviewRequestNotFound when the acceptance has no request.
	Get(ctx context.Context, acceptanceID string) (domain.ReviewCase, error)
	// RecordDecision appends the decision and marks the case decided in one
	// step, only if the case is pending (an atomic claim, as in EC-D02);
	// otherwise it returns ErrReviewAlreadyDecided. It returns
	// ErrReviewRequestNotFound when the acceptance has no request.
	RecordDecision(ctx context.Context, d domain.HumanDecision) (domain.ReviewCase, error)
	// DecisionForAdjustedOffer returns the decision that created the adjusted
	// offer, or found=false when no decision created it.
	DecisionForAdjustedOffer(ctx context.Context, offerID string) (d domain.HumanDecision, found bool, err error)

	// ListDueNotifications returns up to limit cases whose customer notice is
	// pending and due at now, oldest first.
	ListDueNotifications(ctx context.Context, now time.Time, limit int) ([]domain.ReviewCase, error)
	// UpdateNotification replaces the customer notice of a case. It returns
	// ErrReviewRequestNotFound when the acceptance has no request.
	UpdateNotification(ctx context.Context, acceptanceID string, n domain.ReviewNotification) error
}

// OfferWriter stores a new offer (the adjusted offer of an advisor decision).
// Create is idempotent by offer id. HU-W1.2-03 will implement it with the
// persisted offers of the rating engine.
type OfferWriter interface {
	Create(ctx context.Context, offer domain.Offer) error
}

// AdjustedOfferStore keeps the offers an advisor adjusts (HU-W1.1-02, decision
// D-1 a of the fix C5): Create stores the offer of a recorded adjust decision,
// idempotent by id and never overwriting it, and Get reads it back
// (ErrOfferNotFound for an unknown id). An adjusted offer is immutable.
type AdjustedOfferStore interface {
	OfferRepository
	OfferWriter
}

// ReviewOutcomeNotifier tells the customer that their request was adjusted or
// rejected by an advisor (decision D-3 c). It is idempotent by the notice key.
type ReviewOutcomeNotifier interface {
	NotifyReviewOutcome(ctx context.Context, n domain.ReviewOutcomeNotice) error
}
