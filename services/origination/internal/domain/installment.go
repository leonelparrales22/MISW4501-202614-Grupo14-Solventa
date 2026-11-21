package domain

import (
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/events"
)

// HU-W1.6-02: the monthly instalments of the premium and the cancellation of
// a policy for non-payment.
//
// The premium of a policy origination issues is monthly. The charge of the
// issuance (HA-D02) pays the first month; month n >= 2 is an instalment, due
// on the day of the month the coverage started (the last day of the month
// when it has no such day). When it falls due its grace period starts: the
// policy keeps its coverage, the instalment is charged on days 1, 8 and 15 of
// the grace with the card of the issuance, and if no charge goes through, the
// policy is cancelled on day 31 with no refund (art. 1068 of the Commercial
// Code: no late-payment interest, the arrears end the contract).

// InstallmentKeyPrefix starts the idempotency key of an attempt to charge an
// instalment. The gateway also reads it as the reference of the charge, which
// is how its test card for instalments knows what to decline.
const InstallmentKeyPrefix = "cuota"

// InstallmentStatus is where an instalment stands.
type InstallmentStatus string

const (
	InstallmentPending InstallmentStatus = "pending"
	InstallmentPaid    InstallmentStatus = "paid"
	// InstallmentUnpaid: the grace period ended without a payment and the
	// policy was cancelled.
	InstallmentUnpaid InstallmentStatus = "unpaid"
)

// InstallmentRules are the deadlines of the instalments. Day is how long a day
// lasts: 24 hours, except in a demonstration, where it is shortened so the
// whole grace period passes in minutes; then a month is 30 such days.
type InstallmentRules struct {
	// GraceDays is how long the policy keeps its coverage after an
	// instalment falls due unpaid. The policy is cancelled the day after.
	GraceDays int
	// AttemptDays are the days of the grace the instalment is charged on,
	// in order.
	AttemptDays []int
	Day         time.Duration
	// RetryUnknownAfter is when an attempt whose outcome is unknown is tried
	// again, with the same idempotency key.
	RetryUnknownAfter time.Duration
}

// DefaultInstallmentRules are the rules agreed for the story: 30 days of
// grace, charges on days 1, 8 and 15, cancellation on day 31.
var DefaultInstallmentRules = InstallmentRules{
	GraceDays: 30, AttemptDays: []int{1, 8, 15}, Day: 24 * time.Hour, RetryUnknownAfter: time.Hour,
}

// ErrInvalidInstallmentRules is returned for rules that cannot be applied.
var ErrInvalidInstallmentRules = errors.New("invalid instalment rules")

// Validate checks the rules.
func (r InstallmentRules) Validate() error {
	switch {
	case r.Day <= 0 || r.Day > 24*time.Hour:
		return fmt.Errorf("%w: a day lasts between 1 ns and 24 hours", ErrInvalidInstallmentRules)
	case r.GraceDays < 1 || r.GraceDays > 90:
		return fmt.Errorf("%w: the grace period is between 1 and 90 days", ErrInvalidInstallmentRules)
	case len(r.AttemptDays) == 0 || len(r.AttemptDays) > 10:
		return fmt.Errorf("%w: between 1 and 10 attempts", ErrInvalidInstallmentRules)
	case r.RetryUnknownAfter <= 0:
		return fmt.Errorf("%w: the retry of an unknown outcome must be positive", ErrInvalidInstallmentRules)
	}
	last := 0
	for _, d := range r.AttemptDays {
		if d <= last || d > r.GraceDays {
			return fmt.Errorf("%w: the attempts are increasing days within the grace period", ErrInvalidInstallmentRules)
		}
		last = d
	}
	return nil
}

// calendar tells whether a day lasts 24 hours, so a month is a calendar
// month and not 30 days.
func (r InstallmentRules) calendar() bool { return r.Day == 24*time.Hour }

// days is n days of the rules.
func (r InstallmentRules) days(n int) time.Duration { return time.Duration(n) * r.Day }

