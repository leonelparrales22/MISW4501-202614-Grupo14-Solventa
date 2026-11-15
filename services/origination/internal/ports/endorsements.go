package ports

import (
	"context"
	"errors"
	"time"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
)

// Errors of the review of the change requests (HU-W1.4-02).
var (
	// ErrRequestAlreadyDecided is returned when the request is no longer in a
	// status the action needs: another analyst decided it first.
	ErrRequestAlreadyDecided = errors.New("the change request was already decided")
	// ErrPolicyChanged is returned when another endorsement of the same
	// policy was applied first (decision D-3 a): nothing changed, and the
	// next attempt is decided against the new terms.
	ErrPolicyChanged = errors.New("another endorsement of the policy was applied first")
	// ErrNotReturnable is returned for a request that is not a not applied
	// one waiting to be returned to the insured.
	ErrNotReturnable = errors.New("the change request cannot be returned")
)

// AppliedEndorsement is what applying a request stores in one transaction: the
// request claimed as applied, the attempt with its validations, the
// endorsement with the version that follows BaseVersion and the e-mail to the
// insured.
type AppliedEndorsement struct {
	Request domain.ChangeRequest
	// BaseVersion is the version of the terms the validations ran against.
	BaseVersion     int
	Terms           domain.EndorsedTerms
	PreviousPremium float64
	Validations     domain.Validations
	By              domain.Actor
	At              time.Time
}

// PolicyEndorsements counts the endorsements of a policy and names the last.
type PolicyEndorsements struct {
	Count int
	Last  *domain.Endorsement
}

// EndorsementStore keeps what the analyst does on the change requests
// (HU-W1.4-02): the endorsements, the attempts and the actions only receive
// rows, and of a request it only changes the status and the decision reason.
// Every decision claims the request with a conditional update (EC-D02), so of
// two analysts deciding at once exactly one wins.
type EndorsementStore interface {
	// Inbox returns the page of the requests that pass the filter, the latest
	// filed first, with the holder of their policy, the total and the
	// indicators (whatever the filter).
	Inbox(ctx context.Context, f domain.EndorsementInboxFilter) (domain.EndorsementInboxPage, error)
	// LatestTerms returns the terms of the last endorsement that governs the
	// policy: its own or, for a renewal without its own, the one of the
	// policy it renews.
	LatestTerms(ctx context.Context, policyID string) (domain.LatestTerms, error)
	// Record returns what was done on a request.
	Record(ctx context.Context, requestID string) (domain.RequestRecord, error)
	// Endorsements counts the endorsements of a policy.
	Endorsements(ctx context.Context, policyID string) (PolicyEndorsements, error)
	// StartReview moves a filed request to in review and records who took
	// it. It reports false, and changes nothing, when it was already in
	// review; ErrRequestAlreadyDecided for any other status.
	StartReview(ctx context.Context, requestID string, by domain.Actor, at time.Time) (bool, error)
	// Apply stores the endorsement. ErrRequestAlreadyDecided when the request
	// is no longer filed nor in review, ErrPolicyChanged when another
	// endorsement of the policy took the version first; in both cases
	// nothing is stored.
	Apply(ctx context.Context, a AppliedEndorsement) (domain.Endorsement, error)
	// RecordNotApplied moves the request to not applied and records the
	// attempt. ErrRequestAlreadyDecided when it is no longer filed nor in
	// review.
	RecordNotApplied(ctx context.Context, a domain.EndorsementAttempt) (domain.EndorsementAttempt, error)
	// Reject rejects a filed or in review request with the observations the
	// insured reads. ErrRequestAlreadyDecided otherwise.
	Reject(ctx context.Context, requestID, observations string, by domain.Actor, at time.Time) error
	// Return returns a not applied request to the insured with the
	// observations they read. ErrNotReturnable otherwise.
	Return(ctx context.Context, requestID, observations string, by domain.Actor, at time.Time) error
	// History returns the endorsements and the attempts not applied of a
	// policy, the newest first.
	History(ctx context.Context, policyID string) ([]domain.EndorsementHistoryEntry, error)
}

// EndorsementNoticeStore keeps the e-mails of the decisions (decision D-12 a).
type EndorsementNoticeStore interface {
	// ListDueNotices returns up to limit pending e-mails due at now.
	ListDueNotices(ctx context.Context, now time.Time, limit int) ([]domain.EndorsementNotice, error)
	// UpdateNotice records where the e-mail of a request stands.
	UpdateNotice(ctx context.Context, requestID string, d domain.NoticeDelivery) error
}

// IdentityDocumentChecker asks the registry whether the identity document of a
// beneficiary is valid (decision D-5 a). Any failure of the provider is
// DocumentUnverifiable.
type IdentityDocumentChecker interface {
	CheckDocument(ctx context.Context, b domain.Beneficiary) domain.DocumentStatus
}

// OpenClaimsChecker counts the claims in progress of a policy in claims
// (decision D-6 a). Any failure is an error: the validation is then
// unverifiable.
type OpenClaimsChecker interface {
	OpenClaims(ctx context.Context, policyID string) (int, error)
}

// EndorsementOutcomeNotifier tells the insured the decision on their request
// by e-mail, with its idempotency key.
type EndorsementOutcomeNotifier interface {
	NotifyEndorsementOutcome(ctx context.Context, n domain.EndorsementNotice) error
}
