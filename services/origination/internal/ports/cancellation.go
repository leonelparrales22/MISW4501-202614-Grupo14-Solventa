package ports

import (
	"context"
	"errors"
	"time"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/idempotency"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
)

// HU-W1.6-01: the cancellation of a policy at the request of its insured, with
// the refund of the part of the current month the coverage will not use.

// Errors of the cancellation at the request of the insured.
var (
	// ErrPolicyAlreadyCancelled is returned when the policy was cancelled
	// before: for non-payment, or by an earlier confirmation.
	ErrPolicyAlreadyCancelled = errors.New("the policy is already cancelled")
	// ErrPaymentInProgress is returned while the sweep is charging an
	// instalment of the policy (decision D-6): cancelling then could charge a
	// policy just cancelled. It lasts as long as the lease of the sweep.
	ErrPaymentInProgress = errors.New("an instalment of the policy is being charged")
	// ErrRenewalInProgress is returned when the current period of the policy
	// has a renewal accepted or done (decision D-6 a): cancelling it would
	// leave the renewal behind.
	ErrRenewalInProgress = errors.New("the renewal of the policy is in progress")
)

// CancellationFacts is what the store knows of a policy to quote and cancel
// it: the policy, what was paid of its premium, whether the sweep is charging
// it, its cancellation and the renewal of its current period.
type CancellationFacts struct {
	Policy domain.Policy
	// Issuance is the charge of the issuance (the first month); nil when it
	// is not known.
	Issuance *domain.Charge
	// Installments are the months billed, oldest first.
	Installments []domain.Installment
	// InstallmentHeld is true when a pending instalment is held by the sweep
	// at now: it is being charged.
	InstallmentHeld bool
	// Cancellation is the cancellation of the policy; nil when it has none.
	Cancellation *domain.PolicyCancellation
	// Renewal is the status of the renewal of the current period; empty
	// when it has none.
	Renewal domain.RenewalStatus
}

// CancellationDecision decides, from the facts of the policy read in the
// transaction of the cancellation, what to write; an error writes nothing and
// is returned as it is.
type CancellationDecision func(CancellationFacts) (domain.InsuredCancellation, error)

// CancellationStore keeps the cancellations at the request of the insured
// and their refunds, in origination's own database (migration 0020).
type CancellationStore interface {
	// Facts reads the facts of the policy at now, without holding anything:
	// what the quote of screen A5 needs. ErrPolicyNotFound for an unknown id.
	Facts(ctx context.Context, policyID string, now time.Time) (CancellationFacts, error)
	// Cancel cancels the policy in one transaction: it holds the months of
	// the policy, reads its facts again under that hold, asks decide what to
	// write, closes the pending months unpaid, stores the cancellation and
	// numbers and stores its refund. It returns the cancellation stored,
	// ErrPolicyNotFound, ErrPolicyAlreadyCancelled when another one won, or
	// the error of decide.
	Cancel(ctx context.Context, policyID string, now time.Time, decide CancellationDecision) (domain.PolicyCancellation, error)
}

// RefundStore keeps the processing of the refunds in process (decision D-5
// b): the idempotent mechanism of pkg/idempotency (EC-D02) over
// policy_refund, by policy id. Every method that records a result is
// conditional on the claim (idempotency.ErrClaimLost otherwise).
type RefundStore interface {
	idempotency.Store[domain.Refund]
	// ListDue returns up to limit policy ids whose refund is in process and
	// due at now, and that no worker holds.
	ListDue(ctx context.Context, now time.Time, limit int) ([]string, error)
}

// RefundGateway sends a refund to the payment gateway with its idempotency
// key and returns the gateway's id of the refund. Its errors wrap
// idempotency.ErrRejected (final), idempotency.ErrAmbiguous (try again with the
// same key) or idempotency.ErrNotSent.
type RefundGateway interface {
	Refund(ctx context.Context, r domain.Refund) (providerReference string, err error)
}