// DueAt is when month seq of the policy falls due: the coverage start plus
// seq-1 months, in Colombia, on the same day of the month (the last day of a
// shorter month) and at the same time. Month 1 falls due when the coverage
// starts.
func (r InstallmentRules) DueAt(p Policy, seq int) time.Time {
	if !r.calendar() {
		return p.CoverageStart.Add(r.days(30 * (seq - 1))).UTC()
	}
	start := p.CoverageStart.In(ColombiaTime)
	y, m, d := start.Date()
	first := time.Date(y, m+time.Month(seq-1), 1, start.Hour(), start.Minute(), start.Second(), start.Nanosecond(),
		ColombiaTime)
	if last := first.AddDate(0, 1, -1).Day(); d > last {
		d = last
	}
	return first.AddDate(0, 0, d-1).UTC()
}

// GraceEndsAt is the end of the grace period of an instalment due at due.
func (r InstallmentRules) GraceEndsAt(due time.Time) time.Time {
	return due.Add(r.days(r.GraceDays)).UTC()
}

// CancelsAt is when the policy is cancelled if the instalment due at due is
// still unpaid: the day after the grace period.
func (r InstallmentRules) CancelsAt(due time.Time) time.Time {
	return due.Add(r.days(r.GraceDays + 1)).UTC()
}

// AttemptAt is when attempt n (1-based) of an instalment due at due is made;
// false when there is no such attempt.
func (r InstallmentRules) AttemptAt(due time.Time, n int) (time.Time, bool) {
	if n < 1 || n > len(r.AttemptDays) {
		return time.Time{}, false
	}
	return due.Add(r.days(r.AttemptDays[n-1])).UTC(), true
}

// MaxAttempts is how many times an instalment is charged.
func (r InstallmentRules) MaxAttempts() int { return len(r.AttemptDays) }

// Months is how many monthly premiums the policy has: the months between the
// start and the end of its coverage, at least 1.
//
// With a demo day (shorter than 24 h) a month lasts 30 demo days but the
// coverage keeps its real end, so a policy has hundreds of thousands of
// months: they are counted with arithmetic, never one by one.
func (r InstallmentRules) Months(p Policy) int {
	if !r.calendar() {
		span, month := p.CoverageEnd.Sub(p.CoverageStart), r.days(30)
		if span <= month {
			return 1
		}
		return int((span + month - 1) / month)
	}
	n := 1
	for r.DueAt(p, n+1).Before(p.CoverageEnd) {
		n++
	}
	return n
}

// DueInstallments are the months of the policy, from 2, that fell due in
// [since, now]: the ones to bill now. A month due before since was never
// billed by this system and is not billed now.
func (r InstallmentRules) DueInstallments(p Policy, since, now time.Time) []int {
	var out []int
	months := r.Months(p)
	for seq := 2; seq <= months; seq++ {
		due := r.DueAt(p, seq)
		if due.After(now) {
			break
		}
		if !due.Before(since) {
			out = append(out, seq)
		}
	}
	return out
}

// NextDueAt is when the next month of the policy falls due after now; false
// when the coverage ends first.
func (r InstallmentRules) NextDueAt(p Policy, now time.Time) (time.Time, bool) {
	months := r.Months(p)
	for seq := 2; seq <= months; seq++ {
		if due := r.DueAt(p, seq); due.After(now) {
			return due, true
		}
	}
	return time.Time{}, false
}

// Installment is one month of the premium of a policy.
type Installment struct {
	PolicyID    string
	Seq         int
	DueAt       time.Time
	GraceEndsAt time.Time
	CancelsAt   time.Time
	AmountMinor int64
	Currency    string
	Status      InstallmentStatus
	// Attempts counts the attempts the gateway declined.
	Attempts int
	// NextAttemptAt is when it is charged next; zero once there is no attempt
	// left or it finished.
	NextAttemptAt     time.Time
	PaidAt            time.Time
	ProviderReference string
	CreatedAt         time.Time
	// History is every attempt the gateway answered, oldest first.
	History []InstallmentAttempt
}

