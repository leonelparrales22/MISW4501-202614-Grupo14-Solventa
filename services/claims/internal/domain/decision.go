package domain

import (
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/idempotency"
)

// The statuses a claim reaches when operations decides it (HU-W2.2-03). None
// of them is triageable, so a decided claim leaves the evaluation and the
// triage stops recalculating it.
const (
	// StatusApproved is an approved claim whose indemnity is being paid.
	StatusApproved Status = "approved"
	// StatusPaid is an approved claim whose payout the gateway confirmed.
	StatusPaid Status = "paid"
	// StatusRejected is a claim operations rejected with a reason.
	StatusRejected Status = "rejected"
	// StatusPaymentException is an approved claim whose payout failed: the
	// gateway rejected it or the automatic attempts ran out. The approval is
	// kept, and the analyst corrects the destination and retries.
	StatusPaymentException Status = "payment_exception"
)

// Decidable tells whether a claim in the status can be approved or rejected:
// only a claim in evaluation, and only once.
func Decidable(s Status) bool { return Triageable(s) }

// DecidableStatuses returns the statuses a claim can be decided in, in a new
// slice.
func DecidableStatuses() []Status { return TriageableStatuses() }

// Outcome is what operations decided.
type Outcome string

const (
	OutcomeApproved Outcome = "approved"
	OutcomeRejected Outcome = "rejected"
)

// MaxRejectionReasonLength bounds the reason of a rejection, in characters.
const MaxRejectionReasonLength = 1000

// MaxAutomaticPayoutAttempts is how many times the indemnity is sent to the
// gateway before the claim becomes a payment exception (screen O6b).
const MaxAutomaticPayoutAttempts = 3

// basisPoints is 100 %.
const basisPoints = 10_000

// The reasons a decision is refused. They are the analyst's to correct.
var (
	ErrAmountOutOfRange          = errors.New("claims: the approved amount must be positive")
	ErrAmountExceedsInsuredValue = errors.New("claims: the approved amount exceeds the insured value of the coverage")
	ErrNothingToPay              = errors.New("claims: after the deductible there is nothing to pay")
	ErrNoPayoutAccount           = errors.New("claims: the insured has no account to pay the indemnity to")
	ErrReasonRequired            = errors.New("claims: the reason of the rejection is required")
	ErrReasonTooLong             = errors.New("claims: the reason of the rejection is too long")
	ErrNoContact                 = errors.New("claims: the insured has no e-mail to notify the rejection to")
)

// DeductibleOf is the part of the approved amount that is not paid, rounded
// to the nearest cent: 5 % of $ 4.350.000 is $ 217.500.
func DeductibleOf(approvedMinor int64, deductibleBasisPoints int) int64 {
	return (approvedMinor*int64(deductibleBasisPoints) + basisPoints/2) / basisPoints
}

// PayoutFailure tells why a payout became a payment exception.
type PayoutFailure string

const (
	// PayoutRejected: the gateway answered and rejected it (an invalid
	// destination account, for one).
	PayoutRejected PayoutFailure = "rejected"
	// PayoutRetriesExhausted: the gateway did not confirm it in
	// MaxAutomaticPayoutAttempts attempts.
	PayoutRetriesExhausted PayoutFailure = "retries_exhausted"
)

// PayoutFailureFor maps the reason of the shared runner to the payout's.
func PayoutFailureFor(r idempotency.FailureReason) PayoutFailure {
	if r == idempotency.ReasonRejected {
		return PayoutRejected
	}
	return PayoutRetriesExhausted
}

// Payout is the disbursement of an approval: one per approval, under one
// idempotency key that never changes, however many times it is retried
// (HA-D02). The key is also the payment reference operations quotes.
type Payout struct {
	Key           string
	BeneficiaryID string
	AmountMinor   int64
	Currency      string
	Destination   PayoutAccount

	Status        idempotency.Status
	Attempts      int
	NextAttemptAt time.Time
	ClaimedUntil  time.Time
	LastAttemptAt time.Time
	LastFailureAt time.Time
	Failure       PayoutFailure
	LastError     string
	// GatewayCode is the gateway's reason for a rejection, such as
	// invalid_destination_account (screen O6b).
	GatewayCode string
	// Reference is the gateway's id of the payout made.
	Reference   string
	ConfirmedAt time.Time

	// The manual retries of operations.
	ManualRetries int
	LastRetriedBy string
	LastRetriedAt time.Time
}

// Notice is the e-mail that tells the insured the claim was rejected.
type Notice struct {
	To            string
	SentAt        time.Time
	Attempts      int
	NextAttemptAt time.Time
	LastError     string
}

