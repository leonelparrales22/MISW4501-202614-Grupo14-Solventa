package idempotency

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/resilience"
)

// CallOutcome classifies the result of one call to the provider.
type CallOutcome string

const (
	OutcomeConfirmed CallOutcome = "confirmed"
	OutcomeRejected  CallOutcome = "rejected"
	OutcomeAmbiguous CallOutcome = "ambiguous"
	OutcomeNotSent   CallOutcome = "not_sent"
)

// Classify returns the outcome a call error stands for: nil is confirmed,
// ErrRejected rejected, ErrNotSent not sent, and anything else ambiguous (an
// unknown error never ends the operation: it is retried with the same key).
func Classify(err error) CallOutcome {
	switch {
	case err == nil:
		return OutcomeConfirmed
	case errors.Is(err, ErrRejected):
		return OutcomeRejected
	case errors.Is(err, ErrNotSent):
		return OutcomeNotSent
	default:
		return OutcomeAmbiguous
	}
}

// Exec calls the provider for the claimed attempt, sending rec's idempotency
// key. Its context ends when the lease of the claim does.
type Exec[T Record] func(ctx context.Context, rec T) (Outcome, error)

// RunnerConfig configures a Runner.
type RunnerConfig struct {
	// Policy is the growing wait between attempts; the retry window itself is
	// the WindowEndsAt of each attempt, set by the store when it creates or
	// resumes the attempt (Policy.WindowEnd helps to compute it).
	Policy resilience.RetryPolicy
	// Lease is how long a claim holds the attempt. It must be longer than the
	// timeout of one call to the provider: a call never outlives its lease.
	Lease time.Duration
	// Now is the clock; nil uses time.Now.
	Now func() time.Time
}

// Report tells what one Process call did.
type Report[T Record] struct {
	// Claimed is false when another worker held the attempt or it was not
	// due: then nothing was called and nothing changed.
	Claimed bool
	// Outcome classifies the call; empty when not claimed.
	Outcome CallOutcome
	// Status and Reason are the state recorded after the call (Reason only
	// for StatusFailed).
	Status Status
	Reason FailureReason
	// Record is the attempt as stored after the call, or as claimed when
	// recording the result failed.
	Record T
	// CallErr is the provider error; nil when confirmed.
	CallErr error
}

// Runner processes idempotent attempts (see the package documentation).
type Runner[T Record] struct {
	store  Store[T]
	policy resilience.RetryPolicy
	lease  time.Duration
	now    func() time.Time
}

// NewRunner builds a Runner.
func NewRunner[T Record](store Store[T], cfg RunnerConfig) (*Runner[T], error) {
	if store == nil {
		return nil, errors.New("idempotency: a store is required")
	}
	if err := cfg.Policy.Validate(); err != nil {
		return nil, err
	}
	if cfg.Lease <= 0 {
		return nil, errors.New("idempotency: the claim lease must be positive")
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	return &Runner[T]{store: store, policy: cfg.Policy, lease: cfg.Lease, now: now}, nil
}

// Process claims the attempt with the given id and, only if this caller won
// the claim, runs exec and records its result: confirmed on success, failed
// on an explicit rejection, and otherwise rescheduled with the same key and a
// growing wait, or failed with ReasonWindowExpired once the window is over.
// The error is about the store; a provider failure is reported in Report.
func (r *Runner[T]) Process(ctx context.Context, id string, exec Exec[T]) (Report[T], error) {
	var rep Report[T]
	rec, claimed, err := r.store.Claim(ctx, id, r.now().UTC(), r.lease)
	if err != nil {
		return rep, fmt.Errorf("idempotency: could not claim attempt %s: %w", id, err)
	}
	if !claimed {
		return rep, nil
	}
	rep.Claimed, rep.Record = true, rec

	cctx, cancel := context.WithTimeout(ctx, r.lease)
	out, callErr := exec(cctx, rec)
	cancel()

	rep.Outcome, rep.CallErr = Classify(callErr), callErr
	now := r.now().UTC()
	var stored T
	switch rep.Outcome {
	case OutcomeConfirmed:
		rep.Status = StatusConfirmed
		stored, err = r.store.Confirm(ctx, rec, now, out)
	case OutcomeRejected:
		rep.Status, rep.Reason = StatusFailed, ReasonRejected
		stored, err = r.store.Fail(ctx, rec, now, ReasonRejected, callErr.Error())
	default:
		st := rec.AttemptState()
		next, exhausted := r.policy.Next(st.FailedAttempts+1, st.WindowEndsAt, now)
		if exhausted {
			rep.Status, rep.Reason = StatusFailed, ReasonWindowExpired
			stored, err = r.store.Fail(ctx, rec, now, ReasonWindowExpired, callErr.Error())
		} else {
			rep.Status = StatusPending
			stored, err = r.store.Reschedule(ctx, rec, now, next, callErr.Error())
		}
	}
	if err != nil {
		rep.Status, rep.Reason = StatusInProgress, ""
		return rep, fmt.Errorf("idempotency: could not record the %s result of attempt %s: %w", rep.Outcome, id, err)
	}
	rep.Record = stored
	return rep, nil
}