// NewInstallment builds month seq of the policy, pending, with its first
// attempt scheduled.
func NewInstallment(p Policy, seq int, r InstallmentRules, now time.Time) Installment {
	due := r.DueAt(p, seq)
	first, _ := r.AttemptAt(due, 1)
	return Installment{
		PolicyID: p.ID, Seq: seq, DueAt: due, GraceEndsAt: r.GraceEndsAt(due), CancelsAt: r.CancelsAt(due),
		AmountMinor: ToMinorUnits(p.Premium), Currency: p.Currency, Status: InstallmentPending,
		NextAttemptAt: first, CreatedAt: now.UTC(),
	}
}

// IdempotencyKey is the key of the next attempt: one per attempt, because the
// gateway answers a key it already declined with the same decline.
func (i Installment) IdempotencyKey() string {
	return InstallmentKeyPrefix + "-" + i.PolicyID + "-" + strconv.Itoa(i.Seq) + "-" + strconv.Itoa(i.Attempts+1)
}

// AfterDecline is when the instalment is charged again after attempt
// i.Attempts+1 was declined; false when it was the last one.
func (i Installment) AfterDecline(r InstallmentRules) (time.Time, bool) {
	return r.AttemptAt(i.DueAt, i.Attempts+2)
}

// AttemptOutcome is what the gateway answered to an attempt.
type AttemptOutcome string

const (
	AttemptApproved AttemptOutcome = "approved"
	AttemptDeclined AttemptOutcome = "declined"
)

// InstallmentAttempt is one answer of the gateway to an instalment.
type InstallmentAttempt struct {
	Attempt           int
	IdempotencyKey    string
	Outcome           AttemptOutcome
	DeclineCode       string
	ProviderReference string
	AttemptedAt       time.Time
}

// CancellationReason is why a policy was cancelled.
type CancellationReason string

// CancellationNonPayment: an instalment was not paid within its grace period.
const CancellationNonPayment CancellationReason = "non_payment"

// PolicyCancellation is the cancellation of a policy for non-payment. It
// never has a refund.
type PolicyCancellation struct {
	PolicyID    string
	Reason      CancellationReason
	Installment Installment
	CancelledAt time.Time
	EventID     string
	// EmailSentAt is zero until the e-mail to the insured went out.
	EmailSentAt time.Time
}

// PaymentStatus is how the premium of a policy stands.
type PaymentStatus string

const (
	PaymentUpToDate PaymentStatus = "up_to_date"
	// PaymentPending: an instalment fell due unpaid; the policy is in its
	// grace period and keeps its coverage.
	PaymentPending PaymentStatus = "payment_pending"
	// PaymentCancelled: the policy was cancelled for non-payment.
	PaymentCancelled PaymentStatus = "cancelled"
)

// PolicyBilling is how the premium of a policy origination issued stands at a
// moment: what the wallet and the detail of the policy show.
type PolicyBilling struct {
	Status PaymentStatus
	// Pending is the oldest instalment that fell due unpaid, while the
	// policy is in its grace period.
	Pending *Installment
	// NextDueAt is when the next month falls due, while the policy is up to
	// date and its coverage has months left.
	NextDueAt    time.Time
	Cancellation *PolicyCancellation
	// Installments are the months billed, oldest first.
	Installments []Installment
}

// BillingOf is how the premium of the policy stands at now, from its
// instalments (oldest first) and its cancellation (nil when it has none).
func BillingOf(p Policy, r InstallmentRules, installments []Installment, c *PolicyCancellation, now time.Time) PolicyBilling {
	b := PolicyBilling{Status: PaymentUpToDate, Cancellation: c, Installments: installments}
	if c != nil {
		b.Status = PaymentCancelled
		return b
	}
	for i := range installments {
		if installments[i].Status == InstallmentPending {
			b.Status, b.Pending = PaymentPending, &installments[i]
			return b
		}
	}
	if next, ok := r.NextDueAt(p, now); ok {
		b.NextDueAt = next
	}
	return b
}

// Contract of the PolicyCancelledForNonPayment event (docs/events/README.md).
const (
	EventTypePolicyCancelledForNonPayment    = "PolicyCancelledForNonPayment"
	EventVersionPolicyCancelledForNonPayment = 1
	policyCancelledIDPrefix                  = "policy-cancelled-non-payment-"
)

