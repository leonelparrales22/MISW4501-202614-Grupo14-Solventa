package ports

import (
	"context"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
)

// ReviewStore keeps the reviews of the advisor (HU-W2.1-03). Recording one
// moves the status of the claim in the same statement, so a claim never shows
// a status its last review does not have.
type ReviewStore interface {
	// Review records the review of a filing waiting for review and moves the
	// claim to the status of the review; it assigns the Seq. It returns
	// domain.ErrNotReviewable when the claim is not waiting for review any
	// more, ErrClaimNotFound when there is no such claim.
	Review(ctx context.Context, r domain.Review) (domain.Review, error)
	// Reviews returns the reviews of a claim, oldest first.
	Reviews(ctx context.Context, claimID string) ([]domain.Review, error)
}

// StatusHistory reads the changes of status of the claims, which the
// database records as they happen (HU-W2.1-03).
type StatusHistory interface {
	// History returns the changes of a claim, oldest first.
	History(ctx context.Context, claimID string) ([]domain.StatusChange, error)
}

// ResubmissionStore keeps the resubmissions of returned filings (HU-W2.1-04).
// Recording one corrects the facts, attaches the new supports, withdraws the
// replaced ones and makes the claim active again, all or nothing.
type ResubmissionStore interface {
	// Resubmit records the resubmission, conditionally on the claim being
	// returned by the review it answers; it assigns the Seq. It returns
	// domain.ErrNotReturned when the claim is not returned any more,
	// ErrClaimNotFound when there is no such claim and
	// ErrSupportAlreadyAttached when a new support is attached to a claim.
	Resubmit(ctx context.Context, r domain.Resubmission) (domain.Resubmission, error)
	// Resubmissions returns the resubmissions of a claim, oldest first.
	Resubmissions(ctx context.Context, claimID string) ([]domain.Resubmission, error)
}
