package domain

import "time"

// HU-M3.3-01: the insured is told every time their claim moves on to a stage
// they wait for: it is in evaluation, it was approved (with the amount) and
// its indemnity was paid (with the amount and the date).

// StageChange is one change of status of a claim, with what its notice tells:
// the claim, its insured and, once decided, the payout.
type StageChange struct {
	// ChangeID is the id of the change in the history of the claim; it grows
	// with every change of every claim.
	ChangeID       int64
	ClaimID        string
	FilingNumber   string
	PolicyID       string
	Subject        string
	Channel        Channel
	Status         Status
	PreviousStatus Status
	ChangedAt      time.Time

	// The payout of the approval, when there is one: what the insured
	// receives (the gross amount less the deductible) and when it was paid.
	PayoutMinor *int64
	Currency    string
	PaidAt      *time.Time
}

// Notifies tells whether the insured is told about the change:
//
//   - the filing entered evaluation;
//   - the claim was approved, coming from evaluation (a payout retried after
//     a payment exception goes back to approved, and that is not a new
//     approval);
//   - the indemnity was paid.
//
// A parametric claim is not told by stages: it goes through the three of them
// in seconds and its payment already has its own push (HU-M3.1-01).
func (c StageChange) Notifies() bool {
	if c.Channel == ChannelParametric {
		return false
	}
	switch c.Status {
	case StatusInEvaluation, StatusPaid:
		return true
	case StatusApproved:
		return c.PreviousStatus == StatusInEvaluation
	}
	return false
}
