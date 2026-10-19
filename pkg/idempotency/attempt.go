package idempotency

import (
	"context"
	"errors"
	"time"
)

// Status is the state of an idempotent attempt.
type Status string

const (
	// StatusPending: waiting for its next attempt (or the first one).
	StatusPending Status = "pending"
	// StatusInProgress: claimed by one worker, which is calling the provider.
	StatusInProgress Status = "in_progress"
	// StatusConfirmed: the provider confirmed the operation. Terminal.
	StatusConfirmed Status = "confirmed"
	// StatusFailed: rejected by the provider or out of retry window. Terminal
	// until someone resumes it.
	StatusFailed Status = "failed"
)

// FailureReason tells why an attempt failed.
type FailureReason string

const (
	// ReasonRejected: the provider answered and rejected the operation.
	ReasonRejected FailureReason = "rejected"
	// ReasonWindowExpired: the retry window ended without a confirmation.
	ReasonWindowExpired FailureReason = "retry_window_expired"
)

// Errors that classify the result of a call to the provider. The provider
// client wraps one of them; Runner decides with errors.Is.
var (
	// ErrRejected: the provider answered and rejected the operation. It is
	// final: retrying would get the same answer.
	ErrRejected = errors.New("idempotency: the provider rejected the operation")
	// ErrAmbiguous: the outcome is unknown (timeout, connection cut, 5xx). The
	// provider may have done it, so the retry reuses the same key.
	ErrAmbiguous = errors.New("idempotency: the outcome of the call is unknown")
	// ErrNotSent: the call never left (for example, an open circuit breaker).
	// It is retried like an ambiguous one.
	ErrNotSent = errors.New("idempotency: the call was not sent")
)

// ErrClaimLost is returned by a Store when the attempt is no longer held by
// the claim that is trying to record its result: its lease expired and
// another worker claimed it, or the attempt already finished.
var ErrClaimLost = errors.New("idempotency: the attempt is no longer held by this claim")

// Attempt is the state Runner needs from a stored attempt. A store keeps more
// (the data of the operation); it exposes this view through Record.
type Attempt struct {
	// ID identifies the operation, e.g. the acceptance id of a premium charge.
	ID string
	// Key is the idempotency key sent to the provider on every attempt.
	Key    string
	Status Status
	// FailedAttempts counts the failed attempts in the current retry window.
	FailedAttempts int
	// WindowEndsAt bounds the retries; the zero time means no window.
	WindowEndsAt time.Time
	// NextAttemptAt is when a pending attempt is due.
	NextAttemptAt time.Time
	// ClaimedUntil is when the lease of the current claim expires.
	ClaimedUntil time.Time
}

// Record is a stored attempt with the data of its operation.
type Record interface {
	AttemptState() Attempt
}

// Outcome is what the provider returned for a confirmed operation.
type Outcome struct {
	// Reference is the provider's identifier of the operation (a charge id).
	Reference string
}

// Store persists the attempts of one kind of operation. Each service
// implements it over its own database. Every method that records a result is
// conditional on the claim still being held: it returns ErrClaimLost when it
// is not, and changes nothing.
type Store[T Record] interface {
	// Claim moves the attempt with the given id to in_progress, with a lease
	// that ends at now+lease, if it is pending and due at now, or in_progress
	// with an expired lease. It must be one atomic conditional statement.
	// claimed is false, with no error, when the attempt is not claimable (held
	// by another worker, not due, finished or unknown).
	Claim(ctx context.Context, id string, now time.Time, lease time.Duration) (rec T, claimed bool, err error)
	// Confirm records the confirmation and returns the stored record.
	Confirm(ctx context.Context, rec T, now time.Time, out Outcome) (T, error)
	// Reschedule counts one more failed attempt, keeps the key and makes the
	// attempt pending again, due at next.
	Reschedule(ctx context.Context, rec T, now, next time.Time, cause string) (T, error)
	// Fail counts one more failed attempt and marks it failed for the reason.
	Fail(ctx context.Context, rec T, now time.Time, reason FailureReason, cause string) (T, error)
}
