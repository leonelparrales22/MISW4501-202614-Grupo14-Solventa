package domain

import (
	"time"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/idempotency"
)

// HU-W2.1-02: what the insured reads of the stage of their claims, without
// depending on a phone call. The list shows when each claim last changed and
// a tally of all of them (screen A7); the detail shows each stage with the
// decision and the provider assigned (screen A8).

// ClaimRef names a claim: its id and the filing number the insured quotes.
type ClaimRef struct {
	ID           string
	FilingNumber string
}

// ClaimTally is the tally of the insured's claims (screen A7): how many are in
// each status, the ones paid since the start of the year and what they paid,
// and the ones waiting for the insured (returned to correct).
type ClaimTally struct {
	ByStatus map[Status]int
	// PaidSince counts the claims paid since the start of the year, and
	// PaidAmountMinor is what they paid, in cents of Currency.
	PaidSince       int
	PaidAmountMinor int64
	Currency        string
	// PendingAction are the claims returned to the insured, newest filed
	// first, up to MaxPendingAction.
	PendingAction []ClaimRef
}

// MaxPendingAction bounds the returned claims the tally names.
const MaxPendingAction = 20

// InProgress counts the claims that are not closed: waiting for the advisor,
// returned, in evaluation, approved or with the payout stuck.
func (t ClaimTally) InProgress() int {
	n := 0
	for s, count := range t.ByStatus {
		if !Closed(s) {
			n += count
		}
	}
	return n
}

// Closed tells whether a claim in the status is finished: paid or rejected.
func Closed(s Status) bool { return s == StatusPaid || s == StatusRejected }

// StartOfYear is the first instant of the year of at in Colombian time: the
// year the insured reads in A7.
func StartOfYear(at time.Time) time.Time {
	local := at.In(ColombiaTime)
	return time.Date(local.Year(), time.January, 1, 0, 0, 0, 0, ColombiaTime).UTC()
}

// DecisionView is the decision of operations as the insured reads it (screen
// A8): the outcome, the amount the insurer pays and when, or why it was
// rejected. Never the account, the gross amount nor who decided.
type DecisionView struct {
	Outcome   Outcome
	DecidedAt time.Time
	// AmountMinor is the payout, net of the deductible, in cents of Currency;
	// zero in a rejection.
	AmountMinor int64
	Currency    string
	// PayoutStatus is pending, in_progress, confirmed or failed; empty in a
	// rejection. PaidAt is set once it is confirmed.
	PayoutStatus idempotency.Status
	PaidAt       *time.Time
	Reason       string
}

// ViewOf is the decision as the insured reads it.
func ViewOf(d Decision) DecisionView {
	v := DecisionView{Outcome: d.Outcome, DecidedAt: d.DecidedAt.UTC(), Reason: d.Reason}
	if p := d.Payout; p != nil {
		v.AmountMinor, v.Currency, v.PayoutStatus = p.AmountMinor, p.Currency, p.Status
		if v.Currency == "" {
			v.Currency = d.Currency
		}
		if p.Status == idempotency.StatusConfirmed && !p.ConfirmedAt.IsZero() {
			paid := p.ConfirmedAt.UTC()
			v.PaidAt = &paid
		}
	}
	return v
}

// ProviderView is the provider assigned to the claim as the insured reads it
// (screen A8): who attends and how far, never how to reach them.
type ProviderView struct {
	Name       string
	DistanceKm float64
	AssignedAt time.Time
}

// ProviderViewOf is the assignment as the insured reads it.
func ProviderViewOf(a Assignment) ProviderView {
	return ProviderView{Name: a.ProviderName, DistanceKm: a.DistanceKm, AssignedAt: a.AssignedAt.UTC()}
}