// PolicyCancelledForNonPaymentID is the deterministic id of the event: a
// policy is cancelled once.
func PolicyCancelledForNonPaymentID(policyID string) string {
	return policyCancelledIDPrefix + policyID
}

// ErrInvalidCancellation is returned for a cancellation that misses what the
// notice to the insured must carry.
var ErrInvalidCancellation = errors.New("invalid cancellation for non-payment")

// PolicyCancelledForNonPayment is the event of a cancellation: what the
// notifications service tells the insured.
type PolicyCancelledForNonPayment struct {
	PolicyID string
	// Subject is the insured: the sub of their Cognito account.
	Subject        string
	InstallmentSeq int
	DueAt          time.Time
	GraceEndsAt    time.Time
	CancelledAt    time.Time
	AmountMinor    int64
	Currency       string
}

// NewPolicyCancelledForNonPayment builds the event of the cancellation of p
// for the unpaid instalment i at now.
func NewPolicyCancelledForNonPayment(p Policy, i Installment, now time.Time) PolicyCancelledForNonPayment {
	return PolicyCancelledForNonPayment{
		PolicyID: p.ID, Subject: p.CustomerID, InstallmentSeq: i.Seq, DueAt: i.DueAt.UTC(),
		GraceEndsAt: i.GraceEndsAt.UTC(), CancelledAt: now.UTC(), AmountMinor: i.AmountMinor, Currency: i.Currency,
	}
}

// Validate reports why the event cannot be published.
func (e PolicyCancelledForNonPayment) Validate() error {
	switch {
	case e.PolicyID == "" || e.Subject == "":
		return fmt.Errorf("%w: policy_id and subject are required", ErrInvalidCancellation)
	case e.InstallmentSeq < 2:
		return fmt.Errorf("%w: the unpaid instalment is month 2 or later", ErrInvalidCancellation)
	case e.DueAt.IsZero() || e.CancelledAt.IsZero() || !e.CancelledAt.After(e.DueAt):
		return fmt.Errorf("%w: the policy is cancelled after the instalment fell due", ErrInvalidCancellation)
	case e.AmountMinor <= 0 || e.Currency == "":
		return fmt.Errorf("%w: the unpaid amount and its currency are required", ErrInvalidCancellation)
	}
	return nil
}

// EventID is the id of the event.
func (e PolicyCancelledForNonPayment) EventID() string {
	return PolicyCancelledForNonPaymentID(e.PolicyID)
}

// policyCancelledPayload is the JSON payload of the event. refund is always
// "not_applicable": the cancellation for non-payment returns nothing.
type policyCancelledPayload struct {
	PolicyID       string    `json:"policy_id"`
	Subject        string    `json:"subject"`
	Reason         string    `json:"reason"`
	Line           string    `json:"line"`
	InstallmentSeq int       `json:"installment_seq"`
	DueAt          time.Time `json:"due_at"`
	GraceEndsAt    time.Time `json:"grace_ends_at"`
	CancelledAt    time.Time `json:"cancelled_at"`
	AmountMinor    int64     `json:"amount_minor"`
	Currency       string    `json:"currency"`
	Refund         string    `json:"refund"`
}

// RefundNotApplicable is the refund of a cancellation for non-payment.
const RefundNotApplicable = "not_applicable"

// Envelope wraps the cancellation in the envelope of pkg/events; it occurred
// when the policy was cancelled.
func (e PolicyCancelledForNonPayment) Envelope() (events.Envelope, error) {
	if err := e.Validate(); err != nil {
		return events.Envelope{}, err
	}
	return events.New(e.EventID(), EventTypePolicyCancelledForNonPayment, EventSource,
		EventVersionPolicyCancelledForNonPayment, e.CancelledAt, policyCancelledPayload{
			PolicyID: e.PolicyID, Subject: e.Subject, Reason: string(CancellationNonPayment), Line: string(LineLife),
			InstallmentSeq: e.InstallmentSeq, DueAt: e.DueAt.UTC(), GraceEndsAt: e.GraceEndsAt.UTC(),
			CancelledAt: e.CancelledAt.UTC(), AmountMinor: e.AmountMinor, Currency: e.Currency,
			Refund: RefundNotApplicable,
		})
}