// Decision is what operations decided on a claim, by whom and when
// (criterion 5). A claim has one decision.
type Decision struct {
	ClaimID      string
	FilingNumber string
	Outcome      Outcome
	DecidedBy    string
	DecidedAt    time.Time

	// The approval: the amount the analyst approved, the terms of the
	// coverage it was checked against and the payout, which is the approved
	// amount less the deductible.
	ApprovedAmountMinor   int64
	InsuredValueMinor     int64
	DeductibleBasisPoints int
	Currency              string
	Payout                *Payout

	// The rejection and its notice to the insured.
	Reason string
	Notice *Notice
}

// DeductibleMinor is the deductible discounted from the approved amount.
func (d Decision) DeductibleMinor() int64 {
	return DeductibleOf(d.ApprovedAmountMinor, d.DeductibleBasisPoints)
}

// AttemptState is the payout as the shared runner sees it (pkg/idempotency).
// The window is the number of attempts, not a time: it is zero.
func (d Decision) AttemptState() idempotency.Attempt {
	p := d.Payout
	if p == nil {
		return idempotency.Attempt{ID: d.ClaimID}
	}
	return idempotency.Attempt{
		ID: d.ClaimID, Key: p.Key, Status: p.Status, FailedAttempts: p.Attempts,
		NextAttemptAt: p.NextAttemptAt, ClaimedUntil: p.ClaimedUntil,
	}
}

// PayoutKey is the idempotency key of the indemnity of a claim. It depends
// only on the claim, which has one approval, so the claim is paid once.
func PayoutKey(claimID string) string { return "indemnizacion-" + claimID }

// NoticeKey is the idempotency key of the notice of a rejection.
func NoticeKey(claimID string) string { return "rechazo-" + claimID }

// Approve builds the approval of a claim in evaluation for approvedMinor,
// checked against the coverage of its incident (criterion 2): the amount is
// positive, it does not exceed the insured value, and what is left after the
// deductible is paid to the account of the insured, due at once.
func Approve(c ClaimRecord, cov Coverage, holder Holder, approvedMinor int64, by string, at time.Time) (Decision, error) {
	switch {
	case approvedMinor <= 0:
		return Decision{}, ErrAmountOutOfRange
	case approvedMinor > cov.InsuredValueMinor:
		return Decision{}, ErrAmountExceedsInsuredValue
	}
	payout := approvedMinor - DeductibleOf(approvedMinor, cov.DeductibleBasisPoints)
	if payout <= 0 {
		return Decision{}, ErrNothingToPay
	}
	account := holder.PayoutAccount
	if strings.TrimSpace(account.BankCode) == "" || strings.TrimSpace(account.AccountNumber) == "" {
		return Decision{}, ErrNoPayoutAccount
	}
	at = at.UTC()
	return Decision{
		ClaimID: c.ID, FilingNumber: c.FilingNumber, Outcome: OutcomeApproved, DecidedBy: by, DecidedAt: at,
		ApprovedAmountMinor: approvedMinor, InsuredValueMinor: cov.InsuredValueMinor,
		DeductibleBasisPoints: cov.DeductibleBasisPoints, Currency: cov.Currency,
		Payout: &Payout{
			Key: PayoutKey(c.ID), BeneficiaryID: c.Subject, AmountMinor: payout, Currency: cov.Currency,
			Destination: account, Status: idempotency.StatusPending, NextAttemptAt: at,
		},
	}, nil
}

// Reject builds the rejection of a claim in evaluation with its reason
// (criterion 3), and the notice to the insured, due at once.
func Reject(c ClaimRecord, reason, notifyTo, by string, at time.Time) (Decision, error) {
	reason, err := RejectionReason(reason)
	if err != nil {
		return Decision{}, err
	}
	if strings.TrimSpace(notifyTo) == "" {
		return Decision{}, ErrNoContact
	}
	at = at.UTC()
	return Decision{
		ClaimID: c.ID, FilingNumber: c.FilingNumber, Outcome: OutcomeRejected, DecidedBy: by, DecidedAt: at,
		Reason: reason, Notice: &Notice{To: strings.TrimSpace(notifyTo), NextAttemptAt: at},
	}, nil
}

// RejectionReason returns the reason of a rejection without surrounding
// blanks, or why it is not valid: it is required and up to
// MaxRejectionReasonLength characters.
func RejectionReason(reason string) (string, error) {
	reason = strings.TrimSpace(reason)
	switch {
	case reason == "":
		return "", ErrReasonRequired
	case utf8.RuneCountInString(reason) > MaxRejectionReasonLength:
		return "", ErrReasonTooLong
	}
	return reason, nil
}

// StatusOf is the status of the claim that the decision leaves it in.
func (d Decision) StatusOf() Status {
	if d.Outcome == OutcomeRejected {
		return StatusRejected
	}
	switch d.Payout.Status {
	case idempotency.StatusConfirmed:
		return StatusPaid
	case idempotency.StatusFailed:
		return StatusPaymentException
	}
	return StatusApproved
}

// PayoutRetry is a manual retry of a failed payout by operations, with the
// destination corrected when the analyst changed it.
type PayoutRetry struct {
	By          string
	At          time.Time
	Destination *PayoutAccount
}
