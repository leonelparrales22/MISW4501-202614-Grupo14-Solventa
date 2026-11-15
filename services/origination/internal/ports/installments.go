package ports

import (
	"context"
	"errors"
	"time"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
)

// HU-W1.6-02: the monthly instalments of the premium and the cancellation of
// a policy for non-payment.

// ErrInstallmentClaimLost is returned when an instalment is written by a task
// that no longer holds it: its lease ended and another task took it, or it
// stopped being pending (the policy was cancelled).
var ErrInstallmentClaimLost = errors.New("the instalment is no longer held by this task")

// InstallmentStore keeps the instalments of the policies origination issued,
// the attempts to charge them and the cancellations for non-payment, in
// origination's own database.
type InstallmentStore interface {
	// BillingStart is when the instalments started to be billed: a month that
	// fell due before it is not billed.
	BillingStart(ctx context.Context) (time.Time, error)
	// ListBillable returns the policies with months to bill at now: their
	// coverage started and has not ended, they are not cancelled and no
	// month of theirs is pending. A policy in its grace period is not billed
	// a new month until the one due is paid (or the policy is cancelled).
	ListBillable(ctx context.Context, now time.Time) ([]domain.Policy, error)
	// CreateInstallment stores the instalment unless the policy already has
	// that month; created=false then.
	CreateInstallment(ctx context.Context, i domain.Installment) (created bool, err error)

	// ClaimDueAttempts holds, until now+lease, up to limit pending
	// instalments whose next attempt is due at now and that no other task
	// holds, and returns them with their policy.
	ClaimDueAttempts(ctx context.Context, now time.Time, lease time.Duration, limit int) ([]PolicyInstallment, error)
	// RecordApproved records the approved attempt and the instalment as paid.
	RecordApproved(ctx context.Context, i domain.Installment, a domain.InstallmentAttempt) error
	// RecordDeclined records the declined attempt, counts it and schedules
	// the next one at next (zero when there is none left), releasing the
	// instalment.
	RecordDeclined(ctx context.Context, i domain.Installment, a domain.InstallmentAttempt, next time.Time) error
	// RescheduleUnknown releases an instalment whose attempt had no known
	// outcome, to be tried again at next with the same key. It is not counted.
	RescheduleUnknown(ctx context.Context, i domain.Installment, next time.Time) error

	// ListOverdue returns up to limit pending instalments that no task holds
	// and whose grace period ended at now, with their policy.
	ListOverdue(ctx context.Context, now time.Time, limit int) ([]PolicyInstallment, error)
	// Cancel cancels the policy for the unpaid instalment, in one
	// transaction with its PolicyCancelledForNonPayment event: the instalment
	// becomes unpaid and the cancellation is stored. created=false when the
	// instalment was paid, held or the policy cancelled in the meantime.
	Cancel(ctx context.Context, p domain.Policy, i domain.Installment, now time.Time) (created bool, err error)

	// ListUnnotified returns up to limit cancellations whose e-mail to the
	// insured has not gone out and was tried fewer than maxAttempts times,
	// oldest first.
	ListUnnotified(ctx context.Context, maxAttempts, limit int) ([]CancellationNotice, error)
	// MarkEmailed records that the e-mail of the cancellation went out.
	MarkEmailed(ctx context.Context, policyID string, at time.Time) error
	// CountEmailFailure counts a failed e-mail of the cancellation.
	CountEmailFailure(ctx context.Context, policyID string) error

	// Billing reads the instalments, with their attempts, and the
	// cancellation of each policy of the ids. A policy with neither has no
	// entry.
	Billing(ctx context.Context, policyIDs []string) (map[string]BillingRecord, error)
}

// PolicyInstallment is an instalment with its policy.
type PolicyInstallment struct {
	Policy      domain.Policy
	Installment domain.Installment
}

// CancellationNotice is a cancellation with what the e-mail to the insured
// says.
type CancellationNotice struct {
	Policy       domain.Policy
	Cancellation domain.PolicyCancellation
}

// BillingRecord is what the store keeps of the premium of a policy: its
// instalments, oldest first, and its cancellation (nil when it has none).
type BillingRecord struct {
	Installments []domain.Installment
	Cancellation *domain.PolicyCancellation
}

// CancellationChecker tells whether a policy was cancelled: a cancelled
// policy cannot be renewed.
type CancellationChecker interface {
	IsCancelled(ctx context.Context, policyID string) (bool, error)
}

// PaymentMethodReader reads the payment method the insured paid the issuance
// with, which the instalments are charged to.
type PaymentMethodReader interface {
	// PaymentMethodOf returns the payment method of the charge of the
	// acceptance; an empty method when it has none.
	PaymentMethodOf(ctx context.Context, acceptanceID string) (domain.PaymentMethod, error)
}

// InstallmentGateway charges an instalment with the idempotency key of the
// attempt. Its errors wrap idempotency.ErrRejected (declined: the attempt is
// spent), idempotency.ErrAmbiguous (unknown outcome: try again with the same
// key) or idempotency.ErrNotSent (never sent).
type InstallmentGateway interface {
	ChargeInstallment(ctx context.Context, p domain.Policy, i domain.Installment, key string,
		m domain.PaymentMethod) (providerReference string, err error)
}

// CancellationNotifier tells the insured by e-mail that their policy was
// cancelled for non-payment, with no refund. The push is the notifications
// service's, from the event.
type CancellationNotifier interface {
	NotifyCancellation(ctx context.Context, n CancellationNotice) error
}
