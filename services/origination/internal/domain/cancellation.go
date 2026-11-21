package domain

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/idempotency"
)

// HU-W1.6-01: the cancellation of a policy at the request of its insured, with
// the refund of the part of the premium they paid for the current month that
// the coverage will not use.
//
// The rule of the refund (decisions D-3 a and D-3r a, docs/contratos/HU-W1.6-01-api.md):
//
//  1. The cancellation takes effect at the end of the day it is confirmed, in
//     Colombia; the policy is cancelled from the confirmation on.
//  2. The premium is monthly (HU-W1.6-02): the current month n is the one with
//     DueAt(n) <= now < DueAt(n+1), and its period ends at DueAt(n+1), or at
//     the end of the coverage when that comes first.
//  3. The base is what was actually paid for that month: the charge of the
//     issuance for month 1, the instalment n when it is paid. Never the
//     current premium: an endorsement does not charge the difference of the
//     month in progress.
//  4. The period has D calendar days; the insured used U of them, from its
//     first day to the day of the cancellation, both included. N = D - U are
//     not used.
//  5. The refund is paid * N / D, truncated to the peso: never more than what
//     was not used. The percentage shown is N / D with one decimal.
//  6. There are no non-refundable fees.
//  7. A current month that was not paid (an instalment in its grace period,
//     or due and not billed yet) has nothing paid to give back: the refund is
//     0 and not applicable (decision D-6 a).
//
// In a demonstration (InstallmentRules.Day shorter than 24 hours) the days are
// days of the demonstration, with the same arithmetic.

// CancellationInsuredRequest: the insured asked to cancel the policy.
const CancellationInsuredRequest CancellationReason = "insured_request"

// InsuredCancellationReason is why the insured cancels (decision D-9 a): one
// of a fixed list, so it can be counted.
type InsuredCancellationReason string

// The reasons the insured can choose.
const (
	InsuredReasonPropertySold   InsuredCancellationReason = "property_sold"
	InsuredReasonMortgagePaid   InsuredCancellationReason = "mortgage_paid"
	InsuredReasonBetterOffer    InsuredCancellationReason = "better_offer"
	InsuredReasonNoLongerNeeded InsuredCancellationReason = "no_longer_needed"
	InsuredReasonFinancial      InsuredCancellationReason = "financial_reasons"
	InsuredReasonOther          InsuredCancellationReason = "other"
)

// InsuredCancellationReasons is the list, in the order the screen shows it.
var InsuredCancellationReasons = []InsuredCancellationReason{
	InsuredReasonPropertySold, InsuredReasonMortgagePaid, InsuredReasonBetterOffer, InsuredReasonNoLongerNeeded,
	InsuredReasonFinancial, InsuredReasonOther,
}

// ValidInsuredCancellationReason tells whether r is one of the list.
func ValidInsuredCancellationReason(r InsuredCancellationReason) bool {
	for _, v := range InsuredCancellationReasons {
		if v == r {
			return true
		}
	}
	return false
}

// MaxCancellationComments bounds the optional comments of the insured, in
// characters.
const MaxCancellationComments = 500

// RefundStatus is where the refund of a cancellation at the request of the
// insured stands (decision D-5 b): it is registered with the cancellation and
// sent to the gateway in the background.
type RefundStatus string

const (
	// RefundStatusNotApplicable: there was nothing paid to give back.
	RefundStatusNotApplicable RefundStatus = RefundNotApplicable
	// RefundInProcess: registered, the gateway has not confirmed it yet.
	RefundInProcess RefundStatus = "in_process"
	// RefundCompleted: the gateway confirmed it.
	RefundCompleted RefundStatus = "completed"
	// RefundFailed: the gateway rejected it, or the retry window ended.
	RefundFailed RefundStatus = "failed"
)

// RefundKeyPrefix starts the idempotency key of a refund: "reembolso-" + the
// policy id, never random, so every retry of the refund of a cancellation
// carries the same key (EC-D02).
const RefundKeyPrefix = "reembolso"

