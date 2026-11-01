package ports

import (
	"context"
	"errors"
	"time"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/idempotency"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
)

// ErrNotDecidable means the claim is not in evaluation: it was already
// decided, or it is in a status that cannot be decided.
var ErrNotDecidable = errors.New("claims: the claim is not in evaluation")

// ErrDecisionNotFound means the claim has not been decided.
var ErrDecisionNotFound = errors.New("claims: the claim has not been decided")

// ErrPayoutNotRetryable means the claim is not a payment exception: its
// payout is in course, already paid, or there is none.
var ErrPayoutNotRetryable = errors.New("claims: the payout of the claim is not a payment exception")

// DecisionStore keeps the decisions of the claims and their payouts
// (HU-W2.2-03). Every write moves the status of the claim in the same
// statement, so a claim never shows a status its decision does not have. The
// payout is an idempotency.Store: only the worker that claims it calls the
// gateway, and every result is recorded only while that claim is held.
type DecisionStore interface {
	idempotency.Store[domain.Decision]

	// Decide records the decision of a claim in evaluation and moves it to
	// the status of the decision. ErrNotDecidable when the claim is not in
	// evaluation any more, ErrClaimNotFound when there is no such claim.
	Decide(ctx context.Context, d domain.Decision) (domain.Decision, error)
	// Decision returns the decision of a claim, or ErrDecisionNotFound.
	Decision(ctx context.Context, claimID string) (domain.Decision, error)
	// ListPayoutsDue returns the claims whose payout is due at now: pending
	// and due, or held by a claim whose lease expired.
	ListPayoutsDue(ctx context.Context, now time.Time, limit int) ([]string, error)
	// RecordGatewayCode keeps the gateway's reason of a rejected payout.
	RecordGatewayCode(ctx context.Context, claimID, code string) error
	// RetryPayout makes a failed payout due again with no attempts counted,
	// the destination corrected when r carries one, and moves the claim back
	// to approved. The key does not change. ErrPayoutNotRetryable when it is
	// not failed.
	RetryPayout(ctx context.Context, claimID string, r domain.PayoutRetry) (domain.Decision, error)
	// ListNoticesDue returns the rejections whose notice is due at now.
	ListNoticesDue(ctx context.Context, now time.Time, limit int) ([]domain.Decision, error)
	// NoticeSent records that the notice of the rejection was delivered.
	NoticeSent(ctx context.Context, claimID string, at time.Time) error
	// NoticeFailed counts a failed delivery and makes it due again at next.
	NoticeFailed(ctx context.Context, claimID string, at, next time.Time, cause string) error
}

// PayoutGateway pays the indemnity of an approval. It returns the gateway's
// reference of the payout and an error classified for pkg/idempotency.
type PayoutGateway interface {
	Payout(ctx context.Context, d domain.Decision) (reference string, err error)
}

// Mailer delivers an e-mail once per key.
type Mailer interface {
	Send(ctx context.Context, to, subject, body, key string) error
}
