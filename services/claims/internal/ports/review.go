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