// RefundIdempotencyKey is the key of the refund of the policy: a policy is
// cancelled, and refunded, once.
func RefundIdempotencyKey(policyID string) string { return RefundKeyPrefix + "-" + policyID }

// FormatRefundReference is the reference of the n-th refund of a civil year
// of Colombia (decision D-5r a): REF-2026-00042.
func FormatRefundReference(year, n int) string { return fmt.Sprintf("REF-%04d-%05d", year, n) }

// RefundYear is the civil year of Colombia a refund registered at t is
// numbered in.
func RefundYear(t time.Time) int { return t.In(ColombiaTime).Year() }

// EndOfDayColombia is the last second of the civil day of Colombia t falls in.
func EndOfDayColombia(t time.Time) time.Time {
	y, m, d := t.In(ColombiaTime).Date()
	return time.Date(y, m, d, 23, 59, 59, 0, ColombiaTime).UTC()
}

// RefundMethodOf is what is kept of the payment method a refund goes back to:
// its kind, the brand and last four digits of a card or the bank of a PSE.
// Never its token.
func RefundMethodOf(m PaymentMethod) PaymentMethod {
	return PaymentMethod{Kind: m.Kind, Brand: m.Brand, Last4: m.Last4, BankCode: m.BankCode}
}

// PaidPeriodKind is how a month of the premium was paid.
type PaidPeriodKind string

const (
	// PaidByIssuance: month 1, paid by the charge of the issuance (HA-D02).
	PaidByIssuance PaidPeriodKind = "issuance"
	// PaidByInstallment: month 2 or later, an instalment (HU-W1.6-02).
	PaidByInstallment PaidPeriodKind = "installment"
)

// PaidMonth is what was paid for one month of the premium of a policy.
type PaidMonth struct {
	Seq  int
	Kind PaidPeriodKind
	// AmountMinor is what was paid, in minor units; 0 when the month was not
	// paid.
	AmountMinor int64
	Currency    string
	// ChargeReference is the gateway's id of the charge that paid it: the one
	// the refund goes back from.
	ChargeReference string
}

// PaidMonthOf is what was paid for month seq: the charge of the issuance for
// month 1 (when it is confirmed), the instalment seq when it is paid. Nothing
// otherwise.
func PaidMonthOf(seq int, issuance *Charge, installments []Installment) PaidMonth {
	out := PaidMonth{Seq: seq, Kind: PaidByInstallment}
	if seq == 1 {
		out.Kind = PaidByIssuance
		if issuance != nil && issuance.Status == ChargeConfirmed {
			out.AmountMinor, out.Currency = issuance.AmountMinor, issuance.Currency
			out.ChargeReference = issuance.ProviderReference
		}
		return out
	}
	for _, i := range installments {
		if i.Seq == seq && i.Status == InstallmentPaid {
			out.AmountMinor, out.Currency, out.ChargeReference = i.AmountMinor, i.Currency, i.ProviderReference
		}
	}
	return out
}

// CurrentMonth is the month of the premium the coverage is in at now, with its
// period [start, end): from its due date to the next one, or to the end of the
// coverage when that comes first. now is within the coverage.
func (r InstallmentRules) CurrentMonth(p Policy, now time.Time) (seq int, start, end time.Time) {
	months := r.Months(p)
	seq = 1
	for seq < months && !r.DueAt(p, seq+1).After(now) {
		seq++
	}
	start, end = r.DueAt(p, seq), p.CoverageEnd.UTC()
	if seq < months {
		end = r.DueAt(p, seq+1)
	}
	return seq, start, end
}

// civilDay is the civil day of Colombia t falls in, as a date at midnight UTC,
// so two of them differ in whole days.
func civilDay(t time.Time) time.Time {
	y, m, d := t.In(ColombiaTime).Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

func daysBetween(a, b time.Time) int { return int(b.Sub(a).Hours() / 24) }

// CancellationQuote is what the insured sees before confirming (CA-1): the
// month paid, the days not used, the percentage and the refund.
type CancellationQuote struct {
	PolicyID string
	// EffectiveAt is when the coverage ends: the end of the day.
	EffectiveAt time.Time
	// Paid is the current month and what was paid for it.
	Paid                   PaidMonth
	PeriodStart, PeriodEnd time.Time
	PeriodDays, UnusedDays int
	RefundAmountMinor      int64
	Currency               string
	// Method is where the refund goes back to: the payment method of the
	// issuance, without its token.
	Method PaymentMethod
	// PaymentStatus is how the premium stood when it was quoted.
	PaymentStatus PaymentStatus
}

// ProratePercent is the part of the period not used, in percent with one
// decimal.
func (q CancellationQuote) ProratePercent() float64 {
	return proratePercent(q.UnusedDays, q.PeriodDays)
}

func proratePercent(unused, period int) float64 {
	if period <= 0 {
		return 0
	}
	return math.Round(float64(unused)*1000/float64(period)) / 10
}

// RefundStatus is the status the refund is registered with: not applicable
// when there is nothing to give back, in process otherwise.
func (q CancellationQuote) RefundStatus() RefundStatus {
	if q.RefundAmountMinor <= 0 {
		return RefundStatusNotApplicable
	}
	return RefundInProcess
}

// QuoteCancellation applies the rule of the refund to policy p, in force at
// now, whose current month (InstallmentRules.CurrentMonth) was paid as paid
// says. A paid month that is not the current one counts as nothing paid.
func QuoteCancellation(p Policy, r InstallmentRules, paid PaidMonth, method PaymentMethod, status PaymentStatus,
	now time.Time) CancellationQuote {
	seq, start, end := r.CurrentMonth(p, now)
	if paid.Seq != seq {
		paid = PaidMonth{Seq: seq, Kind: PaidByInstallment}
		if seq == 1 {
			paid.Kind = PaidByIssuance
		}
	}
	q := CancellationQuote{PolicyID: p.ID, Paid: paid, PeriodStart: start, PeriodEnd: end, Currency: p.Currency,
		Method: RefundMethodOf(method), PaymentStatus: status}
	if paid.Currency != "" {
		q.Currency = paid.Currency
	}

	var used int
	if r.calendar() {
		q.EffectiveAt = EndOfDayColombia(now)
		q.PeriodDays = daysBetween(civilDay(start), civilDay(end))
		used = daysBetween(civilDay(start), civilDay(now)) + 1
	} else {
		used = int(now.Sub(start)/r.Day) + 1
		q.EffectiveAt = start.Add(time.Duration(used) * r.Day).UTC()
		q.PeriodDays = int(end.Sub(start) / r.Day)
	}
	if q.PeriodDays < 1 {
		q.PeriodDays = 1
	}
	if q.UnusedDays = q.PeriodDays - used; q.UnusedDays < 0 {
		q.UnusedDays = 0
	}
	if paid.AmountMinor > 0 {
		// Truncated to the peso: a minor unit is a cent.
		q.RefundAmountMinor = paid.AmountMinor * int64(q.UnusedDays) / int64(q.PeriodDays) / 100 * 100
	}
	return q
}

// ErrRefundChanged is returned when the refund the insured saw is not the one
// that would be registered now (the day changed, or a payment came in): the
// amount the insured confirms is the one that is registered.
var ErrRefundChanged = errors.New("the refund changed since it was quoted")

// CancellationRequest is what the insured confirms (CA-2).
type CancellationRequest struct {
	Reason   InsuredCancellationReason
	Comments string
	// RefundAmountMinor is the refund the insured saw before confirming.
	RefundAmountMinor int64
	// AcceptTerms is the box "I understand the cancellation is final".
	AcceptTerms bool
}

// Validate returns a *ValidationError naming every invalid field.
func (c CancellationRequest) Validate() error {
	var fields []FieldError
	if !ValidInsuredCancellationReason(c.Reason) {
		fields = append(fields, FieldError{Field: "reason", Code: "invalid"})
	}
	if utf8.RuneCountInString(c.Comments) > MaxCancellationComments {
		fields = append(fields, FieldError{Field: "comments", Code: "too_long"})
	}
	if c.RefundAmountMinor < 0 {
		fields = append(fields, FieldError{Field: "refund_amount", Code: "invalid"})
	}
	if !c.AcceptTerms {
		fields = append(fields, FieldError{Field: "accept_terms", Code: "required"})
	}
	if len(fields) > 0 {
		return &ValidationError{Fields: fields}
	}
	return nil
}

// Refund is the refund of a cancellation at the request of the insured. It is
// registered with the cancellation; only its processing changes afterwards.
type Refund struct {
	PolicyID string
	// Reference is REF-<year>-<number>; empty when nothing is refunded.
	Reference   string
	AmountMinor int64
	Currency    string
	Method      PaymentMethod
	// The calculation (CancellationQuote).
	Paid                   PaidMonth
	PeriodStart, PeriodEnd time.Time
	PeriodDays, UnusedDays int
	Status                 RefundStatus
	RegisteredAt           time.Time

	// The processing against the gateway (decision D-5 b, EC-D02).
	IdempotencyKey    string
	Attempts          int
	NextAttemptAt     time.Time
	ClaimedUntil      time.Time
	RetryUntil        time.Time
	ProviderReference string
	CompletedAt       time.Time
	FailureReason     string
	LastError         string
}

// NewRefund registers the refund of the quote at now; a refund in process is
// due at once and retried until retryUntil (zero for no window).
func NewRefund(q CancellationQuote, now, retryUntil time.Time) Refund {
	r := Refund{
		PolicyID: q.PolicyID, AmountMinor: q.RefundAmountMinor, Currency: q.Currency, Method: q.Method, Paid: q.Paid,
		PeriodStart: q.PeriodStart, PeriodEnd: q.PeriodEnd, PeriodDays: q.PeriodDays, UnusedDays: q.UnusedDays,
		Status: q.RefundStatus(), RegisteredAt: now.UTC(),
	}
	r.Method = RefundMethodOf(r.Method)
	if r.Status == RefundInProcess {
		r.IdempotencyKey, r.NextAttemptAt, r.RetryUntil = RefundIdempotencyKey(q.PolicyID), now.UTC(), retryUntil.UTC()
	}
	return r
}

// ProratePercent is the part of the period not used, in percent with one
// decimal.
func (r Refund) ProratePercent() float64 { return proratePercent(r.UnusedDays, r.PeriodDays) }

// AttemptState is the state of the refund as pkg/idempotency sees it: a
// refund in process is pending, or in progress while a worker holds it.
func (r Refund) AttemptState() idempotency.Attempt {
	status := idempotency.StatusPending
	switch {
	case r.Status == RefundCompleted:
		status = idempotency.StatusConfirmed
	case r.Status == RefundFailed:
		status = idempotency.StatusFailed
	case !r.ClaimedUntil.IsZero():
		status = idempotency.StatusInProgress
	}
	return idempotency.Attempt{
		ID: r.PolicyID, Key: r.IdempotencyKey, Status: status, FailedAttempts: r.Attempts, WindowEndsAt: r.RetryUntil,
		NextAttemptAt: r.NextAttemptAt, ClaimedUntil: r.ClaimedUntil,
	}
}

// InsuredCancellation is the cancellation a store writes: the policy, who
// asked, why, when it takes effect and its refund.
type InsuredCancellation struct {
	PolicyID string
	// RequestedBy is the subject of the insured's token.
	RequestedBy string
	Reason      InsuredCancellationReason
	Comments    string
	CancelledAt time.Time
	EffectiveAt time.Time
	Refund      Refund
}

// NewInsuredCancellation builds the cancellation the insured confirmed with
// the quote of now.
func NewInsuredCancellation(q CancellationQuote, req CancellationRequest, by string, now, retryUntil time.Time) InsuredCancellation {
	return InsuredCancellation{
		PolicyID: q.PolicyID, RequestedBy: by, Reason: req.Reason, Comments: strings.TrimSpace(req.Comments),
		CancelledAt: now.UTC(), EffectiveAt: q.EffectiveAt, Refund: NewRefund(q, now, retryUntil),
	}
}
